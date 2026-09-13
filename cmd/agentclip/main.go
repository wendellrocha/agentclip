package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendellrocha/agentclip/internal/bridge"
	"github.com/wendellrocha/agentclip/internal/buildinfo"
	"github.com/wendellrocha/agentclip/internal/clipboard"
	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/daemon"
	"github.com/wendellrocha/agentclip/internal/mcpserver"
	"github.com/wendellrocha/agentclip/internal/release"
	"github.com/wendellrocha/agentclip/internal/sshsession"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

const (
	clipboardTimeout  = 5 * time.Second
	startupTimeout    = 3 * time.Second
	remotePortMin     = 32000
	remotePortMax     = 44999
	releaseRepository = "wendellrocha/agentclip"
)

var releaseTagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$`)

type armResponse struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type sessionResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

type bridgeBootstrap struct {
	Image        *daemon.Image `json:"image,omitempty"`
	ControlToken string        `json:"control_token"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	var err error
	switch os.Args[1] {
	case "arm":
		err = runArm()
	case "ssh":
		err = runSSH(os.Args[2:])
	case "pair":
		err = runPair(os.Args[2:])
	case "setup":
		err = runSetup(os.Args[2:])
	case "connect":
		err = runConnect(os.Args[2:])
	case "disconnect", "uninstall":
		err = runUninstall(os.Args[2:])
	case "companion":
		err = runCompanion(os.Args[2:])
	case "mcp":
		err = runMCP()
	case "harness":
		err = runHarness(os.Args[2:])
	case "bridge":
		err = runBridge()
	case "doctor":
		err = runDoctor()
	case "logs":
		err = runLogs(os.Args[2:])
	case "upgrade":
		err = runUpgrade(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(buildinfo.Version)
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func runArm() error {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()
	image, err := clipboard.Capture(ctx, clipboard.NativeReader{})
	if err != nil {
		return fmt.Errorf("capture clipboard image: %w", err)
	}
	armed := daemon.Image{PNG: image.PNG, Width: image.Width, Height: image.Height}
	unlock, err := acquireBridgeLock()
	if err != nil {
		return err
	}
	defer unlock()

	state, err := daemon.LoadState()
	if err == nil && bridgeHealthy(state) {
		response, err := controlArm(state, armed)
		if err != nil {
			return err
		}
		fmt.Printf("Armed image: %dx%d PNG, expires at %s.\n", image.Width, image.Height, response.ExpiresAt.Local().Format(time.Kitchen))
		return nil
	}
	if _, err := startBridge(&armed); err != nil {
		return err
	}
	fmt.Printf("Armed image: %dx%d PNG, expires in %s.\n", image.Width, image.Height, 90*time.Second)
	return nil
}

func acquireBridgeLock() (func(), error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("find AgentClip cache directory: %w", err)
	}
	dir := filepath.Join(cacheDir, "agentclip")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create AgentClip cache directory: %w", err)
	}
	path := filepath.Join(dir, "bridge.lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		}
	}
	if err != nil {
		if os.IsExist(err) {
			return nil, errors.New("another AgentClip bridge operation is already in progress")
		}
		return nil, fmt.Errorf("lock AgentClip arm operation: %w", err)
	}
	return func() {
		_ = file.Close()
		_ = os.Remove(path)
	}, nil
}

func runSSH(arguments []string) error {
	if len(arguments) == 0 || arguments[0] == "--" {
		return errors.New("usage: agentclip ssh <ssh-destination> [-- <ssh arguments>]")
	}
	state, err := daemon.LoadState()
	if err != nil || !bridgeHealthy(state) {
		return errors.New("no local AgentClip bridge; copy an image and run `agentclip arm` first")
	}
	session, err := controlSession(state)
	if err != nil {
		return fmt.Errorf("create bridge session: %w", err)
	}
	remotePort, err := randomPort()
	if err != nil {
		return err
	}
	sshArgs := arguments[1:]
	if len(sshArgs) > 0 && sshArgs[0] == "--" {
		sshArgs = sshArgs[1:]
	}
	command, err := sshsession.Command(sshsession.Options{
		Destination: arguments[0], SSHArgs: sshArgs, LocalPort: statePort(state), RemotePort: remotePort,
		Session: sshsession.Session{ID: session.ID, Token: session.Token},
	})
	if err != nil {
		return err
	}
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	return command.Run()
}

func runMCP() error {
	port, err := strconv.Atoi(os.Getenv("AGENTCLIP_BRIDGE_PORT"))
	if err != nil {
		return fmt.Errorf("read AGENTCLIP_BRIDGE_PORT: %w", err)
	}
	provider, err := mcpserver.NewHTTPProvider(port, os.Getenv("AGENTCLIP_SESSION_TOKEN"), os.Getenv("AGENTCLIP_UPLOAD_TOKEN"))
	if err != nil {
		return err
	}
	return mcpserver.New(provider).Run(context.Background(), &mcp.StdioTransport{})
}

func runHarness(arguments []string) error {
	if len(arguments) < 2 {
		return errors.New("usage: agentclip harness <install|remove> <agy|opencode|pi> --name NAME [--port PORT --token TOKEN]")
	}
	settings := flag.NewFlagSet("harness", flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	name := settings.String("name", "", "AgentClip MCP entry name")
	port := settings.Int("port", 0, "bridge loopback port")
	token := settings.String("token", "", "bridge session token")
	uploadToken := settings.String("upload-token", "", "host upload token")
	if err := settings.Parse(arguments[2:]); err != nil {
		return fmt.Errorf("parse harness options: %w", err)
	}
	if *name == "" || settings.NArg() != 0 {
		return errors.New("usage: agentclip harness <install|remove> <agy|opencode|pi> --name NAME [--port PORT --token TOKEN]")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	switch arguments[0] {
	case "install":
		if *port < 1 || *port > 65535 || strings.TrimSpace(*token) == "" {
			return errors.New("harness install requires --port and --token")
		}
		return installHarness(home, arguments[1], *name, *port, *token, *uploadToken)
	case "remove":
		return removeHarness(home, arguments[1], *name)
	default:
		return fmt.Errorf("unsupported harness action %q; expected install or remove", arguments[0])
	}
}

type harnessMCPEntry struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type openCodeMCPEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Enabled     bool              `json:"enabled"`
}

func installHarness(home, harness, name string, port int, token string, uploadTokens ...string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("harness entry name is required")
	}
	uploadToken := ""
	if len(uploadTokens) > 0 {
		uploadToken = uploadTokens[0]
	}
	environment := map[string]string{
		"AGENTCLIP_BRIDGE_PORT":   strconv.Itoa(port),
		"AGENTCLIP_SESSION_TOKEN": token,
	}
	if strings.TrimSpace(uploadToken) != "" {
		environment["AGENTCLIP_UPLOAD_TOKEN"] = uploadToken
	}
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "agy", "antigravity":
		path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
		return updateJSONMCPEntry(path, "mcpServers", name, harnessMCPEntry{Command: "agentclip", Args: []string{"mcp"}, Env: environment})
	case "opencode":
		path := filepath.Join(home, ".config", "opencode", "opencode.json")
		return updateJSONMCPEntry(path, "mcp", name, openCodeMCPEntry{Type: "local", Command: []string{"agentclip", "mcp"}, Environment: environment, Enabled: true})
	case "pi":
		return writePiExtension(home, name, port, token, uploadToken)
	default:
		return fmt.Errorf("unsupported harness %q; supported harnesses: agy, opencode, pi", harness)
	}
}

func removeHarness(home, harness, name string) error {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "agy", "antigravity":
		return removeJSONMCPEntry(filepath.Join(home, ".gemini", "config", "mcp_config.json"), "mcpServers", name)
	case "opencode":
		return removeJSONMCPEntry(filepath.Join(home, ".config", "opencode", "opencode.json"), "mcp", name)
	case "pi":
		path := filepath.Join(home, ".pi", "agent", "extensions", name+".ts")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove Pi extension: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported harness %q; supported harnesses: agy, opencode, pi", harness)
	}
}

func updateJSONMCPEntry(path, section, name string, entry any) error {
	root, err := readJSONObject(path)
	if err != nil {
		return err
	}
	entries := map[string]json.RawMessage{}
	if raw, found := root[section]; found {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("read %s from %s: %w", section, path, err)
		}
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode AgentClip MCP entry: %w", err)
	}
	entries[name] = raw
	root[section], err = json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode %s: %w", section, err)
	}
	return writeJSONObject(path, root)
}

func removeJSONMCPEntry(path, section, name string) error {
	root, err := readJSONObject(path)
	if err != nil {
		return err
	}
	raw, found := root[section]
	if !found {
		return nil
	}
	entries := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("read %s from %s: %w", section, path, err)
	}
	if _, found := entries[name]; !found {
		return nil
	}
	delete(entries, name)
	if len(entries) == 0 {
		delete(root, section)
	} else {
		root[section], err = json.Marshal(entries)
		if err != nil {
			return fmt.Errorf("encode %s: %w", section, err)
		}
	}
	return writeJSONObject(path, root)
}

func readJSONObject(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	root := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return root, nil
}

func writeJSONObject(path string, root map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".agentclip-")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	return os.Chmod(path, 0600)
}

func writePiExtension(home, name string, port int, token, uploadToken string) error {
	directory := filepath.Join(home, ".pi", "agent", "extensions")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create Pi extension directory: %w", err)
	}
	path := filepath.Join(directory, name+".ts")
	if err := os.WriteFile(path, []byte(piExtensionSource(port, token, uploadToken)), 0600); err != nil {
		return fmt.Errorf("write Pi extension: %w", err)
	}
	return os.Chmod(path, 0600)
}

func piExtensionSource(port int, token, uploadToken string) string {
	return fmt.Sprintf(`// Generated by AgentClip. Remove with: agentclip uninstall <profile> --agent pi
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { lstat, mkdir, mkdtemp, rename, rm, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { basename, join } from "node:path";
import { Readable } from "node:stream";

const bridgeURL = "http://127.0.0.1:%d";
const token = %q;
const uploadToken = %q;

async function request(path: string, signal?: AbortSignal): Promise<Response> {
  const response = await fetch(bridgeURL + path, {
    headers: { Authorization: "Bearer " + token },
    signal,
  });
  if (!response.ok) throw new Error("AgentClip bridge returned " + response.status + ": " + (await response.text()));
  return response;
}

type StreamingRequestInit = RequestInit & { duplex?: "half" };

async function uploadRequest(path: string, init: StreamingRequestInit, signal?: AbortSignal): Promise<Response> {
  if (!uploadToken) throw new Error("host uploads are unavailable for this profile; run agentclip setup again");
  const response = await fetch(bridgeURL + path, { ...init, headers: { ...(init.headers || {}), Authorization: "Bearer " + uploadToken }, signal });
  if (!response.ok) throw new Error("AgentClip host inbox returned " + response.status + ": " + (await response.text()));
  return response;
}

async function remoteFileMetadata(path: string) {
  const info = await lstat(path);
  if (!info.isFile() || info.size > 50 * 1024 * 1024) throw new Error("remote file must be a regular file no larger than 50 MiB");
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return { name: basename(path), size: info.size, sha256: hash.digest("hex") };
}

async function waitForHostApproval(offerID: string, signal?: AbortSignal) {
  const deadline = Date.now() + 10 * 60_000;
  while (true) {
    const offer = await (await uploadRequest("/v1/inbound/offers/" + encodeURIComponent(offerID), { method: "GET" }, signal)).json() as { state: string };
    if (offer.state !== "pending") return offer;
    if (Date.now() >= deadline) throw new Error("waiting for host approval timed out");
    await new Promise(resolve => setTimeout(resolve, 250));
  }
}

function text(value: unknown) {
  return { content: [{ type: "text" as const, text: JSON.stringify(value, null, 2) }], details: {} };
}

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: "agentclip_update_status",
    label: "AgentClip update status",
    description: "Checks the AgentClip release status. When update_available is true, run agentclip upgrade on the host.",
    parameters: Type.Object({}),
    async execute(_id, _params, signal) {
      const status = await (await request("/v1/release", signal)).json() as { update_available?: boolean };
      return text({ ...status, ...(status.update_available ? { upgrade_command: "agentclip upgrade" } : {}) });
    },
  });

  pi.registerTool({
    name: "clipboard_status",
    label: "Clipboard status",
    description: "Lists clipboard items available after the user explicitly asks to inspect the clipboard. Returns metadata only.",
    parameters: Type.Object({}),
    async execute(_id, _params, signal) {
      return text(await (await request("/v1/status", signal)).json());
    },
  });

  pi.registerTool({
    name: "get_clipboard_image",
    label: "Get clipboard image",
    description: "Returns the currently armed clipboard image after the user explicitly asks to inspect it.",
    parameters: Type.Object({}),
    async execute(_id, _params, signal) {
      const response = await request("/v1/image", signal);
      const data = Buffer.from(await response.arrayBuffer()).toString("base64");
      return { content: [{ type: "image" as const, source: { type: "base64" as const, mediaType: "image/png", data } }], details: {} };
    },
  });

  pi.registerTool({
    name: "get_clipboard_text",
    label: "Get clipboard text",
    description: "Returns an armed clipboard text item after the user explicitly asks to inspect clipboard text.",
    parameters: Type.Object({ item_id: Type.String() }),
    async execute(_id, params, signal) {
      const response = await request("/v1/items/" + encodeURIComponent(params.item_id), signal);
      return { content: [{ type: "text" as const, text: await response.text() }], details: {} };
    },
  });

  pi.registerTool({
    name: "materialize_clipboard_files",
    label: "Materialize clipboard files",
    description: "Transfers explicitly requested clipboard files to private temporary paths on this server. Call only after the user asks to inspect those files.",
    parameters: Type.Object({ item_ids: Type.Array(Type.String(), { minItems: 1, maxItems: 5 }) }),
    async execute(_id, params, signal) {
      const status = await (await request("/v1/status", signal)).json() as { items?: Array<{ id: string; kind: string; name: string; size: number; sha256: string; consumed?: boolean }> };
      const items = new Map((status.items ?? []).map((item) => [item.id, item]));
      const selected = params.item_ids.map((id) => items.get(id));
      if (selected.some((item) => !item || item.kind !== "file" || item.consumed)) throw new Error("one or more requested clipboard items are unavailable files");
      const root = join(homedir(), ".cache", "agentclip", "inbox");
      await mkdir(root, { recursive: true, mode: 0o700 });
      const directory = await mkdtemp(join(root, "snapshot-"));
      const files: Array<{ item_id: string; path: string; size: number; sha256: string }> = [];
      try {
        for (const item of selected) {
          const response = await request("/v1/items/" + encodeURIComponent(item!.id), signal);
          const data = Buffer.from(await response.arrayBuffer());
          if (data.length !== item!.size || createHash("sha256").update(data).digest("hex") !== item!.sha256) throw new Error("clipboard file metadata changed during transfer");
          const path = join(directory, basename(item!.name) || item!.id);
          await writeFile(path, data, { mode: 0o600 });
          files.push({ item_id: item!.id, path, size: item!.size, sha256: item!.sha256 });
        }
      } catch (error) {
        await rm(directory, { recursive: true, force: true });
        throw error;
      }
      const timer = setTimeout(() => void rm(directory, { recursive: true, force: true }), 30 * 60_000);
      timer.unref();
      return text({ directory, files });
    },
  });

  pi.registerTool({
    name: "offer_file_to_host",
    label: "Offer file to host",
    description: "Offers one remote file for delivery to the host after the user explicitly asks, then waits for local approval. If accepted, immediately call Deliver file to host with the returned offer ID and the same path.",
    parameters: Type.Object({ path: Type.String() }),
    async execute(_id, params, signal) {
      const metadata = await remoteFileMetadata(params.path);
      const offer = await (await uploadRequest("/v1/inbound/offers", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(metadata) }, signal)).json() as { id: string };
      return text(await waitForHostApproval(offer.id, signal));
    },
  });

  pi.registerTool({
    name: "host_file_offer_status",
    label: "Host file offer status",
    description: "Checks whether the local user accepted, rejected, or expired a pending remote file offer.",
    parameters: Type.Object({ offer_id: Type.String() }),
    async execute(_id, params, signal) {
      return text(await (await uploadRequest("/v1/inbound/offers/" + encodeURIComponent(params.offer_id), { method: "GET" }, signal)).json());
    },
  });

  pi.registerTool({
    name: "deliver_file_to_host",
    label: "Deliver file to host",
    description: "Streams a remote file to the host only after the local user accepted its matching offer. Never choose a host destination path.",
    parameters: Type.Object({ offer_id: Type.String(), path: Type.String() }),
    async execute(_id, params, signal) {
      const metadata = await remoteFileMetadata(params.path);
      const offer = await (await uploadRequest("/v1/inbound/offers/" + encodeURIComponent(params.offer_id), { method: "GET" }, signal)).json() as { name: string; size: number; sha256: string; state: string };
      if (offer.state !== "accepted") throw new Error("host file offer is " + offer.state);
      if (offer.name !== metadata.name || offer.size !== metadata.size || offer.sha256 !== metadata.sha256) throw new Error("remote file changed after it was offered");
      const stream = Readable.toWeb(createReadStream(params.path)) as unknown as BodyInit;
      return text(await (await uploadRequest("/v1/inbound/offers/" + encodeURIComponent(params.offer_id) + "/content", { method: "PUT", headers: { "Content-Length": String(metadata.size) }, body: stream, duplex: "half" }, signal)).json());
    },
  });
}
`, port, token, uploadToken)
}

func runPair(arguments []string) error {
	if len(arguments) < 2 {
		return errors.New("usage: agentclip pair <profile> <ssh-destination> [--agent all|codex|claude|gemini|agy|opencode|pi] [--remote-port 39123] [--skip-agent]")
	}
	settings := flag.NewFlagSet("pair", flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	remotePort := settings.Int("remote-port", 39123, "remote loopback port")
	agent := settings.String("agent", "all", "remote agent: all, codex, claude, gemini, agy, opencode, or pi")
	skipAgent := settings.Bool("skip-agent", false, "do not configure an agent on the server")
	skipCodex := settings.Bool("skip-codex", false, "deprecated alias for --skip-agent")
	if err := settings.Parse(arguments[2:]); err != nil {
		return fmt.Errorf("parse pair options: %w", err)
	}
	profile, err := pairProfile(arguments[0], arguments[1], *remotePort, *agent, *skipAgent || *skipCodex)
	if err != nil {
		return err
	}
	fmt.Printf("Paired profile %q. Start it with: agentclip companion start %s\n", profile.Name, profile.Name)
	return nil
}

func runSetup(arguments []string) error {
	if len(arguments) < 1 {
		return errors.New("usage: agentclip setup <ssh-destination> [--profile NAME] [--agent all|codex|claude|gemini|agy|opencode|pi] [--version vX.Y.Z] [--remote-port 39123] [--skip-agent] [--skip-install] [--no-start]")
	}
	if strings.HasPrefix(arguments[0], "-") {
		return errors.New("SSH destination must not start with a dash")
	}
	settings := flag.NewFlagSet("setup", flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	profileName := settings.String("profile", "", "local Companion profile name")
	agent := settings.String("agent", "all", "remote agent: all, codex, claude, gemini, agy, opencode, or pi")
	releaseVersion := settings.String("version", "", "AgentClip release tag to install remotely")
	remotePort := settings.Int("remote-port", 39123, "remote loopback port")
	skipAgent := settings.Bool("skip-agent", false, "do not configure an agent on the server")
	skipCodex := settings.Bool("skip-codex", false, "deprecated alias for --skip-agent")
	skipInstall := settings.Bool("skip-install", false, "do not install AgentClip on the server")
	noStart := settings.Bool("no-start", false, "do not start the local Companion")
	if err := settings.Parse(arguments[1:]); err != nil {
		return fmt.Errorf("parse setup options: %w", err)
	}
	if settings.NArg() != 0 {
		return fmt.Errorf("unexpected setup arguments: %s", strings.Join(settings.Args(), " "))
	}

	name := *profileName
	if name == "" {
		name = defaultProfileName(arguments[0])
	}
	existingIdentityFile := ""
	if existing, err := companion.LoadProfile(name); err == nil {
		existingIdentityFile = existing.SSHIdentityFile
	}
	identityFile, err := ensureSetupSSHIdentity(arguments[0], name, existingIdentityFile)
	if err != nil {
		return err
	}
	if !*skipInstall {
		tag, err := releaseTag(*releaseVersion)
		if err != nil {
			return err
		}
		fmt.Printf("Installing AgentClip %s on %s...\n", tag, arguments[0])
		if err := remoteInstallCommandWithIdentity(arguments[0], identityFile, tag).Run(); err != nil {
			return fmt.Errorf("install AgentClip on %s: %w", arguments[0], err)
		}
	}

	// A running Companion would retain its old pairing token after re-setup.
	if state, err := companion.LoadRuntime(name); err == nil && companion.RuntimeHealthy(state) {
		if err := stopCompanion(name); err != nil {
			return err
		}
	}
	profile, err := pairProfileWithIdentity(name, arguments[0], *remotePort, *agent, *skipAgent || *skipCodex, identityFile)
	if err != nil {
		return err
	}
	if *noStart {
		fmt.Printf("Setup complete for %q. Start it with: agentclip companion start %s\n", profile.Name, profile.Name)
		return nil
	}
	if err := startCompanion(profile.Name, false); err != nil {
		return err
	}
	fmt.Printf("Setup complete for %q. SSH normally, then ask your agent to inspect the clipboard.\n", profile.Name)
	return nil
}

func runConnect(arguments []string) error {
	if len(arguments) < 1 {
		return errors.New("usage: agentclip connect <profile> [--agent all|codex|claude|gemini|agy|opencode|pi]")
	}
	settings := flag.NewFlagSet("connect", flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	agent := settings.String("agent", "all", "remote agent: all, codex, claude, gemini, agy, opencode, or pi")
	if err := settings.Parse(arguments[1:]); err != nil {
		return fmt.Errorf("parse connect options: %w", err)
	}
	if settings.NArg() != 0 {
		return errors.New("usage: agentclip connect <profile> [--agent all|codex|claude|gemini|agy|opencode|pi]")
	}
	profile, err := companion.LoadProfile(arguments[0])
	if err != nil {
		return err
	}
	adapters, err := configureRemoteAgents(profile, *agent)
	if err != nil {
		return err
	}
	fmt.Printf("Connected %s to profile %q.\n", displayAgents(adapters), profile.Name)
	return nil
}

func runUninstall(arguments []string) error {
	if len(arguments) < 1 {
		return errors.New("usage: agentclip uninstall <profile> --agent <codex|claude|gemini|agy|opencode|pi>")
	}
	settings := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	agent := settings.String("agent", "", "remote agent: codex, claude, gemini, agy, opencode, or pi")
	if err := settings.Parse(arguments[1:]); err != nil {
		return fmt.Errorf("parse uninstall options: %w", err)
	}
	if *agent == "" || settings.NArg() != 0 {
		return errors.New("usage: agentclip uninstall <profile> --agent <codex|claude|gemini|agy|opencode|pi>")
	}
	profile, err := companion.LoadProfile(arguments[0])
	if err != nil {
		return err
	}
	adapter, err := resolveAgentAdapter(*agent)
	if err != nil {
		return err
	}
	if err := remoteLoginForProfile(profile, adapter.removeArguments("agentclip-"+profile.Name)...).Run(); err != nil {
		return fmt.Errorf("remove AgentClip MCP from %s on %s: %w", adapter.displayName, profile.Destination, err)
	}
	fmt.Printf("Removed the AgentClip MCP entry from %s for profile %q. The harness remains installed.\n", adapter.displayName, profile.Name)
	return nil
}

func pairProfile(name, destination string, remotePort int, agent string, skipAgent bool) (companion.Profile, error) {
	return pairProfileWithIdentity(name, destination, remotePort, agent, skipAgent, "")
}

func pairProfileWithIdentity(name, destination string, remotePort int, agent string, skipAgent bool, identityFile string) (companion.Profile, error) {
	token, err := randomToken(32)
	if err != nil {
		return companion.Profile{}, err
	}
	uploadToken, err := randomToken(32)
	if err != nil {
		return companion.Profile{}, err
	}
	profile := companion.Profile{
		Name: name, Destination: destination, RemotePort: remotePort,
		Token: token, UploadToken: uploadToken, SSHIdentityFile: identityFile, CreatedAt: time.Now().UTC(),
	}
	if err := profile.Validate(); err != nil {
		return companion.Profile{}, err
	}
	if !skipAgent {
		adapters, err := configureRemoteAgents(profile, agent)
		if err != nil {
			return companion.Profile{}, err
		}
		fmt.Printf("Configured %s on %s.\n", displayAgents(adapters), profile.Destination)
	}
	if err := companion.SaveProfile(profile); err != nil {
		return companion.Profile{}, err
	}
	return profile, nil
}

func releaseTag(requested string) (string, error) {
	tag := strings.TrimSpace(requested)
	if tag == "" {
		tag = buildinfo.Version
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if strings.Contains(tag, "-dev") {
		return "", errors.New("the development build has no downloadable release; pass --version vX.Y.Z after publishing it, or use `pair` for a manual development setup")
	}
	if !releaseTagPattern.MatchString(tag) {
		return "", fmt.Errorf("release version must be a semantic tag such as v0.2.0, got %q", tag)
	}
	return tag, nil
}

func defaultProfileName(destination string) string {
	name := destination
	if index := strings.LastIndex(name, "@"); index >= 0 {
		name = name[index+1:]
	}
	var builder strings.Builder
	for _, character := range name {
		valid := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_'
		if valid {
			builder.WriteRune(character)
		} else if builder.Len() == 0 || !strings.HasSuffix(builder.String(), "-") {
			builder.WriteByte('-')
		}
	}
	name = strings.Trim(builder.String(), "-_")
	if name == "" {
		name = "server"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

type agentAdapter struct {
	id              string
	displayName     string
	executable      string
	addArguments    func(companion.Profile, string) []string
	removeArguments func(string) []string
}

func configureRemoteAgents(profile companion.Profile, selection string) ([]agentAdapter, error) {
	if strings.HasPrefix(profile.Destination, "-") {
		return nil, errors.New("SSH destination must not start with a dash")
	}
	if strings.EqualFold(strings.TrimSpace(selection), "all") {
		return configureAllRemoteAgents(profile)
	}
	adapter, err := resolveAgentAdapter(selection)
	if err != nil {
		return nil, err
	}
	if err := remotePreflightForProfile(profile, adapter.executable).Run(); err != nil {
		return nil, fmt.Errorf("remote preflight failed: install `agentclip` and `%s` on %s, or retry with --skip-agent: %w", adapter.executable, profile.Destination, err)
	}
	if err := configureRemoteAdapter(profile, adapter); err != nil {
		return nil, err
	}
	return []agentAdapter{adapter}, nil
}

func configureAllRemoteAgents(profile companion.Profile) ([]agentAdapter, error) {
	output, err := remoteSupportedAgentsForProfile(profile).Output()
	if err != nil {
		return nil, fmt.Errorf("remote preflight failed: install `agentclip` on %s, or retry with --skip-agent: %w", profile.Destination, err)
	}
	var configured []agentAdapter
	for _, agentID := range strings.Fields(string(output)) {
		adapter, err := resolveAgentAdapter(agentID)
		if err != nil {
			return nil, fmt.Errorf("read supported harnesses on %s: %w", profile.Destination, err)
		}
		if err := configureRemoteAdapter(profile, adapter); err != nil {
			return nil, err
		}
		configured = append(configured, adapter)
	}
	if len(configured) == 0 {
		return nil, fmt.Errorf("no supported harness is installed on %s; supported harnesses: codex, claude, gemini, agy, opencode, pi", profile.Destination)
	}
	return configured, nil
}

func configureRemoteAdapter(profile companion.Profile, adapter agentAdapter) error {
	name := "agentclip-" + profile.Name
	// Re-pairing deliberately replaces only AgentClip's own named MCP entry.
	_ = remoteLoginForProfile(profile, adapter.removeArguments(name)...).Run()
	if err := remoteLoginForProfile(profile, adapter.addArguments(profile, name)...).Run(); err != nil {
		return fmt.Errorf("configure %s MCP on %s: %w", adapter.displayName, profile.Destination, err)
	}
	return nil
}

func resolveAgentAdapter(agentID string) (agentAdapter, error) {
	switch strings.ToLower(strings.TrimSpace(agentID)) {
	case "codex":
		return agentAdapter{
			id: "codex", displayName: "Codex", executable: "codex",
			removeArguments: func(name string) []string {
				return []string{"codex", "mcp", "remove", name}
			},
			addArguments: func(profile companion.Profile, name string) []string {
				return append(agentEnvironmentArguments([]string{"codex", "mcp", "add", name}, profile), "--", "agentclip", "mcp")
			},
		}, nil
	case "claude", "claude-code":
		return agentAdapter{
			id: "claude", displayName: "Claude Code", executable: "claude",
			removeArguments: func(name string) []string {
				return []string{"claude", "mcp", "remove", "--scope", "user", name}
			},
			addArguments: func(profile companion.Profile, name string) []string {
				arguments := []string{"claude", "mcp", "add", name, "--scope", "user"}
				return append(agentEnvironmentArguments(arguments, profile), "--", "agentclip", "mcp")
			},
		}, nil
	case "gemini", "gemini-cli":
		return agentAdapter{
			id: "gemini", displayName: "Gemini CLI", executable: "gemini",
			removeArguments: func(name string) []string {
				return []string{"gemini", "mcp", "remove", "--scope", "user", name}
			},
			addArguments: func(profile companion.Profile, name string) []string {
				arguments := []string{"gemini", "mcp", "add", name, "agentclip", "mcp", "--scope", "user"}
				return agentEnvironmentArguments(arguments, profile)
			},
		}, nil
	case "agy", "antigravity", "antigravity-cli":
		return agentAdapter{
			id: "agy", displayName: "AGY / Antigravity CLI", executable: "agy",
			removeArguments: func(name string) []string {
				return []string{"agentclip", "harness", "remove", "agy", "--name", name}
			},
			addArguments: func(profile companion.Profile, name string) []string {
				return harnessInstallArguments("agy", profile, name)
			},
		}, nil
	case "opencode":
		return agentAdapter{
			id: "opencode", displayName: "OpenCode", executable: "opencode",
			removeArguments: func(name string) []string {
				return []string{"agentclip", "harness", "remove", "opencode", "--name", name}
			},
			addArguments: func(profile companion.Profile, name string) []string {
				return harnessInstallArguments("opencode", profile, name)
			},
		}, nil
	case "pi", "pi-coding-agent":
		return agentAdapter{
			id: "pi", displayName: "Pi Coding Agent", executable: "pi",
			removeArguments: func(name string) []string {
				return []string{"agentclip", "harness", "remove", "pi", "--name", name}
			},
			addArguments: func(profile companion.Profile, name string) []string {
				return harnessInstallArguments("pi", profile, name)
			},
		}, nil
	default:
		return agentAdapter{}, fmt.Errorf("unsupported agent %q; supported agents: codex, claude, gemini, agy, opencode, pi", agentID)
	}
}

func harnessInstallArguments(harness string, profile companion.Profile, name string) []string {
	arguments := []string{"agentclip", "harness", "install", harness, "--name", name, "--port", strconv.Itoa(profile.RemotePort), "--token", profile.Token}
	if profile.HasUploadToken() {
		arguments = append(arguments, "--upload-token", profile.UploadToken)
	}
	return arguments
}

func agentEnvironmentArguments(arguments []string, profile companion.Profile) []string {
	arguments = append(arguments,
		"--env", fmt.Sprintf("AGENTCLIP_BRIDGE_PORT=%d", profile.RemotePort),
		"--env", "AGENTCLIP_SESSION_TOKEN="+profile.Token,
	)
	if profile.HasUploadToken() {
		arguments = append(arguments, "--env", "AGENTCLIP_UPLOAD_TOKEN="+profile.UploadToken)
	}
	return arguments
}

func displayAgents(adapters []agentAdapter) string {
	names := make([]string, len(adapters))
	for index, adapter := range adapters {
		names[index] = adapter.displayName
	}
	return strings.Join(names, ", ")
}

func remotePreflightCommand(destination, agentExecutable string) *exec.Cmd {
	return remotePreflightCommandWithIdentity(destination, "", agentExecutable)
}

func remotePreflightForProfile(profile companion.Profile, agentExecutable string) *exec.Cmd {
	return remotePreflightCommandWithIdentity(profile.Destination, profile.SSHIdentityFile, agentExecutable)
}

func remotePreflightCommandWithIdentity(destination, identityFile, agentExecutable string) *exec.Cmd {
	// ssh combines all arguments after the destination into a remote shell
	// command. Use a login shell as well: remote AgentClip and Codex are often
	// installed through ~/.profile or Volta, neither of which a plain SSH
	// command is required to load.
	check := "export PATH=\"$HOME/.local/bin:$PATH\"; command -v agentclip >/dev/null && command -v " + shellQuote(agentExecutable) + " >/dev/null"
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(check))
}

func remoteSupportedAgentsCommand(destination string) *exec.Cmd {
	return remoteSupportedAgentsCommandWithIdentity(destination, "")
}

func remoteSupportedAgentsForProfile(profile companion.Profile) *exec.Cmd {
	return remoteSupportedAgentsCommandWithIdentity(profile.Destination, profile.SSHIdentityFile)
}

func remoteSupportedAgentsCommandWithIdentity(destination, identityFile string) *exec.Cmd {
	// This deliberately detects only built-in adapters. It neither installs
	// harnesses nor scans project-level configuration files.
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(remoteSupportedAgentsScript()))
}

func remoteSupportedAgentsScript() string {
	return "export PATH=\"$HOME/.local/bin:$PATH\"; command -v agentclip >/dev/null || exit 10; for agentclip_harness in codex claude gemini agy opencode pi; do command -v \"$agentclip_harness\" >/dev/null && printf '%s\\n' \"$agentclip_harness\"; done; true"
}

func remoteLoginCommand(destination string, arguments ...string) *exec.Cmd {
	return remoteLoginCommandWithIdentity(destination, "", arguments...)
}

func remoteLoginForProfile(profile companion.Profile, arguments ...string) *exec.Cmd {
	return remoteLoginCommandWithIdentity(profile.Destination, profile.SSHIdentityFile, arguments...)
}

func remoteLoginCommandWithIdentity(destination, identityFile string, arguments ...string) *exec.Cmd {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = shellQuote(argument)
	}
	script := "export PATH=\"$HOME/.local/bin:$PATH\"; " + strings.Join(quoted, " ")
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(script))
}

func remoteInstallCommand(destination, tag string) *exec.Cmd {
	return remoteInstallCommandWithIdentity(destination, "", tag)
}

func remoteInstallCommandWithIdentity(destination, identityFile, tag string) *exec.Cmd {
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(remoteInstallScript(tag)))
}

func remoteSSHCommand(destination, identityFile, remoteCommand string) *exec.Cmd {
	arguments := []string{}
	if identityFile != "" {
		arguments = append(arguments, "-i", identityFile, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes")
	}
	arguments = append(arguments, destination, remoteCommand)
	return exec.Command("ssh", arguments...)
}

// ensureSetupSSHIdentity preserves an already-working SSH key setup. When the
// destination rejects non-interactive key authentication, it creates a
// dedicated AgentClip key and installs its public half through one interactive
// password login before the rest of setup runs.
func ensureSetupSSHIdentity(destination, profileName, existingIdentityFile string) (string, error) {
	output, err := sshKeyCheckCommand(destination, existingIdentityFile).CombinedOutput()
	if err == nil {
		return existingIdentityFile, nil
	}
	if !sshAuthenticationFailure(output) {
		return "", fmt.Errorf("check SSH key authentication for %s: %s", destination, sshErrorSummary(output, err))
	}
	identityFile, publicKey, err := ensureAgentClipSSHKey(profileName)
	if err != nil {
		return "", err
	}
	fmt.Printf("Configuring a dedicated AgentClip SSH key for %s (your password may be requested once)...\n", destination)
	bootstrap := bootstrapSSHKeyCommand(destination, publicKey)
	bootstrap.Stdin, bootstrap.Stdout, bootstrap.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := bootstrap.Run(); err != nil {
		return "", fmt.Errorf("install AgentClip SSH key on %s: %w", destination, err)
	}
	output, err = sshKeyCheckCommand(destination, identityFile).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("verify AgentClip SSH key on %s: %s", destination, sshErrorSummary(output, err))
	}
	return identityFile, nil
}

func sshKeyCheckCommand(destination, identityFile string) *exec.Cmd {
	arguments := []string{"-o", "BatchMode=yes"}
	if identityFile != "" {
		arguments = append(arguments, "-i", identityFile, "-o", "IdentitiesOnly=yes")
	}
	arguments = append(arguments, destination, "true")
	return exec.Command("ssh", arguments...)
}

func sshAuthenticationFailure(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "permission denied") ||
		strings.Contains(message, "authentication failed") ||
		strings.Contains(message, "too many authentication failures") ||
		strings.Contains(message, "no supported authentication methods")
}

func sshErrorSummary(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}

func ensureAgentClipSSHKey(profileName string) (string, string, error) {
	identityFile, err := companion.SSHIdentityPath(profileName)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(identityFile), 0700); err != nil {
		return "", "", fmt.Errorf("create AgentClip SSH key directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(identityFile), 0700); err != nil {
		return "", "", fmt.Errorf("secure AgentClip SSH key directory: %w", err)
	}
	publicFile := identityFile + ".pub"
	privateInfo, privateErr := os.Lstat(identityFile)
	publicInfo, publicErr := os.Lstat(publicFile)
	if privateErr == nil && (!privateInfo.Mode().IsRegular() || privateInfo.Mode()&os.ModeSymlink != 0) {
		return "", "", errors.New("AgentClip SSH private key is not a regular file")
	}
	if publicErr == nil && (!publicInfo.Mode().IsRegular() || publicInfo.Mode()&os.ModeSymlink != 0) {
		return "", "", errors.New("AgentClip SSH public key is not a regular file")
	}
	if privateErr != nil && !os.IsNotExist(privateErr) {
		return "", "", fmt.Errorf("inspect AgentClip SSH private key: %w", privateErr)
	}
	if publicErr != nil && !os.IsNotExist(publicErr) {
		return "", "", fmt.Errorf("inspect AgentClip SSH public key: %w", publicErr)
	}
	if os.IsNotExist(privateErr) && !os.IsNotExist(publicErr) {
		return "", "", errors.New("AgentClip SSH public key exists without its private key")
	}
	if os.IsNotExist(privateErr) {
		command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "agentclip:"+profileName, "-f", identityFile)
		if output, err := command.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("generate AgentClip SSH key (requires ssh-keygen): %s", sshErrorSummary(output, err))
		}
	} else if os.IsNotExist(publicErr) {
		output, err := exec.Command("ssh-keygen", "-y", "-f", identityFile).Output()
		if err != nil {
			return "", "", fmt.Errorf("derive AgentClip SSH public key: %w", err)
		}
		if err := os.WriteFile(publicFile, output, 0600); err != nil {
			return "", "", fmt.Errorf("write AgentClip SSH public key: %w", err)
		}
	}
	if err := os.Chmod(identityFile, 0600); err != nil {
		return "", "", fmt.Errorf("secure AgentClip SSH private key: %w", err)
	}
	if err := os.Chmod(publicFile, 0600); err != nil {
		return "", "", fmt.Errorf("secure AgentClip SSH public key: %w", err)
	}
	publicKey, err := readAgentClipSSHPublicKey(publicFile)
	if err != nil {
		return "", "", err
	}
	return identityFile, publicKey, nil
}

func readAgentClipSSHPublicKey(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read AgentClip SSH public key: %w", err)
	}
	key := strings.TrimSpace(string(payload))
	if strings.ContainsAny(key, "\r\n") || !strings.HasPrefix(key, "ssh-ed25519 ") || len(strings.Fields(key)) < 2 {
		return "", errors.New("invalid AgentClip SSH public key")
	}
	return key, nil
}

func bootstrapSSHKeyCommand(destination, publicKey string) *exec.Cmd {
	script := strings.Join([]string{
		"set -eu",
		"umask 077",
		"mkdir -p \"$HOME/.ssh\"",
		"touch \"$HOME/.ssh/authorized_keys\"",
		"grep -qxF " + shellQuote(publicKey) + " \"$HOME/.ssh/authorized_keys\" || printf '%s\\n' " + shellQuote(publicKey) + " >> \"$HOME/.ssh/authorized_keys\"",
	}, "; ")
	return exec.Command("ssh", "-o", "NumberOfPasswordPrompts=1", destination, "sh -lc "+shellQuote(script))
}

func remoteInstallScript(tag string) string {
	// The installer detects the remote OS and architecture, verifies the release
	// checksum, and installs only into the remote user's home directory.
	installerURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/scripts/install.sh", releaseRepository, tag)
	return strings.Join([]string{
		"set -eu",
		"curl -fsSL --retry 3 " + shellQuote(installerURL) + " | sh -s -- --version " + shellQuote(tag),
	}, "; ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func runCompanion(arguments []string) error {
	if len(arguments) < 2 || len(arguments) > 4 {
		return errors.New("usage: agentclip companion <start|stop|status|open|view|run|inbox> <profile> [--verbose] | agentclip companion <accept|reject> <profile> <offer-id>")
	}
	verbose := false
	if len(arguments) == 3 && arguments[2] == "--verbose" && (arguments[0] == "start" || arguments[0] == "run" || arguments[0] == "serve") {
		verbose = true
	}
	if len(arguments) == 4 {
		return errors.New("unknown Companion option; expected --verbose")
	}
	switch arguments[0] {
	case "start":
		return startCompanion(arguments[1], verbose)
	case "stop":
		return stopCompanion(arguments[1])
	case "status":
		return printCompanionStatus(arguments[1])
	case "open", "view":
		return openCompanionView(arguments[1])
	case "run", "serve":
		if verbose {
			os.Setenv("AGENTCLIP_LOG_LEVEL", "debug")
		}
		return runCompanionService(arguments[1], arguments[0] == "run")
	case "inbox":
		return printCompanionInbox(arguments[1])
	case "accept", "reject":
		if len(arguments) != 3 {
			return errors.New("usage: agentclip companion <accept|reject> <profile> <offer-id>")
		}
		return companionInboundAction(arguments[1], arguments[2], arguments[0])
	default:
		return errors.New("usage: agentclip companion <start|stop|status|open|view|run|inbox> <profile>")
	}
}

func startCompanion(name string, verbose bool) error {
	if _, err := companion.LoadProfile(name); err != nil {
		return err
	}
	if state, err := companion.LoadRuntime(name); err == nil && companion.RuntimeHealthy(state) {
		return fmt.Errorf("Companion %q is already running; use `agentclip companion open %s`", name, name)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate AgentClip executable: %w", err)
	}
	command := exec.Command(executable, "companion", "serve", name)
	if verbose {
		command.Env = append(os.Environ(), "AGENTCLIP_LOG_LEVEL=debug")
	}
	command.Stdin, command.Stdout, command.Stderr = nil, io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return fmt.Errorf("start Companion: %w", err)
	}
	pid := command.Process.Pid
	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		state, err := companion.LoadRuntime(name)
		if err == nil && state.PID == pid && companion.RuntimeHealthy(state) {
			_ = command.Process.Release()
			fmt.Printf("Companion %q started. Open: agentclip companion open %s\nLogs: %s\n", name, name, companionLogPath(name))
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
	return errors.New("Companion did not start within the expected time")
}

func companionLogPath(name string) string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join("~", ".cache", "agentclip", "logs", name+".log")
	}
	return filepath.Join(cacheDir, "agentclip", "logs", name+".log")
}

func runLogs(arguments []string) error {
	if len(arguments) < 1 {
		return errors.New("usage: agentclip logs <profile> [--export caminho]")
	}
	settings := flag.NewFlagSet("logs", flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	exportPath := settings.String("export", "", "copy the complete log to this file")
	if err := settings.Parse(arguments[1:]); err != nil {
		return fmt.Errorf("parse logs options: %w", err)
	}
	if settings.NArg() != 0 {
		return errors.New("usage: agentclip logs <profile> [--export caminho]")
	}
	logger, err := companion.NewLogger(arguments[0])
	if err != nil {
		return err
	}
	defer logger.Close()
	if *exportPath != "" {
		if err := logger.Export(*exportPath); err != nil {
			return err
		}
		fmt.Printf("Logs exportados para %s\n", *exportPath)
		return nil
	}
	fmt.Println(logger.Path())
	return nil
}

type remoteUpgradeResult struct {
	Profile string
	Err     error
}

// runUpgrade updates every configured server, then atomically replaces the
// executable that launched this command. A remote failure is reported after
// every profile has been attempted and does not discard a verified local
// upgrade.
func runUpgrade(arguments []string) error {
	if len(arguments) != 0 {
		return errors.New("usage: agentclip upgrade")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	checker := release.NewChecker()
	tag, err := checker.FetchLatest(ctx)
	if err != nil {
		return fmt.Errorf("resolve latest AgentClip release: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running AgentClip executable: %w", err)
	}
	staged, err := upgrader.Prepare(ctx, upgrader.Options{Version: tag, Executable: executable})
	if err != nil {
		return fmt.Errorf("prepare AgentClip %s: %w", tag, err)
	}
	profiles, err := companion.ListProfiles()
	if err != nil {
		staged.Cleanup()
		return err
	}
	// Capture this before remote work starts: only Companions that were healthy
	// when the upgrade began are eligible for a later restart.
	active, err := activeCompanions(profiles)
	if err != nil {
		staged.Cleanup()
		return err
	}
	results := make([]remoteUpgradeResult, 0, len(profiles))
	for _, profile := range profiles {
		fmt.Printf("Updating %q on %s...\n", profile.Name, profile.Destination)
		err := remoteInstallCommandWithIdentity(profile.Destination, profile.SSHIdentityFile, tag).Run()
		results = append(results, remoteUpgradeResult{Profile: profile.Name, Err: err})
	}
	if err := stopCompanions(active); err != nil {
		staged.Cleanup()
		_ = restartCompanions(active)
		return err
	}
	if runtime.GOOS == "windows" {
		if err := launchWindowsReplacement(staged, active); err != nil {
			staged.Cleanup()
			_ = restartCompanions(active)
			return err
		}
		printRemoteUpgradeSummary(results)
		if hasRemoteUpgradeFailure(results) {
			return errors.New("one or more remote hosts could not be updated; the local update will finish shortly")
		}
		fmt.Printf("AgentClip %s will replace itself and restart %d Companion(s) shortly.\n", tag, len(active))
		return nil
	}
	if err := os.Rename(staged.Path, staged.Target); err != nil {
		staged.Cleanup()
		_ = restartCompanions(active)
		return fmt.Errorf("replace AgentClip executable: %w", err)
	}
	if err := restartCompanions(active); err != nil {
		printRemoteUpgradeSummary(results)
		return err
	}
	printRemoteUpgradeSummary(results)
	if hasRemoteUpgradeFailure(results) {
		return errors.New("one or more remote hosts could not be updated")
	}
	fmt.Printf("AgentClip updated to %s.\n", tag)
	return nil
}

func activeCompanions(profiles []companion.Profile) ([]string, error) {
	active := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		state, err := companion.LoadRuntime(profile.Name)
		if err == nil && companion.RuntimeHealthy(state) {
			active = append(active, profile.Name)
		}
	}
	return active, nil
}

func stopCompanions(names []string) error {
	for _, name := range names {
		state, err := companion.LoadRuntime(name)
		if err != nil || !companion.RuntimeHealthy(state) {
			continue
		}
		if err := companion.StopRuntime(state); err != nil {
			return fmt.Errorf("stop active Companion %q: %w", name, err)
		}
	}
	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		stopped := true
		for _, name := range names {
			state, err := companion.LoadRuntime(name)
			if err == nil && companion.RuntimeHealthy(state) {
				stopped = false
				break
			}
		}
		if stopped {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("timed out stopping active Companions")
}

func restartCompanions(names []string) error {
	var failures []string
	for _, name := range names {
		if err := startCompanion(name, false); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failures) != 0 {
		return fmt.Errorf("restart Companion(s): %s", strings.Join(failures, "; "))
	}
	return nil
}

func printRemoteUpgradeSummary(results []remoteUpgradeResult) {
	if len(results) == 0 {
		fmt.Println("No remote profiles configured.")
		return
	}
	fmt.Println("Remote update summary:")
	for _, result := range results {
		if result.Err == nil {
			fmt.Printf("  %s: updated\n", result.Profile)
		} else {
			fmt.Printf("  %s: failed (%v)\n", result.Profile, result.Err)
		}
	}
}

func hasRemoteUpgradeFailure(results []remoteUpgradeResult) bool {
	for _, result := range results {
		if result.Err != nil {
			return true
		}
	}
	return false
}

// launchWindowsReplacement delegates the locked .exe replacement to a fresh
// PowerShell process. It waits for this upgrade process to exit, swaps the
// verified staged executable, starts only the previously active Companions,
// then removes its own script.
func launchWindowsReplacement(staged upgrader.StagedBinary, companions []string) error {
	script, err := os.CreateTemp(filepath.Dir(staged.Target), ".agentclip-upgrade-*.ps1")
	if err != nil {
		return fmt.Errorf("create Windows upgrade helper: %w", err)
	}
	scriptPath := script.Name()
	profiles, err := json.Marshal(companions)
	if err == nil {
		_, err = script.WriteString(windowsReplacementScript())
	}
	if closeErr := script.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(scriptPath)
		return fmt.Errorf("write Windows upgrade helper: %w", err)
	}
	command := windowsReplacementCommand(scriptPath, staged, string(profiles), os.Getpid())
	if err := command.Start(); err != nil {
		_ = os.Remove(scriptPath)
		return fmt.Errorf("start Windows upgrade helper: %w", err)
	}
	return command.Process.Release()
}

func windowsReplacementCommand(scriptPath string, staged upgrader.StagedBinary, profiles string, processID int) *exec.Cmd {
	return exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", scriptPath, "-ProcessId", strconv.Itoa(processID), "-Source", staged.Path, "-Target", staged.Target, "-Profiles", profiles)
}

func windowsReplacementScript() string {
	return "param([int]$ProcessId,[string]$Source,[string]$Target,[string]$Profiles)\n" +
		"$ErrorActionPreference = 'Stop'\n" +
		"Wait-Process -Id $ProcessId\n" +
		"Move-Item -Force -LiteralPath $Source -Destination $Target\n" +
		"$profiles = ConvertFrom-Json $Profiles\n" +
		"foreach ($profile in @($profiles)) { Start-Process -WindowStyle Hidden -FilePath $Target -ArgumentList @('companion','serve',$profile) }\n" +
		"Remove-Item -Force -LiteralPath $PSCommandPath\n"
}

func stopCompanion(name string) error {
	state, err := companion.LoadRuntime(name)
	if err != nil || !companion.RuntimeHealthy(state) {
		return fmt.Errorf("Companion %q is not running", name)
	}
	if err := companion.StopRuntime(state); err != nil {
		return fmt.Errorf("stop Companion %q: %w", name, err)
	}
	fmt.Printf("Companion %q is stopping.\n", name)
	return nil
}

func printCompanionStatus(name string) error {
	state, err := companion.LoadRuntime(name)
	if err != nil || !companion.RuntimeHealthy(state) {
		return fmt.Errorf("Companion %q is not running", name)
	}
	payload, err := companion.FetchRuntimeStatus(state)
	if err != nil {
		return fmt.Errorf("get Companion status: %w", err)
	}
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, payload, "", "  "); err != nil {
		return err
	}
	fmt.Println(formatted.String())
	return nil
}

func printCompanionInbox(name string) error { return printCompanionStatus(name) }

func companionInboundAction(name, offerID, action string) error {
	state, err := companion.LoadRuntime(name)
	if err != nil || !companion.RuntimeHealthy(state) {
		return fmt.Errorf("Companion %q is not running", name)
	}
	if err := companion.InboundAction(state, offerID, action); err != nil {
		return fmt.Errorf("%s inbound offer: %w", action, err)
	}
	fmt.Printf("Inbound offer %q %sed.\n", offerID, action)
	return nil
}

func openCompanionView(name string) error {
	state, err := companion.LoadRuntime(name)
	if err != nil || !companion.RuntimeHealthy(state) {
		return fmt.Errorf("Companion %q is not running", name)
	}
	url := companion.ViewURL(state)
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", url)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open Companion view: %w", err)
	}
	_ = command.Process.Release()
	return nil
}

type companionDashboardStatus struct {
	Profile     string                    `json:"profile"`
	Destination string                    `json:"destination"`
	StartedAt   time.Time                 `json:"started_at"`
	Tunnel      companion.TunnelStatus    `json:"tunnel"`
	Clipboard   companionClipboardView    `json:"clipboard"`
	Inbound     bridge.InboundLocalStatus `json:"inbound"`
	Release     release.Status            `json:"release"`
}

type companionClipboardView struct {
	Armed     bool                     `json:"armed"`
	Consumed  bool                     `json:"consumed"`
	ExpiresAt time.Time                `json:"expires_at,omitempty"`
	Items     []companionClipboardItem `json:"items,omitempty"`
	Error     string                   `json:"error,omitempty"`
}

type companionClipboardItem struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	Size int64  `json:"size"`
}

func runCompanionService(name string, announce bool) error {
	profile, err := companion.LoadProfile(name)
	if err != nil {
		return err
	}
	logger, err := companion.NewLogger(name)
	if err != nil {
		return err
	}
	defer logger.Close()
	logger.Info("Companion starting: profile=%s destination=%s", profile.Name, profile.Destination)
	unlock, err := acquireBridgeLock()
	if err != nil {
		logger.Error("bridge unavailable: %v", err)
		return err
	}
	state, started, err := ensureBridge(nil)
	unlock()
	if err != nil {
		return err
	}
	if started {
		defer func() { _ = controlPost(state, "/v1/control/shutdown", nil, nil) }()
	}
	payload, err := json.Marshal(struct {
		ID          string `json:"id"`
		Token       string `json:"token"`
		UploadToken string `json:"upload_token,omitempty"`
	}{ID: "companion:" + profile.Name, Token: profile.Token, UploadToken: profile.UploadToken})
	if err != nil {
		return err
	}
	if err := controlPost(state, "/v1/control/persistent-session", payload, nil); err != nil {
		return fmt.Errorf("register companion session: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serviceStartedAt := time.Now().UTC()
	var releaseMu sync.RWMutex
	releaseState := release.Status{CurrentVersion: buildinfo.Version}
	if err := publishReleaseStatus(state, releaseState); err != nil {
		logger.Error("publish initial release status: %v", err)
	}
	var tunnelMu sync.RWMutex
	tunnel := companion.TunnelStatus{UpdatedAt: time.Now().UTC()}
	stop := make(chan struct{}, 1)
	control, err := companion.StartControl(profile.Name, func() any {
		tunnelMu.RLock()
		currentTunnel := tunnel
		tunnelMu.RUnlock()
		return companionDashboardStatus{
			Profile: profile.Name, Destination: profile.Destination, StartedAt: serviceStartedAt,
			Tunnel: currentTunnel, Clipboard: companionClipboardStatus(state, profile.Token), Inbound: companionInboundStatus(state), Release: func() release.Status {
				releaseMu.RLock()
				defer releaseMu.RUnlock()
				return releaseState
			}(),
		}
	}, func() {
		select {
		case stop <- struct{}{}:
		default:
		}
	}, func(action, offerID string) error { return controlInboundAction(state, offerID, action) }, func(offerID string) (companion.InboundFileContent, error) {
		return controlInboundText(state, offerID)
	})
	if err != nil {
		logger.Error("start Companion control server: %v", err)
		return fmt.Errorf("start Companion control server: %w", err)
	}
	defer control.Close()
	go watchReleaseStatus(ctx, state, logger, func(next release.Status) {
		releaseMu.Lock()
		releaseState = next
		releaseMu.Unlock()
	})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	errors := make(chan error, 2)
	watcher := companion.SnapshotWatcher{
		Source: companion.HostSnapshotSource{},
		Arm: func(ctx context.Context, items []bridge.Item) error {
			return controlArmSnapshot(state, items)
		},
		Log: logger,
	}
	go func() { errors <- watcher.Run(ctx) }()
	go func() {
		errors <- companion.RunTunnelWithLogger(ctx, profile, statePort(state), func(status companion.TunnelStatus) {
			tunnelMu.Lock()
			tunnel = status
			tunnelMu.Unlock()
		}, logger)
	}()
	if announce {
		fmt.Printf("Companion %q is running; SSH normally, then ask your agent to inspect the clipboard.\n", profile.Name)
	}
	var runErr error
	received := 0
	select {
	case <-signals:
	case <-stop:
	case runErr = <-errors:
		received = 1
	}
	cancel()
	for received < 2 {
		if err := <-errors; runErr == nil && err != nil {
			runErr = err
		}
		received++
	}
	if runErr != nil {
		logger.Error("Companion stopped with error: %v", runErr)
	} else {
		logger.Info("Companion stopped")
	}
	return runErr
}

func companionClipboardStatus(state daemon.State, token string) companionClipboardView {
	if !validLoopbackAddress(state.Address) {
		return companionClipboardView{Error: "bridge unavailable"}
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+state.Address+"/v1/status", nil)
	if err != nil {
		return companionClipboardView{Error: "bridge unavailable"}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		return companionClipboardView{Error: "bridge unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return companionClipboardView{}
	}
	if response.StatusCode != http.StatusOK {
		return companionClipboardView{Error: "clipboard status unavailable"}
	}
	var view companionClipboardView
	if err := json.NewDecoder(io.LimitReader(response.Body, 8*1024)).Decode(&view); err != nil {
		return companionClipboardView{Error: "clipboard status unavailable"}
	}
	return view
}

func runBridge() error {
	var bootstrap bridgeBootstrap
	if err := json.NewDecoder(io.LimitReader(os.Stdin, clipboard.MaxBytes*2)).Decode(&bootstrap); err != nil {
		return fmt.Errorf("read bridge bootstrap: %w", err)
	}
	if bootstrap.ControlToken == "" {
		return errors.New("bridge bootstrap did not include a control token")
	}
	d, err := daemon.Start(bootstrap.Image, bootstrap.ControlToken)
	if err != nil {
		return fmt.Errorf("start local bridge: %w", err)
	}
	defer d.Close()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	return nil
}

func runDoctor() error {
	state, err := daemon.LoadState()
	if err != nil || !bridgeHealthy(state) {
		return errors.New("bridge: unavailable (copy an image and run `agentclip arm`)")
	}
	fmt.Printf("bridge: healthy at %s (PID %d)\n", state.Address, state.PID)
	return nil
}

func ensureBridge(initial *daemon.Image) (daemon.State, bool, error) {
	state, err := daemon.LoadState()
	if err == nil && bridgeHealthy(state) {
		return state, false, nil
	}
	state, err = startBridge(initial)
	return state, true, err
}

func startBridge(image *daemon.Image) (daemon.State, error) {
	token, err := randomToken(32)
	if err != nil {
		return daemon.State{}, err
	}
	payload, err := json.Marshal(bridgeBootstrap{Image: image, ControlToken: token})
	if err != nil {
		return daemon.State{}, fmt.Errorf("encode bridge bootstrap: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return daemon.State{}, fmt.Errorf("locate AgentClip executable: %w", err)
	}
	command := exec.Command(executable, "bridge")
	command.Stdin, command.Stdout, command.Stderr = bytes.NewReader(payload), io.Discard, os.Stderr
	if err := command.Start(); err != nil {
		return daemon.State{}, fmt.Errorf("start local bridge process: %w", err)
	}
	pid := command.Process.Pid
	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		state, err := daemon.LoadState()
		if err == nil && state.PID == pid && bridgeHealthy(state) {
			_ = command.Process.Release()
			return state, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
	return daemon.State{}, errors.New("local bridge did not start within the expected time")
}

func controlArm(state daemon.State, image daemon.Image) (armResponse, error) {
	payload, err := json.Marshal(struct {
		PNG    string `json:"png"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}{base64.StdEncoding.EncodeToString(image.PNG), image.Width, image.Height})
	if err != nil {
		return armResponse{}, err
	}
	var result armResponse
	return result, controlPost(state, "/v1/control/arm", payload, &result)
}

func controlArmSnapshot(state daemon.State, items []bridge.Item) error {
	type snapshotItem struct {
		ID       string          `json:"id"`
		Kind     bridge.ItemKind `json:"kind"`
		MIMEType string          `json:"mime_type"`
		Name     string          `json:"name"`
		Data     string          `json:"data,omitempty"`
		Width    int             `json:"width,omitempty"`
		Height   int             `json:"height,omitempty"`
		File     *bridge.FileRef `json:"file,omitempty"`
	}
	payload := struct {
		Items []snapshotItem `json:"items"`
	}{Items: make([]snapshotItem, 0, len(items))}
	for _, item := range items {
		entry := snapshotItem{ID: item.ID, Kind: item.Kind, MIMEType: item.MIMEType, Name: item.Name, Width: item.Width, Height: item.Height, File: item.File}
		if len(item.Data) > 0 {
			entry.Data = base64.StdEncoding.EncodeToString(item.Data)
		}
		payload.Items = append(payload.Items, entry)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return controlPost(state, "/v1/control/snapshot", data, nil)
}

func controlSession(state daemon.State) (sessionResponse, error) {
	var result sessionResponse
	return result, controlPost(state, "/v1/control/sessions", nil, &result)
}

func publishReleaseStatus(state daemon.State, status release.Status) error {
	payload, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return controlPost(state, "/v1/control/release", payload, nil)
}

// watchReleaseStatus checks once after Companion startup and then relies on
// the shared disk cache to avoid repeated GitHub calls from other Companions.
func watchReleaseStatus(ctx context.Context, state daemon.State, logger *companion.Logger, set func(release.Status)) {
	checker := release.NewChecker()
	refresh := func() {
		status, err := checker.Current(ctx)
		if err != nil {
			logger.Error("release check failed: %v", err)
		}
		set(status)
		if err := publishReleaseStatus(state, status); err != nil {
			logger.Error("publish release status: %v", err)
		}
	}
	refresh()
	ticker := time.NewTicker(release.CacheTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func companionInboundStatus(state daemon.State) bridge.InboundLocalStatus {
	var result bridge.InboundLocalStatus
	if err := controlGet(state, "/v1/control/inbound", &result); err != nil {
		return bridge.InboundLocalStatus{}
	}
	return result
}

func controlInboundAction(state daemon.State, offerID, action string) error {
	if action != "accept" && action != "reject" {
		return errors.New("invalid inbound action")
	}
	return controlPost(state, "/v1/control/inbound/"+offerID+"/"+action, nil, nil)
}

func controlInboundText(state daemon.State, offerID string) (companion.InboundFileContent, error) {
	if !validLoopbackAddress(state.Address) || strings.TrimSpace(offerID) == "" {
		return companion.InboundFileContent{}, errors.New("received file is unavailable")
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+state.Address+"/v1/control/inbound/"+offerID+"/file", nil)
	if err != nil {
		return companion.InboundFileContent{}, err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	response, err := (&http.Client{Timeout: bridge.InboundTransferTimeout}).Do(request)
	if err != nil {
		return companion.InboundFileContent{}, err
	}
	if response.StatusCode != http.StatusOK || response.ContentLength < 0 {
		response.Body.Close()
		return companion.InboundFileContent{}, errors.New("received file is unavailable")
	}
	_, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	if err != nil || strings.TrimSpace(parameters["filename"]) == "" {
		response.Body.Close()
		return companion.InboundFileContent{}, errors.New("received file is unavailable")
	}
	return companion.InboundFileContent{Name: parameters["filename"], Size: response.ContentLength, Previewable: response.Header.Get("X-AgentClip-Previewable") == "true", Reader: response.Body}, nil
}

func controlPost(state daemon.State, path string, payload []byte, output any) error {
	if !validLoopbackAddress(state.Address) {
		return errors.New("bridge state has an invalid loopback address")
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+state.Address+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return fmt.Errorf("bridge control returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func controlGet(state daemon.State, path string, output any) error {
	if !validLoopbackAddress(state.Address) {
		return errors.New("bridge state has an invalid loopback address")
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+state.Address+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return fmt.Errorf("bridge control returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func bridgeHealthy(state daemon.State) bool {
	if !validLoopbackAddress(state.Address) {
		return false
	}
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + state.Address + "/healthz")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func statePort(state daemon.State) int {
	_, port, err := net.SplitHostPort(state.Address)
	if err != nil {
		return 0
	}
	value, _ := strconv.Atoi(port)
	return value
}

func validLoopbackAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	value, err := strconv.Atoi(port)
	return err == nil && value > 0 && value <= 65535
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate secure random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func randomPort() (int, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(remotePortMax-remotePortMin+1))
	if err != nil {
		return 0, fmt.Errorf("choose remote SSH port: %w", err)
	}
	return remotePortMin + int(value.Int64()), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: agentclip <command>")
	fmt.Fprintln(os.Stderr, "commands: arm, ssh, pair, setup, connect, uninstall, companion, logs, mcp, harness, doctor, version")
}
