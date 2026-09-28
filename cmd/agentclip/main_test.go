package main

import (
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

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
