package companion

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/testenv"
)

// TestMain keeps these tests away from the developer's real cache and config. It
// also lets the test binary stand in for `ssh`: the tunnel tests copy it into a
// directory on PATH as `ssh`, so the fake works the same on every platform and
// timestamps come from Go instead of a system `date`.
func TestMain(m *testing.M) {
	if os.Getenv(fakeSSHEnv) == "1" {
		os.Exit(runFakeSSH())
	}
	os.Exit(testenv.Main(m))
}

const fakeSSHEnv = "AGENTCLIP_TEST_FAKE_SSH"

// runFakeSSH behaves like an ssh tunnel that the test scripts through its
// environment: it records its start, exits with status 255 for its first
// FAILURES starts, at start number DROP_AT stays up for DROP_AFTER and then drops
// like a lost connection, and otherwise stays up until it is killed.
func runFakeSSH() int {
	dir := os.Getenv("AGENTCLIP_TEST_FAKE_SSH_DIR")
	number := 1
	if data, err := os.ReadFile(filepath.Join(dir, "starts")); err == nil {
		number, _ = strconv.Atoi(string(data))
		number++
	}
	_ = os.WriteFile(filepath.Join(dir, "starts"), []byte(strconv.Itoa(number)), 0o600)
	if file, err := os.OpenFile(filepath.Join(dir, "stamps"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(file, time.Now().UnixNano())
		file.Close()
	}
	failures, _ := strconv.Atoi(os.Getenv("AGENTCLIP_TEST_FAKE_SSH_FAILURES"))
	dropAt, _ := strconv.Atoi(os.Getenv("AGENTCLIP_TEST_FAKE_SSH_DROP_AT"))
	dropAfter, _ := time.ParseDuration(os.Getenv("AGENTCLIP_TEST_FAKE_SSH_DROP_AFTER"))
	switch {
	case number <= failures:
		return 255
	case dropAt > 0 && number == dropAt:
		time.Sleep(dropAfter)
		return 255
	}
	time.Sleep(time.Minute)
	return 0
}
