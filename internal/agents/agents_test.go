package agents

import (
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
)

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
			adapter, err := Resolve(test.id)
			if err != nil {
				t.Fatal(err)
			}
			if adapter.Executable != test.executable {
				t.Fatalf("executable = %q, want %q", adapter.Executable, test.executable)
			}
			assertArgumentsContain(t, adapter.AddArguments(profile, "agentclip-m2"), test.addContains)
			assertArgumentsContain(t, adapter.RemoveArguments("agentclip-m2"), test.removeContains)
		})
	}
}

func TestResolveAgentAdapterRejectsUnknownAgent(t *testing.T) {
	if _, err := Resolve("aider"); err == nil {
		t.Fatal("expected unsupported agent error")
	}
}

func TestDisplayAgents(t *testing.T) {
	codex, err := Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	gemini, err := Resolve("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Display([]Adapter{codex, gemini}), "Codex, Gemini CLI"; got != want {
		t.Fatalf("Display() = %q, want %q", got, want)
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
