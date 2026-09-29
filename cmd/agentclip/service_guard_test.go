package main

import (
	"strings"
	"testing"

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
