package main

import (
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
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
	for _, required := range []string{"command -v agentclip", "codex", "claude", "gemini", "agentclip_harness"} {
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
	if _, err := resolveAgentAdapter("opencode"); err == nil {
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
