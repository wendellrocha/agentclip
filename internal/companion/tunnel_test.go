package companion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fastTunnel makes the reconnection quick enough to watch in a test.
func fastTunnel(t *testing.T, min, max, stable time.Duration) {
	t.Helper()
	previous := tunnelTiming
	t.Cleanup(func() { tunnelTiming = previous })
	tunnelTiming.min, tunnelTiming.max, tunnelTiming.stable = min, max, stable
}

// fakeSSH installs an `ssh` that exits with 255 for its first `failures` runs,
// then stays up until it is killed, and counts how often it was started.
func fakeSSH(t *testing.T, failures int) (starts func() int) {
	t.Helper()
	if os.PathSeparator == '\\' {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	bin, counter := t.TempDir(), filepath.Join(t.TempDir(), "starts")
	script := "#!/bin/sh\nn=$(cat '" + counter + "' 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > '" + counter + "'\n" +
		"if [ $n -le " + itoa(failures) + " ]; then exit 255; fi\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return func() int {
		data, _ := os.ReadFile(counter)
		n := 0
		for _, c := range strings.TrimSpace(string(data)) {
			n = n*10 + int(c-'0')
		}
		return n
	}
}

func itoa(n int) string { return string(rune('0'+n/10)) + string(rune('0'+n%10)) }

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
	starts := fakeSSH(t, 2)
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
func TestTunnelWaitGrowsToItsCapAndStartsOverAfterAStableTunnel(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	fastTunnel(t, 50*time.Millisecond, 400*time.Millisecond, 300*time.Millisecond)
	bin := t.TempDir()
	stamps := filepath.Join(t.TempDir(), "stamps")
	// Fails 5 times fast, stays up past the stable threshold, drops once, then stays.
	script := "#!/bin/sh\nn=$(cat '" + stamps + ".n' 2>/dev/null || echo 0)\nn=$((n+1))\necho $n > '" + stamps + ".n'\n" +
		"/bin/date +%s%N >> '" + stamps + "'\n" +
		"if [ $n -le 5 ]; then exit 255; fi\nif [ $n -eq 6 ]; then /bin/sleep 0.8; exit 255; fi\nexec /bin/sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = RunTunnel(ctx, profileForTunnel(), 45678) }()
	waitFor(t, "seven starts", func() bool {
		data, _ := os.ReadFile(stamps)
		return len(strings.Fields(string(data))) >= 7
	})
	data, _ := os.ReadFile(stamps)
	var times []int64
	for _, field := range strings.Fields(string(data)) {
		var n int64
		for _, c := range field {
			n = n*10 + int64(c-'0')
		}
		times = append(times, n)
	}
	gap := func(i int) time.Duration { return time.Duration(times[i+1] - times[i]) }
	// Waits after quick exits: 50, 100, 200, 400 (the cap). The gaps also hold
	// the script's own start-up, so only lower bounds and the cap are checked.
	if gap(1) < 90*time.Millisecond || gap(2) < 190*time.Millisecond || gap(3) < 390*time.Millisecond {
		t.Errorf("the wait did not grow: gaps %v %v %v %v", gap(0), gap(1), gap(2), gap(3))
	}
	// The fifth wait would be 800ms if it kept doubling; the cap holds it at 400ms.
	if gap(4) < 390*time.Millisecond || gap(4) > 700*time.Millisecond {
		t.Errorf("the wait after the cap was reached is %v, want it held near 400ms", gap(4))
	}
	// The sixth run stayed up 0.8s, past the 300ms threshold, so the next wait
	// starts over at 50ms instead of the 400ms cap it had reached.
	if wait := gap(5) - 800*time.Millisecond; wait > 250*time.Millisecond {
		t.Errorf("after a stable tunnel the wait was %v, want it to start over near 50ms, not stay at the cap", wait)
	}
}
