package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/remote"
	"github.com/wendellrocha/agentclip/internal/testenv"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

// The whole lifecycle of a server, through the same functions the commands run:
// setup installs the verified binary and registers the MCP entry, running it
// again does not download anything, connect registers again, uninstall removes
// only AgentClip's entry. The server is a fake `ssh` that runs the remote
// commands locally with a home of its own, the harness is a script that logs its
// arguments, and the release download is replaced; everything else is real.
func TestServerLifecycleSetupConnectUninstall(t *testing.T) {
	server := testenv.NewFakeServer(t)
	harnessLog := filepath.Join(t.TempDir(), "codex.log")
	server.Install("codex", `echo "codex $*" >> '`+harnessLog+`'`)

	downloads := 0
	previous := remote.FetchRelease
	t.Cleanup(func() { remote.FetchRelease = previous })
	remote.FetchRelease = func(_ context.Context, options upgrader.Options) (upgrader.Fetched, error) {
		downloads++
		path := filepath.Join(t.TempDir(), "agentclip")
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+options.Version+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return upgrader.Fetched{Path: path}, nil
	}
	logged := func() []string {
		data, _ := os.ReadFile(harnessLog)
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}

	if err := runSetup([]string{"bastion-e2e", "--profile", "e2e", "--version", "v0.7.3", "--agent", "codex", "--no-start"}); err != nil {
		t.Fatal(err)
	}
	profile, err := companion.LoadProfile("e2e")
	if err != nil || profile.Destination != "bastion-e2e" || profile.Token == "" || profile.UploadToken == "" {
		t.Fatalf("saved profile = %+v, %v", profile, err)
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want the release fetched once", downloads)
	}
	if out, err := os.ReadFile(server.Path("agentclip")); err != nil || !strings.Contains(string(out), "v0.7.3") {
		t.Fatalf("the server has %q, %v", out, err)
	}
	calls := logged()
	if len(calls) != 2 || calls[0] != "codex mcp remove agentclip-e2e" || !strings.Contains(calls[1], "codex mcp add agentclip-e2e") {
		t.Fatalf("harness calls after setup = %q, want a removal then an addition", calls)
	}
	for _, secret := range []string{"AGENTCLIP_SESSION_TOKEN=" + profile.Token, "AGENTCLIP_UPLOAD_TOKEN=" + profile.UploadToken} {
		if !strings.Contains(calls[1], secret) {
			t.Errorf("the server was registered without %s", strings.SplitN(secret, "=", 2)[0])
		}
	}

	// Running setup again pairs afresh but has nothing to install.
	if err := runSetup([]string{"bastion-e2e", "--profile", "e2e", "--version", "v0.7.3", "--agent", "codex", "--no-start"}); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Fatalf("a second setup downloaded again (%d downloads)", downloads)
	}
	repaired, err := companion.LoadProfile("e2e")
	if err != nil || repaired.Token == profile.Token {
		t.Fatalf("a second setup must issue a new pairing token (err %v)", err)
	}
	if got := logged(); len(got) != 4 {
		t.Fatalf("harness calls after the second setup = %q", got)
	}

	// connect registers the saved profile again, without touching the profile.
	if err := runConnect([]string{"e2e", "--agent", "codex"}); err != nil {
		t.Fatal(err)
	}
	if got := logged(); len(got) != 6 || !strings.Contains(got[5], "AGENTCLIP_SESSION_TOKEN="+repaired.Token) {
		t.Fatalf("harness calls after connect = %q", got)
	}

	// uninstall removes only AgentClip's own entry.
	if err := runUninstall([]string{"e2e", "--agent", "codex"}); err != nil {
		t.Fatal(err)
	}
	if got := logged(); got[len(got)-1] != "codex mcp remove agentclip-e2e" {
		t.Fatalf("last harness call = %q, want the removal of agentclip-e2e", got[len(got)-1])
	}
}

// setup refuses to touch a server whose release cannot be verified, and leaves
// no profile behind.
func TestSetupWithAnUnverifiableReleaseInstallsNothingAndSavesNoProfile(t *testing.T) {
	server := testenv.NewFakeServer(t)
	server.Install("codex", "true")
	previous := remote.FetchRelease
	t.Cleanup(func() { remote.FetchRelease = previous })
	remote.FetchRelease = func(context.Context, upgrader.Options) (upgrader.Fetched, error) {
		return upgrader.Fetched{}, os.ErrPermission
	}
	err := runSetup([]string{"bastion-e2e", "--profile", "refused", "--version", "v0.7.3", "--agent", "codex", "--no-start"})
	if err == nil || !strings.Contains(err.Error(), "install AgentClip on bastion-e2e") {
		t.Fatalf("err = %v, want the install failure", err)
	}
	if _, statErr := os.Stat(server.Path("agentclip")); statErr == nil {
		t.Error("a binary was installed although verification failed")
	}
	if _, loadErr := companion.LoadProfile("refused"); loadErr == nil {
		t.Error("a profile was saved for a server that was never set up")
	}
}
