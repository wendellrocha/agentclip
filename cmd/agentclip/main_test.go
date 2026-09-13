package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

func TestRemotePreflightPassesTheWholeCheckAsOneRemoteCommand(t *testing.T) {
	command := remotePreflightCommand("bastion-m2", "codex")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	if !strings.HasPrefix(command.Args[2], "sh -lc '") || !strings.Contains(command.Args[2], "command -v agentclip") || !strings.Contains(command.Args[2], "codex") {
		t.Fatalf("remote command = %q", command.Args[2])
	}
}

func TestRemoteSupportedAgentsChecksOnlyBuiltInHarnesses(t *testing.T) {
	command := remoteSupportedAgentsCommand("bastion-m2")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	for _, required := range []string{"command -v agentclip", "codex", "claude", "gemini", "agy", "opencode", "pi", "agentclip_harness"} {
		if !strings.Contains(command.Args[2], required) {
			t.Fatalf("remote command = %q; missing %q", command.Args[2], required)
		}
	}
}

func TestRemoteSupportedAgentsScriptSucceedsWhenSomeHarnessesAreAbsent(t *testing.T) {
	if !strings.HasSuffix(remoteSupportedAgentsScript(), "; true") {
		t.Fatalf("detection script must not return a missing harness status: %q", remoteSupportedAgentsScript())
	}
}

func TestRemoteLoginCommandEscapesArgumentsInsideLoginShell(t *testing.T) {
	command := remoteLoginCommand("bastion-m2", "codex", "mcp", "add", "agentclip-m2", "--env", "TOKEN=has'aquote")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	if !strings.HasPrefix(command.Args[2], "sh -lc '") || !strings.Contains(command.Args[2], "'\"'\"'") {
		t.Fatalf("remote login command is not safely quoted: %q", command.Args[2])
	}
}

func TestRemoteInstallCommandPinsTheRequestedRelease(t *testing.T) {
	command := remoteInstallCommand("bastion-m2", "v0.2.0")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	if !strings.HasPrefix(command.Args[2], "sh -lc '") {
		t.Fatalf("remote installer command = %q", command.Args[2])
	}
	script := remoteInstallScript("v0.2.0")
	if !strings.Contains(script, "https://raw.githubusercontent.com/wendellrocha/agentclip/v0.2.0/scripts/install.sh") || !strings.Contains(script, "--version 'v0.2.0'") {
		t.Fatalf("remote installer script = %q", script)
	}
}

func TestWindowsUpgradeHelperCommandCarriesStagedBinaryAndActiveProfiles(t *testing.T) {
	staged := upgrader.StagedBinary{Path: `C:\\Temp\\agentclip.new`, Target: `C:\\Tools\\agentclip.exe`}
	command := windowsReplacementCommand(`C:\\Temp\\upgrade.ps1`, staged, `["dev","prod"]`, 42)
	arguments := strings.Join(command.Args, " ")
	for _, expected := range []string{"powershell", "-NoProfile", "-ExecutionPolicy Bypass", "-ProcessId 42", staged.Path, staged.Target, `["dev","prod"]`} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("Windows helper command %q does not contain %q", arguments, expected)
		}
	}
	for _, expected := range []string{"Wait-Process -Id $ProcessId", "Move-Item -Force", "Start-Process -WindowStyle Hidden", "Remove-Item -Force -LiteralPath $PSCommandPath"} {
		if !strings.Contains(windowsReplacementScript(), expected) {
			t.Fatalf("Windows helper script does not contain %q", expected)
		}
	}
}

func TestManagedSSHCommandsUseDedicatedIdentityWithoutPrompts(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "agentclip-key")
	install := remoteInstallCommandWithIdentity("bastion-m2", identity, "v0.2.0")
	login := remoteLoginCommandWithIdentity("bastion-m2", identity, "true")
	for _, command := range []*exec.Cmd{install, login, sshKeyCheckCommand("bastion-m2", identity)} {
		arguments := strings.Join(command.Args, " ")
		for _, required := range []string{"-i " + identity, "IdentitiesOnly=yes", "BatchMode=yes", "bastion-m2"} {
			if !strings.Contains(arguments, required) {
				t.Fatalf("SSH command %q does not contain %q", arguments, required)
			}
		}
	}
}

func TestBootstrapSSHKeyCommandIsIdempotentAndLimitsPasswordPrompts(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest agentclip:dev"
	command := bootstrapSSHKeyCommand("bastion-m2", key)
	arguments := strings.Join(command.Args, " ")
	for _, required := range []string{"NumberOfPasswordPrompts=1", "grep -qxF", "authorized_keys", key} {
		if !strings.Contains(arguments, required) {
			t.Fatalf("bootstrap command %q does not contain %q", arguments, required)
		}
	}
}

func TestSSHAuthenticationFailureRecognition(t *testing.T) {
	for _, output := range []string{
		"Permission denied (publickey,password).",
		"Authentication failed.",
		"Received disconnect: Too many authentication failures",
	} {
		if !sshAuthenticationFailure([]byte(output)) {
			t.Fatalf("authentication failure %q was not recognized", output)
		}
	}
	if sshAuthenticationFailure([]byte("ssh: connect to host bastion port 22: Connection refused")) {
		t.Fatal("network failure must not trigger key bootstrap")
	}
}

func TestEnsureAgentClipSSHKeyCreatesPrivateEd25519Key(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not available")
	}
	t.Setenv("AGENTCLIP_CONFIG_DIR", t.TempDir())
	identity, publicKey, err := ensureAgentClipSSHKey("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(publicKey, "ssh-ed25519 ") {
		t.Fatalf("public key = %q", publicKey)
	}
	for _, path := range []string{identity, identity + ".pub"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("key permissions for %s = %o, want 600", path, info.Mode().Perm())
		}
	}
	if secondIdentity, secondPublicKey, err := ensureAgentClipSSHKey("dev"); err != nil || secondIdentity != identity || secondPublicKey != publicKey {
		t.Fatalf("key reuse = (%q, %q, %v), want existing key", secondIdentity, secondPublicKey, err)
	}
}

func TestReleaseTag(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		want      string
		wantError bool
	}{
		{name: "prefixes a stable local version", requested: "0.2.0", want: "v0.2.0"},
		{name: "accepts a tag", requested: "v1.2.3-rc.1", want: "v1.2.3-rc.1"},
		{name: "rejects development builds", requested: "0.2.0-dev", wantError: true},
		{name: "rejects an invalid version", requested: "latest", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := releaseTag(test.requested)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("releaseTag(%q) = %q, want %q", test.requested, got, test.want)
			}
		})
	}
}

func TestDefaultProfileName(t *testing.T) {
	tests := map[string]string{
		"bastion-m2":              "bastion-m2",
		"wendell@bastion.example": "bastion-example",
		"user@[2001:db8::1]":      "2001-db8-1",
	}
	for destination, want := range tests {
		if got := defaultProfileName(destination); got != want {
			t.Errorf("defaultProfileName(%q) = %q, want %q", destination, got, want)
		}
	}
}

func TestAgentAdaptersBuildUserScopedMCPCommands(t *testing.T) {
	profile := companion.Profile{Name: "m2", RemotePort: 39123, Token: "pair-token"}
	tests := []struct {
		id             string
		executable     string
		addContains    []string
		removeContains []string
	}{
		{
			id: "codex", executable: "codex",
			addContains:    []string{"codex", "mcp", "add", "agentclip-m2", "--", "agentclip", "mcp", "AGENTCLIP_BRIDGE_PORT=39123", "AGENTCLIP_SESSION_TOKEN=pair-token"},
			removeContains: []string{"codex", "mcp", "remove", "agentclip-m2"},
		},
		{
			id: "claude", executable: "claude",
			addContains:    []string{"claude", "mcp", "add", "agentclip-m2", "--scope", "user", "--", "agentclip", "mcp", "AGENTCLIP_BRIDGE_PORT=39123", "AGENTCLIP_SESSION_TOKEN=pair-token"},
			removeContains: []string{"claude", "mcp", "remove", "--scope", "user", "agentclip-m2"},
		},
		{
			id: "gemini", executable: "gemini",
			addContains:    []string{"gemini", "mcp", "add", "agentclip-m2", "agentclip", "mcp", "--scope", "user", "AGENTCLIP_BRIDGE_PORT=39123", "AGENTCLIP_SESSION_TOKEN=pair-token"},
			removeContains: []string{"gemini", "mcp", "remove", "--scope", "user", "agentclip-m2"},
		},
		{
			id: "agy", executable: "agy",
			addContains:    []string{"agentclip", "harness", "install", "agy", "--name", "agentclip-m2", "--port", "39123", "--token", "pair-token"},
			removeContains: []string{"agentclip", "harness", "remove", "agy", "--name", "agentclip-m2"},
		},
		{
			id: "opencode", executable: "opencode",
			addContains:    []string{"agentclip", "harness", "install", "opencode", "--name", "agentclip-m2", "--port", "39123", "--token", "pair-token"},
			removeContains: []string{"agentclip", "harness", "remove", "opencode", "--name", "agentclip-m2"},
		},
		{
			id: "pi", executable: "pi",
			addContains:    []string{"agentclip", "harness", "install", "pi", "--name", "agentclip-m2", "--port", "39123", "--token", "pair-token"},
			removeContains: []string{"agentclip", "harness", "remove", "pi", "--name", "agentclip-m2"},
		},
	}
	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			adapter, err := resolveAgentAdapter(test.id)
			if err != nil {
				t.Fatal(err)
			}
			if adapter.executable != test.executable {
				t.Fatalf("executable = %q, want %q", adapter.executable, test.executable)
			}
			assertArgumentsContain(t, adapter.addArguments(profile, "agentclip-m2"), test.addContains)
			assertArgumentsContain(t, adapter.removeArguments("agentclip-m2"), test.removeContains)
		})
	}
}

func TestResolveAgentAdapterRejectsUnknownAgent(t *testing.T) {
	if _, err := resolveAgentAdapter("aider"); err == nil {
		t.Fatal("expected unsupported agent error")
	}
}

func TestDisplayAgents(t *testing.T) {
	codex, err := resolveAgentAdapter("codex")
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := resolveAgentAdapter("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := displayAgents([]agentAdapter{codex, gemini}), "Codex, Gemini CLI"; got != want {
		t.Fatalf("displayAgents() = %q, want %q", got, want)
	}
}

func TestAgentAdaptersPropagateUploadToken(t *testing.T) {
	profile := companion.Profile{Name: "m2", Destination: "host", RemotePort: 39123, Token: "read-token", UploadToken: "upload-token"}
	arguments := agentEnvironmentArguments([]string{"codex", "mcp", "add"}, profile)
	if !strings.Contains(strings.Join(arguments, " "), "AGENTCLIP_UPLOAD_TOKEN=upload-token") {
		t.Fatalf("adapter arguments omit upload token: %#v", arguments)
	}
	harness := harnessInstallArguments("agy", profile, "agentclip-m2")
	if !strings.Contains(strings.Join(harness, " "), "--upload-token upload-token") {
		t.Fatalf("harness arguments omit upload token: %#v", harness)
	}
}

func TestHarnessConfigurationPreservesOtherEntries(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"other":{"command":"other"}},"keep":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installHarness(home, "agy", "agentclip-m2", 39123, "pair-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Keep       bool                       `json:"keep"`
		MCPServers map[string]harnessMCPEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if !config.Keep || config.MCPServers["other"].Command != "other" {
		t.Fatalf("unrelated configuration was changed: %s", data)
	}
	entry := config.MCPServers["agentclip-m2"]
	if entry.Command != "agentclip" || !reflect.DeepEqual(entry.Args, []string{"mcp"}) || entry.Env["AGENTCLIP_SESSION_TOKEN"] != "pair-token" || entry.Env["AGENTCLIP_UPLOAD_TOKEN"] != "upload-token" {
		t.Fatalf("AgentClip AGY entry = %#v", entry)
	}
	if err := removeHarness(home, "agy", "agentclip-m2"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "agentclip-m2") || !strings.Contains(string(data), "other") {
		t.Fatalf("AgentClip removal did not preserve other entry: %s", data)
	}
}

func TestOpenCodeAndPiHarnessConfiguration(t *testing.T) {
	home := t.TempDir()
	if err := installHarness(home, "opencode", "agentclip-m2", 39123, "pair-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	openCode, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"type": "local"`, `"command": [`, `"agentclip"`, `"mcp"`, `"environment"`, `"AGENTCLIP_BRIDGE_PORT": "39123"`, `"AGENTCLIP_UPLOAD_TOKEN": "upload-token"`} {
		if !strings.Contains(string(openCode), required) {
			t.Fatalf("OpenCode configuration missing %q: %s", required, openCode)
		}
	}
	if err := installHarness(home, "pi", "agentclip-m2", 39123, "pair-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	piPath := filepath.Join(home, ".pi", "agent", "extensions", "agentclip-m2.ts")
	piExtension, err := os.ReadFile(piPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"clipboard_status", "get_clipboard_image", "get_clipboard_text", "materialize_clipboard_files", "offer_file_to_host", "host_file_offer_status", "deliver_file_to_host", "waitForHostApproval", "127.0.0.1:39123", "pair-token", "upload-token"} {
		if !strings.Contains(string(piExtension), required) {
			t.Fatalf("Pi extension missing %q", required)
		}
	}
	if err := removeHarness(home, "pi", "agentclip-m2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(piPath); !os.IsNotExist(err) {
		t.Fatalf("Pi extension still exists after removal: %v", err)
	}
}

func assertArgumentsContain(t *testing.T, arguments, required []string) {
	t.Helper()
	for _, value := range required {
		found := false
		for _, argument := range arguments {
			if argument == value {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("arguments %q do not contain %q", arguments, value)
		}
	}
}
