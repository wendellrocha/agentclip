package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The point of the fake server is that it sees only what a test installs. An
// agentclip on the machine running the tests, on PATH or not, must stay invisible.
func TestFakeServerSeesOnlyWhatIsInstalled(t *testing.T) {
	decoys := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoys, "agentclip"), []byte("#!/bin/sh\necho decoy\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", decoys+string(os.PathListSeparator)+os.Getenv("PATH"))

	server := NewFakeServer(t)
	locate := func() (string, error) {
		out, err := exec.Command("ssh", "host", `sh -lc 'export PATH="$HOME/.local/bin:$PATH"; command -v agentclip'`).Output()
		return strings.TrimSpace(string(out)), err
	}
	if path, err := locate(); err == nil {
		t.Fatalf("the server found an agentclip it was never given: %s", path)
	}
	server.Install("agentclip", "echo real")
	path, err := locate()
	if err != nil || path != server.Path("agentclip") {
		t.Fatalf("located %q (%v), want %q", path, err, server.Path("agentclip"))
	}
	if home, _ := os.UserHomeDir(); home != server.Home {
		t.Fatalf("HOME = %q, want the server's %q", home, server.Home)
	}
}
