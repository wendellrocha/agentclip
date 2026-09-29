package selfupgrade

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

// harness wires a Runner to fakes that record every effect in order.
type harness struct {
	t      *testing.T
	calls  []string
	stdout bytes.Buffer
	stderr bytes.Buffer

	staged   string // path of the staged binary on disk
	target   string
	profiles []companion.Profile
	active   []string
	notice   string

	latestErr, prepareErr, profilesErr, activeErr, stopErr, replaceErr, restartErr, windowsErr error
	remoteErrs                                                                                 map[string]error
	restarted                                                                                  [][]string

	runner Runner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{t: t, target: filepath.Join(dir, "agentclip"), staged: filepath.Join(dir, ".agentclip-upgrade-1")}
	if err := os.WriteFile(h.staged, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.runner = Runner{
		GOOS:   "linux",
		Stdout: &h.stdout,
		Stderr: &h.stderr,
		LatestTag: func(context.Context) (string, error) {
			h.calls = append(h.calls, "latest")
			return "v9.9.9", h.latestErr
		},
		Executable: func() (string, error) {
			h.calls = append(h.calls, "executable")
			return h.target, nil
		},
		Prepare: func(_ context.Context, tag, executable string) (upgrader.StagedBinary, error) {
			h.calls = append(h.calls, "prepare "+tag)
			if executable != h.target {
				t.Errorf("Prepare got executable %q, want %q", executable, h.target)
			}
			if h.prepareErr != nil {
				return upgrader.StagedBinary{}, h.prepareErr
			}
			return upgrader.StagedBinary{Path: h.staged, Target: h.target, Notice: h.notice}, nil
		},
		Profiles: func() ([]companion.Profile, error) {
			h.calls = append(h.calls, "profiles")
			return h.profiles, h.profilesErr
		},
		ActiveCompanions: func([]companion.Profile) ([]string, error) {
			h.calls = append(h.calls, "active")
			return h.active, h.activeErr
		},
		InstallRemote: func(profile companion.Profile, tag string) error {
			h.calls = append(h.calls, "remote "+profile.Name+" "+tag)
			return h.remoteErrs[profile.Name]
		},
		StopCompanions: func(names []string) error {
			h.calls = append(h.calls, "stop "+strings.Join(names, ","))
			return h.stopErr
		},
		RestartCompanions: func(names []string) error {
			h.calls = append(h.calls, "restart "+strings.Join(names, ","))
			h.restarted = append(h.restarted, names)
			return h.restartErr
		},
		Replace: func(from, to string) error {
			h.calls = append(h.calls, "replace")
			if h.replaceErr != nil {
				return h.replaceErr
			}
			return os.Rename(from, to)
		},
		LaunchWindowsReplacement: func(_ upgrader.StagedBinary, active []string) error {
			h.calls = append(h.calls, "windows "+strings.Join(active, ","))
			return h.windowsErr
		},
	}
	return h
}

func (h *harness) run() error { return h.runner.Run(context.Background()) }

func (h *harness) stagedExists() bool {
	_, err := os.Stat(h.staged)
	return err == nil
}

func (h *harness) targetContent() string {
	data, _ := os.ReadFile(h.target)
	return string(data)
}

func (h *harness) expectCalls(want ...string) {
	h.t.Helper()
	if !reflect.DeepEqual(h.calls, want) {
		h.t.Fatalf("calls =\n  %q\nwant\n  %q", h.calls, want)
	}
}

func profile(name string) companion.Profile {
	return companion.Profile{Name: name, Destination: name + "-host", SSHIdentityFile: "/keys/" + name}
}

func TestUpgradeUpdatesServersThenReplacesTheExecutableAndRestartsActiveCompanions(t *testing.T) {
	h := newHarness(t)
	h.profiles = []companion.Profile{profile("m2"), profile("vortx")}
	h.active = []string{"m2"}

	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.expectCalls(
		"latest", "executable", "prepare v9.9.9", "profiles", "active",
		"remote m2 v9.9.9", "remote vortx v9.9.9",
		"stop m2", "replace", "restart m2",
	)
	if h.targetContent() != "new" {
		t.Fatalf("the executable was not replaced: %q", h.targetContent())
	}
	for _, want := range []string{`Updating "m2" on m2-host...`, "m2: updated", "vortx: updated", "AgentClip updated to v9.9.9."} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, h.stdout.String())
		}
	}
}

func TestUpgradeRestartsOnlyTheCompanionsThatWereActiveWhenItStarted(t *testing.T) {
	h := newHarness(t)
	h.profiles = []companion.Profile{profile("m2"), profile("vortx"), profile("idle")}
	h.active = []string{"vortx"}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"vortx"}}; !reflect.DeepEqual(h.restarted, want) {
		t.Fatalf("restarted %v, want %v", h.restarted, want)
	}
}

func TestUpgradeWithoutProfilesStillReplacesTheExecutable(t *testing.T) {
	h := newHarness(t)
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.expectCalls("latest", "executable", "prepare v9.9.9", "profiles", "active", "stop ", "replace", "restart ")
	if !strings.Contains(h.stdout.String(), "No remote profiles configured.") {
		t.Fatalf("output = %q", h.stdout.String())
	}
}

func TestUpgradeStopsBeforeAnyEffectWhenTheReleaseCannotBeResolved(t *testing.T) {
	h := newHarness(t)
	h.latestErr = errors.New("rate limited")
	err := h.run()
	if err == nil || !strings.Contains(err.Error(), "resolve latest AgentClip release") || !errors.Is(err, h.latestErr) {
		t.Fatalf("err = %v", err)
	}
	h.expectCalls("latest")
}

func TestUpgradeTouchesNothingWhenThePreparedBinaryIsRefused(t *testing.T) {
	h := newHarness(t)
	h.profiles = []companion.Profile{profile("m2")}
	h.active = []string{"m2"}
	h.prepareErr = errors.New("no build attestation")
	err := h.run()
	if err == nil || !strings.Contains(err.Error(), "prepare AgentClip v9.9.9") || !errors.Is(err, h.prepareErr) {
		t.Fatalf("err = %v", err)
	}
	// Nothing on any server or on this machine changes when verification fails.
	h.expectCalls("latest", "executable", "prepare v9.9.9")
	if h.targetContent() != "" {
		t.Fatal("the executable must not change")
	}
}

func TestUpgradeShowsTheVerificationNoticeAsAWarning(t *testing.T) {
	h := newHarness(t)
	h.notice = "v0.7.0 predates build attestations"
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.stderr.String(), "warning: v0.7.0 predates build attestations") {
		t.Fatalf("stderr = %q", h.stderr.String())
	}
}

func TestUpgradeCleansUpTheStagedBinaryWhenItCannotListProfilesOrCompanions(t *testing.T) {
	t.Run("profiles", func(t *testing.T) {
		h := newHarness(t)
		h.profilesErr = errors.New("unreadable")
		if err := h.run(); !errors.Is(err, h.profilesErr) {
			t.Fatalf("err = %v", err)
		}
		if h.stagedExists() {
			t.Fatal("the staged binary was left behind")
		}
		h.expectCalls("latest", "executable", "prepare v9.9.9", "profiles")
	})
	t.Run("active companions", func(t *testing.T) {
		h := newHarness(t)
		h.activeErr = errors.New("unreadable")
		if err := h.run(); !errors.Is(err, h.activeErr) {
			t.Fatalf("err = %v", err)
		}
		if h.stagedExists() {
			t.Fatal("the staged binary was left behind")
		}
	})
}

func TestUpgradeRestoresTheCompanionsWhenTheyCannotBeStopped(t *testing.T) {
	h := newHarness(t)
	h.profiles = []companion.Profile{profile("m2")}
	h.active = []string{"m2"}
	h.stopErr = errors.New("stuck")
	if err := h.run(); !errors.Is(err, h.stopErr) {
		t.Fatalf("err = %v", err)
	}
	h.expectCalls("latest", "executable", "prepare v9.9.9", "profiles", "active", "remote m2 v9.9.9", "stop m2", "restart m2")
	if h.stagedExists() || h.targetContent() != "" {
		t.Fatalf("staged exists = %v, target = %q; want the machine left as found", h.stagedExists(), h.targetContent())
	}
}

func TestUpgradeRestoresTheCompanionsWhenTheExecutableCannotBeReplaced(t *testing.T) {
	h := newHarness(t)
	h.active = []string{"m2"}
	h.replaceErr = errors.New("permission denied")
	err := h.run()
	if err == nil || !strings.Contains(err.Error(), "replace AgentClip executable") || !errors.Is(err, h.replaceErr) {
		t.Fatalf("err = %v", err)
	}
	h.expectCalls("latest", "executable", "prepare v9.9.9", "profiles", "active", "stop m2", "replace", "restart m2")
	if h.stagedExists() {
		t.Fatal("the staged binary was left behind")
	}
}

func TestUpgradeStillReplacesThisMachineWhenSomeServersFailAndReportsThem(t *testing.T) {
	h := newHarness(t)
	h.profiles = []companion.Profile{profile("m2"), profile("vortx")}
	h.remoteErrs = map[string]error{"vortx": errors.New("ssh: connection refused")}
	err := h.run()
	if err == nil || !strings.Contains(err.Error(), "one or more remote hosts could not be updated") {
		t.Fatalf("err = %v", err)
	}
	if h.targetContent() != "new" {
		t.Fatal("the local executable must still be updated")
	}
	for _, want := range []string{"m2: updated", "vortx: failed (ssh: connection refused)"} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, h.stdout.String())
		}
	}
	if strings.Contains(h.stdout.String(), "AgentClip updated to") {
		t.Fatal("a partial failure must not announce success")
	}
}

func TestUpgradeReportsARestartFailureAfterReplacing(t *testing.T) {
	h := newHarness(t)
	h.profiles = []companion.Profile{profile("m2")}
	h.active = []string{"m2"}
	h.restartErr = errors.New("restart Companion(s): m2: port busy")
	if err := h.run(); !errors.Is(err, h.restartErr) {
		t.Fatalf("err = %v", err)
	}
	if h.targetContent() != "new" || !strings.Contains(h.stdout.String(), "m2: updated") {
		t.Fatalf("target = %q, output = %q", h.targetContent(), h.stdout.String())
	}
}

func TestUpgradeOnWindowsHandsTheSwapToTheHelperInsteadOfRenaming(t *testing.T) {
	h := newHarness(t)
	h.runner.GOOS = "windows"
	h.profiles = []companion.Profile{profile("m2")}
	h.active = []string{"m2"}
	if err := h.run(); err != nil {
		t.Fatal(err)
	}
	h.expectCalls("latest", "executable", "prepare v9.9.9", "profiles", "active", "remote m2 v9.9.9", "stop m2", "windows m2")
	if !strings.Contains(h.stdout.String(), "will replace itself and restart 1 Companion(s) shortly") {
		t.Fatalf("output = %q", h.stdout.String())
	}
	if h.targetContent() != "" {
		t.Fatal("Windows must not overwrite the running executable directly")
	}
}

func TestUpgradeOnWindowsRestoresTheCompanionsWhenTheHelperFails(t *testing.T) {
	h := newHarness(t)
	h.runner.GOOS = "windows"
	h.active = []string{"m2"}
	h.windowsErr = errors.New("powershell missing")
	if err := h.run(); !errors.Is(err, h.windowsErr) {
		t.Fatalf("err = %v", err)
	}
	h.expectCalls("latest", "executable", "prepare v9.9.9", "profiles", "active", "stop m2", "windows m2", "restart m2")
	if h.stagedExists() {
		t.Fatal("the staged binary was left behind")
	}
}

func TestUpgradeOnWindowsStillReportsServerFailures(t *testing.T) {
	h := newHarness(t)
	h.runner.GOOS = "windows"
	h.profiles = []companion.Profile{profile("m2")}
	h.remoteErrs = map[string]error{"m2": errors.New("boom")}
	err := h.run()
	if err == nil || !strings.Contains(err.Error(), "the local update will finish shortly") {
		t.Fatalf("err = %v", err)
	}
}

func TestSummaryAndFailureDetection(t *testing.T) {
	var out bytes.Buffer
	PrintSummary(&out, nil)
	if out.String() != "No remote profiles configured.\n" {
		t.Fatalf("empty summary = %q", out.String())
	}
	if HasFailure(nil) || HasFailure([]RemoteResult{{Profile: "a"}}) {
		t.Fatal("successful results must not fail")
	}
	if !HasFailure([]RemoteResult{{Profile: "a"}, {Profile: "b", Err: errors.New("ssh")}}) {
		t.Fatal("a failed server must be reported")
	}
}
