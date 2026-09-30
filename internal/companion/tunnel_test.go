package companion

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/testenv"
)

// fastTunnel makes the reconnection quick enough to watch in a test.
func fastTunnel(t *testing.T, min, max, stable time.Duration) {
	t.Helper()
	previous := tunnelTiming
	t.Cleanup(func() { tunnelTiming = previous })
	tunnelTiming.min, tunnelTiming.max, tunnelTiming.stable = min, max, stable
}

// fakeSSH puts a fake `ssh` on PATH: the test binary itself, copied under that
// name (see TestMain). It exits with 255 for its first `failures` starts and, when
// dropAt is set, drops at that start after dropAfter; every start is counted and
// timestamped in the returned directory.
type fakeTunnel struct{ dir string }

func fakeSSH(t *testing.T, failures, dropAt int, dropAfter time.Duration) fakeTunnel {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "ssh"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin, dir := t.TempDir(), t.TempDir()
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, name), data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(fakeSSHEnv, "1")
	t.Setenv("AGENTCLIP_TEST_FAKE_SSH_DIR", dir)
	t.Setenv("AGENTCLIP_TEST_FAKE_SSH_FAILURES", strconv.Itoa(failures))
	t.Setenv("AGENTCLIP_TEST_FAKE_SSH_DROP_AT", strconv.Itoa(dropAt))
	t.Setenv("AGENTCLIP_TEST_FAKE_SSH_DROP_AFTER", dropAfter.String())
	return fakeTunnel{dir: dir}
}

func (f fakeTunnel) starts() int {
	data, _ := os.ReadFile(filepath.Join(f.dir, "starts"))
	n, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return n
}

// stamps returns the start times, in order.
func (f fakeTunnel) stamps() []time.Time {
	data, _ := os.ReadFile(filepath.Join(f.dir, "stamps"))
	var times []time.Time
	for _, field := range strings.Fields(string(data)) {
		if nanos, err := strconv.ParseInt(field, 10, 64); err == nil {
			times = append(times, time.Unix(0, nanos))
		}
	}
	return times
}

type statusLog struct {
	mu       sync.Mutex
	statuses []TunnelStatus
}

func (l *statusLog) observe(status TunnelStatus) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statuses = append(l.statuses, status)
}

func (l *statusLog) snapshot() []TunnelStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]TunnelStatus(nil), l.statuses...)
}

func profileForTunnel() Profile {
	return Profile{Name: "t", Destination: "host", RemotePort: 39123, Token: "token", UploadToken: "upload"}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A tunnel that keeps dropping is retried, the state is reported honestly at
// each step, and cancelling ends the loop and the child process.
func TestTunnelReconnectsAfterExitsReportsEachStateAndStopsOnCancel(t *testing.T) {
	fastTunnel(t, 5*time.Millisecond, 20*time.Millisecond, time.Hour)
	ssh := fakeSSH(t, 2, 0, 0)
	starts := ssh.starts
	log := &statusLog{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunTunnelWithStatus(ctx, profileForTunnel(), 45678, log.observe) }()

	waitFor(t, "the third start, which stays up", func() bool { return starts() >= 3 })
	waitFor(t, "a connected report after the failures", func() bool {
		statuses := log.snapshot()
		return len(statuses) >= 5 && statuses[len(statuses)-1].Connected
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelling returned %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop when cancelled")
	}

	statuses := log.snapshot()
	var failures int
	for _, status := range statuses {
		if !status.Connected && strings.Contains(status.LastError, "exit status 255") {
			failures++
		}
	}
	if failures != 2 {
		t.Errorf("reported %d failed exits with their error, want 2: %+v", failures, statuses)
	}
	if last := statuses[len(statuses)-1]; last.Connected || last.LastError != "" {
		t.Errorf("after cancelling the last report is %+v, want a clean disconnect", last)
	}
	if starts() != 3 {
		t.Errorf("ssh was started %d times, want exactly 3", starts())
	}
}

// The wait grows while the tunnel keeps failing quickly, is capped, and starts
// over once a tunnel has stayed up: a healthy tunnel that drops after hours
// must not inherit the wait earned by failures long before.
//
// The waits are read from the log, where the loop writes each one it chooses, so
// the test does not depend on how long a process takes to start: that varies
// from a few milliseconds on Linux to half a second on a busy Windows runner.
func TestTunnelWaitGrowsToItsCapAndStartsOverAfterAStableTunnel(t *testing.T) {
	testenv.IsolateUserDirs(t)
	// A start that fails at once takes far less than "stable"; the sixth one
	// stays up for longer than it, then drops.
	fastTunnel(t, 50*time.Millisecond, 400*time.Millisecond, 2*time.Second)
	ssh := fakeSSH(t, 5, 6, 2500*time.Millisecond)
	logger, err := NewLogger("waits")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = RunTunnelWithLogger(ctx, profileForTunnel(), 45678, nil, logger)
		close(done)
	}()
	// Registered after fastTunnel, so it runs first: the loop and its child are
	// gone before the timing is restored and the fake ssh is removed.
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, "the seventh start", func() bool { return len(ssh.stamps()) >= 7 })

	data, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	var waits []string
	for _, match := range regexp.MustCompile(`SSH tunnel reconnecting in (\S+)`).FindAllStringSubmatch(string(data), -1) {
		waits = append(waits, match[1])
	}
	// After five quick exits: 50ms, then doubling, then held at the 400ms cap.
	// After the tunnel that stayed up, it starts over at 50ms instead of 400ms.
	want := []string{"50ms", "100ms", "200ms", "400ms", "400ms", "50ms"}
	if strings.Join(waits, " ") != strings.Join(want, " ") {
		t.Errorf("waits = %v, want %v", waits, want)
	}
}
