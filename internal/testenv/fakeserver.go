package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// FakeServer stands in for a remote machine reached over SSH, without a network.
// It puts an `ssh` on PATH that runs the remote command locally, with HOME
// pointing at a directory of its own; whatever a test installs into
// ~/.local/bin is what "the server" has.
//
// The command lookup is private. PATH holds only a directory made here, with the
// few system utilities the scripts need and a `sh` that runs `sh -lc` as `sh -c`:
// a login shell rebuilds PATH from /etc/profile on macOS and Debian, which would
// bring back an agentclip or a harness installed on the machine running the tests.
type FakeServer struct {
	t *testing.T
	// Home is the "server's" home directory.
	Home string
}

// utilities the remote scripts of AgentClip call by name.
var serverUtilities = []string{"uname", "cat", "chmod", "mv", "mkdir", "rm", "cut", "grep", "dirname"}

// NewFakeServer skips the test on Windows, where the scripts cannot run.
func NewFakeServer(t *testing.T) *FakeServer {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake server runs POSIX shell scripts")
	}
	bin := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("ssh", "#!/bin/sh\nfor last; do :; done\nexec /bin/sh -c \"$last\"\n")
	write("sh", "#!/bin/sh\nif [ \"$1\" = -lc ]; then shift; exec /bin/sh -c \"$@\"; fi\nexec /bin/sh \"$@\"\n")
	for _, name := range append(serverUtilities, "sha256sum", "shasum") {
		if path, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	t.Setenv("PATH", bin)
	t.Setenv("HOME", home)
	return &FakeServer{t: t, Home: home}
}

// Install puts an executable script into the server's ~/.local/bin.
func (s *FakeServer) Install(name, script string) {
	s.t.Helper()
	dir := filepath.Join(s.Home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		s.t.Fatal(err)
	}
}

// Path is where the server's agentclip lives.
func (s *FakeServer) Path(name string) string {
	return filepath.Join(s.Home, ".local", "bin", name)
}
