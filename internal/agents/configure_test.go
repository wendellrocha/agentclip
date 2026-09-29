package agents

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
)

// server stands in for the remote machine. A fake `ssh` runs the remote command
// locally with HOME pointing at a directory of its own, whose ~/.local/bin holds
// the fake agentclip and harness CLIs. Each harness logs its arguments, so the
// tests can see exactly what setup registered without a network or a real CLI.
type server struct {
	t    *testing.T
	home string
	log  string
}

func newServer(t *testing.T) *server {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake server is built from POSIX shell scripts")
	}
	bin := t.TempDir()
	sshScript := "#!/bin/sh\nfor last; do :; done\nexec sh -c \"$last\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(sshScript), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, home: t.TempDir()}
	s.log = filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("HOME", s.home)
	// Only the system directories, so an agentclip on the developer's machine is
	// never mistaken for the one on the "server".
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return s
}

func (s *server) install(name, script string) {
	s.t.Helper()
	dir := filepath.Join(s.home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		s.t.Fatal(err)
	}
}

// agentclip installs a fake agentclip that reports version.
func (s *server) agentclip(version string) { s.install("agentclip", "echo '"+version+"'") }

// harness installs a fake harness CLI that appends its arguments to the call log.
func (s *server) harness(name string) {
	s.install(name, "echo \""+name+" $*\" >> '"+s.log+"'")
}

func (s *server) calls() []string {
	data, _ := os.ReadFile(s.log)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func profileFor() companion.Profile {
	return companion.Profile{Name: "m2", Destination: "bastion-m2", RemotePort: 39123, Token: "session-token", UploadToken: "upload-token"}
}

func TestConfigureRegistersAgentclipWithTheChosenHarness(t *testing.T) {
	s := newServer(t)
	s.agentclip("v0.7.3")
	s.harness("codex")

	adapters, err := Configure(profileFor(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 1 || adapters[0].ID != "codex" {
		t.Fatalf("adapters = %+v", adapters)
	}
	calls := s.calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want a removal then an addition", calls)
	}
	if calls[0] != "codex mcp remove agentclip-m2" {
		t.Errorf("first call = %q, want the removal of AgentClip's own entry", calls[0])
	}
	for _, want := range []string{"codex mcp add agentclip-m2", "AGENTCLIP_BRIDGE_PORT=39123", "AGENTCLIP_SESSION_TOKEN=session-token", "AGENTCLIP_UPLOAD_TOKEN=upload-token", "-- agentclip mcp"} {
		if !strings.Contains(calls[1], want) {
			t.Errorf("addition %q lacks %q", calls[1], want)
		}
	}
}

func TestConfigureAllUsesEveryHarnessTheServerHas(t *testing.T) {
	s := newServer(t)
	s.agentclip("v0.7.3")
	s.harness("codex")
	s.harness("claude")

	adapters, err := Configure(profileFor(), "all")
	if err != nil {
		t.Fatal(err)
	}
	if got := Display(adapters); got != "Codex, Claude Code" {
		t.Fatalf("configured %q, want Codex and Claude Code", got)
	}
	joined := strings.Join(s.calls(), "\n")
	for _, want := range []string{"codex mcp add agentclip-m2", "claude mcp add agentclip-m2 --scope user"} {
		if !strings.Contains(joined, want) {
			t.Errorf("calls lack %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "gemini") {
		t.Errorf("a harness the server does not have was configured:\n%s", joined)
	}
}

func TestConfigureAllExplainsAServerWithoutAnyHarness(t *testing.T) {
	s := newServer(t)
	s.agentclip("v0.7.3")
	if _, err := Configure(profileFor(), "all"); err == nil || !strings.Contains(err.Error(), "no supported harness is installed") {
		t.Fatalf("err = %v, want it to say no harness is installed", err)
	}
}

// A server on an old agentclip is refused before anything is registered, and
// the message says how to fix it.
func TestConfigureRefusesAServerWhoseAgentclipIsTooOld(t *testing.T) {
	for _, selection := range []string{"codex", "all"} {
		t.Run(selection, func(t *testing.T) {
			s := newServer(t)
			s.agentclip("v0.4.0")
			s.harness("codex")
			_, err := Configure(profileFor(), selection)
			if err == nil || !strings.Contains(err.Error(), "agentclip upgrade") {
				t.Fatalf("err = %v, want a refusal that says to upgrade", err)
			}
			if data, _ := os.ReadFile(s.log); len(data) != 0 {
				t.Fatalf("something was registered on an outdated server:\n%s", data)
			}
		})
	}
}

func TestConfigureExplainsAMissingAgentclipOrHarness(t *testing.T) {
	s := newServer(t)
	s.harness("codex") // no agentclip on the server
	if _, err := Configure(profileFor(), "codex"); err == nil || !strings.Contains(err.Error(), "--skip-agent") {
		t.Fatalf("err = %v, want the preflight advice", err)
	}
	if _, err := Configure(profileFor(), "all"); err == nil || !strings.Contains(err.Error(), "--skip-agent") {
		t.Fatalf("err (all) = %v, want the preflight advice", err)
	}
}

func TestConfigureRefusesBadInput(t *testing.T) {
	if _, err := Configure(companion.Profile{Name: "x", Destination: "-oProxyCommand=evil"}, "codex"); err == nil || !strings.Contains(err.Error(), "dash") {
		t.Fatalf("a destination that starts with a dash = %v", err)
	}
	if _, err := Configure(profileFor(), "emacs"); err == nil || !strings.Contains(err.Error(), "unsupported agent") {
		t.Fatalf("an unknown agent = %v", err)
	}
}

func TestConfigureReportsAFailedRegistration(t *testing.T) {
	s := newServer(t)
	s.agentclip("v0.7.3")
	s.install("codex", `case "$2" in add) exit 3;; esac`)
	if _, err := Configure(profileFor(), "codex"); err == nil || !strings.Contains(err.Error(), "configure Codex MCP on bastion-m2") {
		t.Fatalf("err = %v, want the failed registration named", err)
	}
}
