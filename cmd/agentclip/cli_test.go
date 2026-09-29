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

func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binaryPath, args...)
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

func TestCLIVersionAndUsage(t *testing.T) {
	if result := runCLI(t, "version"); result.exitCode != 0 || strings.TrimSpace(result.stdout) == "" {
		t.Fatalf("version = %+v, want the version and exit 0", result)
	}
	if result := runCLI(t, "--version"); result.exitCode != 0 || strings.TrimSpace(result.stdout) == "" {
		t.Fatalf("--version = %+v", result)
	}
	// No command is a request for help; an unknown one is a mistake.
	if result := runCLI(t); result.exitCode != 0 || !strings.Contains(result.stderr, "usage: agentclip") {
		t.Fatalf("no arguments = %+v, want usage and exit 0", result)
	}
	if result := runCLI(t, "bogus"); result.exitCode != 2 || !strings.Contains(result.stderr, "usage: agentclip") {
		t.Fatalf("unknown command = %+v, want usage and exit 2", result)
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
	if cache, err := os.UserCacheDir(); err == nil {
		if _, err := os.Stat(filepath.Join(cache, "agentclip", "bridge.json")); err == nil {
			t.Error("a failed command left a bridge state file behind")
		}
	}
}
