package main

import (
	"strings"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/testenv"
)

// A service started at login must not open a second Companion for a profile
// that is already running: they would fight over the state file and the tunnel.
func TestServiceDoesNotStartASecondCompanionForARunningProfile(t *testing.T) {
	testenv.IsolateUserDirs(t)
	profile := companion.Profile{Name: "dup", Destination: "host", RemotePort: 39123, Token: "t", UploadToken: "u"}
	if err := companion.SaveProfile(profile); err != nil {
		t.Fatal(err)
	}
	running, err := companion.StartControl("dup", func() any { return map[string]any{} }, func() {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()

	// The login service (serve) leaves quietly, so the service manager does not
	// treat it as a failure and restart it in a loop.
	if err := runCompanionService("dup", false); err != nil {
		t.Fatalf("serve over a running Companion = %v, want a quiet no-op", err)
	}
	// Run by hand, it says why nothing happened.
	if err := runCompanionService("dup", true); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("run over a running Companion = %v, want the reason", err)
	}
}

// Starting a Companion is serialised per profile: a second start waits for the
// first to finish claiming the profile, times out if it never does, and takes
// over the lock of a start that crashed.
func TestProfileStartLockSerialisesStartsOfOneProfile(t *testing.T) {
	testenv.IsolateUserDirs(t)
	release, err := acquireProfileStartLock("a", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A different profile is independent.
	other, err := acquireProfileStartLock("b", time.Second, time.Minute)
	if err != nil {
		t.Fatalf("another profile was blocked: %v", err)
	}
	other()

	// The same profile waits, and gets the lock as soon as it is released.
	acquired := make(chan error, 1)
	go func() {
		second, err := acquireProfileStartLock("a", 5*time.Second, time.Minute)
		if err == nil {
			second()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		t.Fatalf("a second start got the lock while the first held it (%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("the waiting start failed after the release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting start never got the lock")
	}

	// It gives up rather than wait forever behind a start that never finishes.
	held, err := acquireProfileStartLock("c", time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	if _, err := acquireProfileStartLock("c", 200*time.Millisecond, time.Minute); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("err = %v, want it to say another start is in progress", err)
	}
	// A lock older than the stale age is a crashed start and is taken over.
	takeover, err := acquireProfileStartLock("c", time.Second, 0)
	if err != nil {
		t.Fatalf("a stale lock was not taken over: %v", err)
	}
	takeover()
}
