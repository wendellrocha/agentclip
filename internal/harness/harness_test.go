package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHarnessConfigurationPreservesOtherEntries(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"other":{"command":"other"}},"keep":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(home, "agy", "agentclip-m2", 39123, "pair-token", "upload-token"); err != nil {
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
	if err := Remove(home, "agy", "agentclip-m2"); err != nil {
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
	if err := Install(home, "opencode", "agentclip-m2", 39123, "pair-token", "upload-token"); err != nil {
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
	if err := Install(home, "pi", "agentclip-m2", 39123, "pair-token", "upload-token"); err != nil {
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
	if err := Remove(home, "pi", "agentclip-m2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(piPath); !os.IsNotExist(err) {
		t.Fatalf("Pi extension still exists after removal: %v", err)
	}
}

func TestInstallRejectsUnsupportedHarnessAndEmptyName(t *testing.T) {
	home := t.TempDir()
	if err := Install(home, "unknown", "agentclip", 39123, "token"); err == nil {
		t.Fatal("unsupported harness must be rejected")
	}
	if err := Install(home, "agy", "  ", 39123, "token"); err == nil {
		t.Fatal("empty entry name must be rejected")
	}
	if err := Remove(home, "unknown", "agentclip"); err == nil {
		t.Fatal("removing from an unsupported harness must be rejected")
	}
}

func TestInstallDoesNotOverwriteMalformedConfiguration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const original = "{not json"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Install(home, "agy", "agentclip", 39123, "token"); err == nil {
		t.Fatal("malformed configuration must not be accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("configuration was modified: %q, %v", data, err)
	}
}

func TestPiExtensionEmbedsEscapedCredentials(t *testing.T) {
	source := piExtensionSource(4242, `to"ken`, "up")
	for _, want := range []string{`"http://127.0.0.1:4242"`, `const token = "to\"ken";`, `const uploadToken = "up";`} {
		if !strings.Contains(source, want) {
			t.Fatalf("extension missing %s", want)
		}
	}
	if strings.Contains(source, "__PORT__") || strings.Contains(source, "__TOKEN__") || strings.Contains(source, "__UPLOAD_TOKEN__") {
		t.Fatal("template placeholders were not replaced")
	}
}
