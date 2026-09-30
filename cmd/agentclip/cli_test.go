package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/testenv"
)

// binaryPath is the agentclip executable built once for these tests. They run
// the real binary, so the dispatch in main and the exit codes users and scripts
// depend on are covered, not only the helpers behind them.
var binaryPath string

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "agentclip-cli-")
	if err != nil {
		panic(err)
	}
	binaryPath = filepath.Join(directory, "agentclip")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	// Built before the per-user directories are redirected, so the Go build
	// cache stays where it is.
	if output, err := exec.Command("go", "build", "-o", binaryPath, ".").CombinedOutput(); err != nil {
		os.RemoveAll(directory)
		panic("build agentclip: " + err.Error() + "\n" + string(output))
	}
	// os.Exit skips deferred calls, so clean up before it.
	code := testenv.Main(m)
	os.RemoveAll(directory)
	os.Exit(code)
}

type cliResult struct {
	stdout, stderr string
	exitCode       int
}

func runCLI(t *testing.T, args ...string) cliResult { return runCLIEnv(t, nil, args...) }

// runCLIEnv runs the binary with extra environment variables, such as the language.
func runCLIEnv(t *testing.T, env []string, args ...string) cliResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, args...)
	command.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := cliResult{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		result.exitCode = exit.ExitCode()
	default:
		t.Fatalf("agentclip %v: %v", args, err)
	}
	return result
}

func TestCLIVersionAndHelp(t *testing.T) {
	for _, flag := range []string{"version", "--version", "-v"} {
		if result := runCLI(t, flag); result.exitCode != 0 || strings.TrimSpace(result.stdout) == "" {
			t.Fatalf("%s = %+v, want the version and exit 0", flag, result)
		}
	}
	// No command, or a request for help, lists every command on stdout.
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		result := runCLI(t, args...)
		if result.exitCode != 0 || result.stderr != "" {
			t.Fatalf("agentclip %v = exit %d, stderr %q, want a quiet exit 0", args, result.exitCode, result.stderr)
		}
		for _, want := range []string{"Usage:", "agentclip help [command]", "setup", "upgrade", "companion", "Set up a server:", "AGENTCLIP_LANG"} {
			if !strings.Contains(result.stdout, want) {
				t.Errorf("agentclip %v does not mention %q:\n%s", args, want, result.stdout)
			}
		}
	}
	// An unknown command is a mistake: it says so, points at help and exits 2.
	if result := runCLI(t, "bogus"); result.exitCode != 2 || !strings.Contains(result.stderr, `unknown command "bogus"`) || !strings.Contains(result.stderr, "agentclip help") {
		t.Fatalf("unknown command = %+v", result)
	}
	if result := runCLI(t, "help", "bogus"); result.exitCode != 2 || !strings.Contains(result.stderr, `unknown command "bogus"`) {
		t.Fatalf("help for an unknown command = %+v", result)
	}
}

// The help of one command gives its syntax and explains it, however it is asked for.
func TestCLICommandHelp(t *testing.T) {
	for _, args := range [][]string{{"help", "setup"}, {"setup", "--help"}, {"setup", "-h"}, {"setup", "help"}} {
		result := runCLI(t, args...)
		if result.exitCode != 0 || result.stderr != "" {
			t.Fatalf("agentclip %v = exit %d, stderr %q", args, result.exitCode, result.stderr)
		}
		for _, want := range []string{"Usage:", "agentclip setup <ssh-destination>", "--profile NAME", "--no-start", "Install and configure a server in one step"} {
			if !strings.Contains(result.stdout, want) {
				t.Errorf("agentclip %v does not mention %q:\n%s", args, want, result.stdout)
			}
		}
	}
	// An alias reaches the same help, and says it is an alias.
	if result := runCLI(t, "help", "disconnect"); result.exitCode != 0 || !strings.Contains(result.stdout, "agentclip uninstall <profile>") || !strings.Contains(result.stdout, "Also known as: disconnect") {
		t.Fatalf("help for an alias = %+v", result)
	}
	if result := runCLI(t, "companion", "--help"); result.exitCode != 0 || !strings.Contains(result.stdout, "autostart") || !strings.Contains(result.stdout, "agentclip companion <accept|reject>") {
		t.Fatalf("companion help = %+v", result)
	}
	// help is a command of its own, and is listed like the others.
	if result := runCLI(t, "help", "help"); result.exitCode != 0 || !strings.Contains(result.stdout, "agentclip help [command]") || !strings.Contains(result.stdout, "agentclip <command> --help") {
		t.Fatalf("help for help = %+v", result)
	}
	if result := runCLI(t, "help", "--help"); result.exitCode != 0 || !strings.Contains(result.stdout, "agentclip help [command]") {
		t.Fatalf("help --help = %+v", result)
	}
	if result := runCLI(t); !strings.Contains(result.stdout, "\n  help ") {
		t.Fatalf("the overview does not list help:\n%s", result.stdout)
	}
	// Asking for help never runs the command: nothing is created.
	if result := runCLI(t, "upgrade", "--help"); result.exitCode != 0 || !strings.Contains(result.stdout, "attestation") {
		t.Fatalf("upgrade help = %+v", result)
	}
}

// The language is chosen by AGENTCLIP_LANG alone; anything unknown is English.
func TestCLISpeaksPortugueseOnlyWhenAsked(t *testing.T) {
	pt := []string{"AGENTCLIP_LANG=pt-BR"}
	help := runCLIEnv(t, pt)
	for _, want := range []string{"Uso:", "Configurar um servidor:", "Atualiza esta máquina", "Defina AGENTCLIP_LANG=en-US"} {
		if !strings.Contains(help.stdout, want) {
			t.Errorf("Portuguese help lacks %q:\n%s", want, help.stdout)
		}
	}
	if strings.Contains(help.stdout, "Usage:") {
		t.Error("Portuguese help still has English headings")
	}
	if result := runCLIEnv(t, pt, "help", "setup"); !strings.Contains(result.stdout, "A forma recomendada de configurar um servidor") || !strings.Contains(result.stdout, "--no-start") {
		t.Fatalf("Portuguese command help = %+v", result)
	}
	if result := runCLIEnv(t, pt, "companion", "autostart", "status", "nope"); !strings.Contains(result.stdout, "não sobe quando você entra na sessão") {
		t.Fatalf("Portuguese command output = %+v", result)
	}
	if result := runCLIEnv(t, pt, "bogus"); result.exitCode != 2 || !strings.Contains(result.stderr, `comando desconhecido "bogus"`) {
		t.Fatalf("Portuguese error = %+v", result)
	}
	// The syntax is not translated, and an unsupported or empty value is English.
	for _, value := range []string{"AGENTCLIP_LANG=fr", "AGENTCLIP_LANG=", "AGENTCLIP_LANG=nonsense"} {
		if result := runCLIEnv(t, []string{value}); !strings.Contains(result.stdout, "Usage:") {
			t.Errorf("%s did not give English help:\n%s", value, result.stdout)
		}
	}
}

// Each command names what it needs when it is called without it, and fails with
// a non-zero status instead of doing something with an empty value.
func TestCLICommandsRejectMissingArguments(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"companion"}, "usage: agentclip companion"},
		{[]string{"companion", "status"}, "usage: agentclip companion"},
		{[]string{"companion", "bogus", "p"}, "usage: agentclip companion"},
		{[]string{"logs"}, "usage: agentclip logs"},
		{[]string{"pair"}, "usage: agentclip pair"},
		{[]string{"pair", "p"}, "usage: agentclip pair"},
		{[]string{"setup"}, "usage: agentclip setup"},
		{[]string{"connect"}, "usage: agentclip connect"},
		{[]string{"uninstall"}, "usage: agentclip uninstall"},
		{[]string{"uninstall", "p"}, "usage: agentclip uninstall"},
		{[]string{"harness"}, "usage: agentclip harness"},
		{[]string{"harness", "install", "pi"}, "usage: agentclip harness"},
		{[]string{"ssh"}, "usage: agentclip ssh"},
		{[]string{"upgrade", "--bogus"}, "usage: agentclip upgrade"},
		{[]string{"companion", "autostart"}, "usage: agentclip companion autostart"},
		{[]string{"companion", "autostart", "enable"}, "usage: agentclip companion autostart"},
		{[]string{"companion", "autostart", "bogus", "p"}, "usage: agentclip companion autostart"},
	} {
		t.Run(strings.Join(test.args, " "), func(t *testing.T) {
			result := runCLI(t, test.args...)
			if result.exitCode != 1 || !strings.Contains(result.stderr, test.want) {
				t.Fatalf("%v = exit %d, stderr %q; want exit 1 and %q", test.args, result.exitCode, result.stderr, test.want)
			}
		})
	}
}

// With no profile saved, every command that needs one says so and fails,
// without starting anything or writing the profile.
func TestCLICommandsOnAnUnknownProfileFailWithoutSideEffects(t *testing.T) {
	notRunning := [][]string{
		{"companion", "status", "nope"},
		{"companion", "stop", "nope"},
		{"companion", "open", "nope"},
		{"companion", "inbox", "nope"},
		{"companion", "accept", "nope", "offer"},
	}
	for _, args := range notRunning {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			result := runCLI(t, args...)
			if result.exitCode != 1 || !strings.Contains(result.stderr, `Companion "nope" is not running`) {
				t.Fatalf("%v = exit %d, stderr %q", args, result.exitCode, result.stderr)
			}
		})
	}
	noProfile := [][]string{
		{"companion", "start", "nope"},
		{"connect", "nope"},
		{"uninstall", "nope", "--agent", "codex"},
	}
	for _, args := range noProfile {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			result := runCLI(t, args...)
			if result.exitCode != 1 || !strings.Contains(result.stderr, `read companion profile "nope"`) {
				t.Fatalf("%v = exit %d, stderr %q", args, result.exitCode, result.stderr)
			}
		})
	}
	// Asking for the logs of a profile that never ran says so, and creates nothing.
	if result := runCLI(t, "logs", "nope"); result.exitCode != 1 || !strings.Contains(result.stderr, "no log for this profile") {
		t.Fatalf("logs of an unknown profile = exit %d, stderr %q", result.exitCode, result.stderr)
	}
	if cache, err := os.UserCacheDir(); err == nil {
		if _, err := os.Stat(filepath.Join(cache, "agentclip", "bridge.json")); err == nil {
			t.Error("a failed command left a bridge state file behind")
		}
		if _, err := os.Stat(filepath.Join(cache, "agentclip", "logs", "nope.log")); err == nil {
			t.Error("asking for the logs of an unknown profile created a log file")
		}
	}
}

// Autostart never touches the system for a profile that does not exist, and
// reporting on it is harmless.
func TestCLIAutostartNeedsAProfileAndReportsWithoutSideEffects(t *testing.T) {
	if result := runCLI(t, "companion", "autostart", "enable", "nope"); result.exitCode != 1 || !strings.Contains(result.stderr, `read companion profile "nope"`) {
		t.Fatalf("enable for a missing profile = exit %d, stderr %q", result.exitCode, result.stderr)
	}
	if result := runCLI(t, "companion", "autostart", "status", "nope"); result.exitCode != 0 || !strings.Contains(result.stdout, "does not start when you log in") {
		t.Fatalf("status = exit %d, stdout %q, stderr %q", result.exitCode, result.stdout, result.stderr)
	}
	if result := runCLI(t, "companion", "autostart", "status", "../escape"); result.exitCode != 1 || !strings.Contains(result.stderr, "invalid Companion profile") {
		t.Fatalf("status for a bad name = exit %d, stderr %q", result.exitCode, result.stderr)
	}
}
