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
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendellrocha/agentclip/internal/agents"
	"github.com/wendellrocha/agentclip/internal/bridge"
	"github.com/wendellrocha/agentclip/internal/buildinfo"
	"github.com/wendellrocha/agentclip/internal/clipboard"
	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/control"
	"github.com/wendellrocha/agentclip/internal/daemon"
	"github.com/wendellrocha/agentclip/internal/harness"
	"github.com/wendellrocha/agentclip/internal/mcpserver"
	"github.com/wendellrocha/agentclip/internal/release"
	"github.com/wendellrocha/agentclip/internal/remote"
	"github.com/wendellrocha/agentclip/internal/selfupgrade"
	"github.com/wendellrocha/agentclip/internal/setup"
	"github.com/wendellrocha/agentclip/internal/sshsession"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

const (
	clipboardTimeout = 5 * time.Second
	startupTimeout   = 3 * time.Second
	remotePortMin    = 32000
	remotePortMax    = 44999
)

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
	if err == nil && control.Healthy(state) {
		response, err := control.Arm(state, armed)
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
	if err != nil || !control.Healthy(state) {
		return errors.New("no local AgentClip bridge; copy an image and run `agentclip arm` first")
	}
	session, err := control.Session(state)
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
		Destination: arguments[0], SSHArgs: sshArgs, LocalPort: control.Port(state), RemotePort: remotePort,
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
		return harness.Install(home, arguments[1], *name, *port, *token, *uploadToken)
	case "remove":
		return harness.Remove(home, arguments[1], *name)
	default:
		return fmt.Errorf("unsupported harness action %q; expected install or remove", arguments[0])
	}
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
	options, err := setup.ParseArgs(arguments)
	if err != nil {
		return err
	}
	return setup.Runner{
		Stdout:         os.Stdout,
		LoadProfile:    companion.LoadProfile,
		EnsureIdentity: remote.EnsureSetupSSHIdentity,
		ReleaseTag:     setup.ReleaseTag,
		Install: func(destination, identityFile, tag string) error {
			_, err := remote.InstallVerified(context.Background(), destination, identityFile, tag, os.Stderr)
			return err
		},
		CompanionRunning: func(name string) bool {
			state, err := companion.LoadRuntime(name)
			return err == nil && companion.RuntimeHealthy(state)
		},
		StopCompanion:  stopCompanion,
		Pair:           pairProfileWithIdentity,
		StartCompanion: func(name string) error { return startCompanion(name, false) },
	}.Run(options)
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
	adapters, err := agents.Configure(profile, *agent)
	if err != nil {
		return err
	}
	fmt.Printf("Connected %s to profile %q.\n", agents.Display(adapters), profile.Name)
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
	adapter, err := agents.Resolve(*agent)
	if err != nil {
		return err
	}
	if err := remote.LoginForProfile(profile, adapter.RemoveArguments("agentclip-"+profile.Name)...).Run(); err != nil {
		return fmt.Errorf("remove AgentClip MCP from %s on %s: %w", adapter.DisplayName, profile.Destination, err)
	}
	fmt.Printf("Removed the AgentClip MCP entry from %s for profile %q. The harness remains installed.\n", adapter.DisplayName, profile.Name)
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
		adapters, err := agents.Configure(profile, agent)
		if err != nil {
			return companion.Profile{}, err
		}
		fmt.Printf("Configured %s on %s.\n", agents.Display(adapters), profile.Destination)
	}
	if err := companion.SaveProfile(profile); err != nil {
		return companion.Profile{}, err
	}
	return profile, nil
}

func runCompanion(arguments []string) error {
	if len(arguments) > 0 && arguments[0] == "autostart" {
		return runAutostart(arguments[1:], os.Stdout)
	}
	if len(arguments) < 2 || len(arguments) > 4 {
		return errors.New("usage: agentclip companion <start|stop|status|open|view|run|inbox> <profile> [--verbose] | agentclip companion <accept|reject> <profile> <offer-id> | agentclip companion autostart <enable|disable|status> <profile>")
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
	if err := companion.RequireLog(arguments[0]); err != nil {
		return err
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
	return selfupgrade.Runner{
		GOOS:           runtime.GOOS,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
		CurrentVersion: buildinfo.Version,
		LatestTag:      checker.FetchLatest,
		Executable:     os.Executable,
		Prepare: func(ctx context.Context, tag, executable string) (upgrader.StagedBinary, error) {
			return upgrader.Prepare(ctx, upgrader.Options{Version: tag, Executable: executable, SkipAttestation: os.Getenv(upgrader.SkipAttestationEnv) == "1"})
		},
		Profiles:         companion.ListProfiles,
		ActiveCompanions: activeCompanions,
		InstallRemote: func(profile companion.Profile, tag string) (bool, error) {
			return remote.InstallVerified(context.Background(), profile.Destination, profile.SSHIdentityFile, tag, os.Stderr)
		},
		StopCompanions:           stopCompanions,
		RestartCompanions:        restartCompanions,
		Replace:                  os.Rename,
		LaunchWindowsReplacement: launchWindowsReplacement,
	}.Run(ctx)
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
	// A login service must not start a second Companion for a profile that is
	// already running: they would fight over the state file and the tunnel. The
	// check and the claim (the runtime state written by StartControl) happen under
	// a start lock, so two starts at the same moment cannot both pass the check.
	releaseStart, err := acquireProfileStartLock(name, profileStartWait, profileStartStale)
	if err != nil {
		return err
	}
	startReleased := false
	defer func() {
		if !startReleased {
			releaseStart()
		}
	}()
	if state, err := companion.LoadRuntime(name); err == nil && companion.RuntimeHealthy(state) {
		if announce {
			return fmt.Errorf("Companion %q is already running; use `agentclip companion open %s`", name, name)
		}
		return nil
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
		defer func() { _ = control.Shutdown(state) }()
	}
	payload, err := json.Marshal(struct {
		ID          string `json:"id"`
		Token       string `json:"token"`
		UploadToken string `json:"upload_token,omitempty"`
	}{ID: "companion:" + profile.Name, Token: profile.Token, UploadToken: profile.UploadToken})
	if err != nil {
		return err
	}
	if err := control.Post(state, "/v1/control/persistent-session", payload, nil); err != nil {
		return fmt.Errorf("register companion session: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serviceStartedAt := time.Now().UTC()
	var releaseMu sync.RWMutex
	releaseState := release.Status{CurrentVersion: buildinfo.Version}
	if err := control.PublishReleaseStatus(state, releaseState); err != nil {
		logger.Error("publish initial release status: %v", err)
	}
	var tunnelMu sync.RWMutex
	tunnel := companion.TunnelStatus{UpdatedAt: time.Now().UTC()}
	stop := make(chan struct{}, 1)
	controlServer, err := companion.StartControl(profile.Name, func() any {
		tunnelMu.RLock()
		currentTunnel := tunnel
		tunnelMu.RUnlock()
		return companionDashboardStatus{
			Profile: profile.Name, Destination: profile.Destination, StartedAt: serviceStartedAt,
			Tunnel: currentTunnel, Clipboard: companionClipboardStatus(state, profile.Token), Inbound: control.InboundStatus(state), Release: func() release.Status {
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
	}, func(action, offerID string) error { return control.InboundAction(state, offerID, action) }, func(offerID string) (companion.InboundFileContent, error) {
		return control.InboundText(state, offerID)
	})
	if err != nil {
		logger.Error("start Companion control server: %v", err)
		return fmt.Errorf("start Companion control server: %w", err)
	}
	// The runtime state is written: a later start sees this Companion as running.
	startReleased = true
	releaseStart()
	defer controlServer.Close()
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
			return control.ArmSnapshot(state, items)
		},
		Log: logger,
	}
	go func() { errors <- watcher.Run(ctx) }()
	go func() {
		errors <- companion.RunTunnelWithLogger(ctx, profile, control.Port(state), func(status companion.TunnelStatus) {
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
	if !control.ValidLoopbackAddress(state.Address) {
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
	if logger, logErr := companion.NewLogger("bridge"); logErr == nil {
		defer logger.Close()
		d.Bridge.SetLogger(logger)
		logger.Info("bridge started: pid=%d", os.Getpid())
		defer logger.Info("bridge stopped")
	} else {
		fmt.Fprintf(os.Stderr, "bridge logging disabled: %v\n", logErr)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	return nil
}

func runDoctor() error {
	state, err := daemon.LoadState()
	if err != nil || !control.Healthy(state) {
		return errors.New("bridge: unavailable (copy an image and run `agentclip arm`)")
	}
	fmt.Printf("bridge: healthy at %s (PID %d)\n", state.Address, state.PID)
	return nil
}

func ensureBridge(initial *daemon.Image) (daemon.State, bool, error) {
	state, err := daemon.LoadState()
	if err == nil && control.Healthy(state) {
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
		if err == nil && state.PID == pid && control.Healthy(state) {
			_ = command.Process.Release()
			return state, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
	return daemon.State{}, errors.New("local bridge did not start within the expected time")
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
		if err := control.PublishReleaseStatus(state, status); err != nil {
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

// How long a start waits for another start of the same profile, and how old a
// start lock must be before it is taken for a leftover of a crashed start.
const (
	profileStartWait  = 25 * time.Second
	profileStartStale = 20 * time.Second
)

// acquireProfileStartLock serialises the starts of one profile. It is held only
// while a Companion decides whether to start and writes its runtime state, so a
// waiting start gets the lock as soon as the first one has either claimed the
// profile or given up.
func acquireProfileStartLock(name string, wait, stale time.Duration) (func(), error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("find AgentClip cache directory: %w", err)
	}
	directory := filepath.Join(cacheDir, "agentclip", "companions")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create AgentClip cache directory: %w", err)
	}
	path := filepath.Join(directory, name+".start.lock")
	deadline := time.Now().Add(wait)
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return func() {
				_ = file.Close()
				_ = os.Remove(path)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("lock the start of Companion %q: %w", name, err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > stale {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another start of Companion %q is in progress", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
