package setup

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
)

func TestReleaseTag(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		want      string
		wantError bool
	}{
		{name: "prefixes a stable local version", requested: "0.2.0", want: "v0.2.0"},
		{name: "accepts a tag", requested: "v1.2.3-rc.1", want: "v1.2.3-rc.1"},
		{name: "rejects development builds", requested: "0.2.0-dev", wantError: true},
		{name: "rejects an invalid version", requested: "latest", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReleaseTag(test.requested)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ReleaseTag(%q) = %q, want %q", test.requested, got, test.want)
			}
		})
	}
}

func TestDefaultProfileName(t *testing.T) {
	tests := map[string]string{
		"bastion-m2":              "bastion-m2",
		"wendell@bastion.example": "bastion-example",
		"user@[2001:db8::1]":      "2001-db8-1",
	}
	for destination, want := range tests {
		if got := DefaultProfileName(destination); got != want {
			t.Errorf("DefaultProfileName(%q) = %q, want %q", destination, got, want)
		}
	}
}

func TestParseArgsDefaultsAndDerivedProfileName(t *testing.T) {
	options, err := ParseArgs([]string{"wendell@bastion.example"})
	if err != nil {
		t.Fatal(err)
	}
	want := Options{Destination: "wendell@bastion.example", Profile: "bastion-example", Agent: "all", RemotePort: 39123}
	if options != want {
		t.Fatalf("options = %+v, want %+v", options, want)
	}
}

func TestParseArgsReadsEveryFlag(t *testing.T) {
	options, err := ParseArgs([]string{"bastion-m2", "--profile", "m2", "--agent", "claude", "--version", "v0.7.1", "--remote-port", "40000", "--skip-agent", "--skip-install", "--no-start"})
	if err != nil {
		t.Fatal(err)
	}
	want := Options{Destination: "bastion-m2", Profile: "m2", Agent: "claude", ReleaseVersion: "v0.7.1", RemotePort: 40000, SkipAgent: true, SkipInstall: true, NoStart: true}
	if options != want {
		t.Fatalf("options = %+v, want %+v", options, want)
	}
}

func TestParseArgsTreatsSkipCodexAsAnAliasForSkipAgent(t *testing.T) {
	options, err := ParseArgs([]string{"bastion-m2", "--skip-codex"})
	if err != nil || !options.SkipAgent {
		t.Fatalf("options = %+v, err = %v", options, err)
	}
}

func TestParseArgsRejectsInvalidInput(t *testing.T) {
	tests := map[string]struct {
		arguments []string
		want      string
	}{
		"no arguments":           {nil, "usage: agentclip setup"},
		"destination is a flag":  {[]string{"-oProxyCommand=evil"}, "must not start with a dash"},
		"unknown flag":           {[]string{"host", "--nope"}, "parse setup options"},
		"flag without a value":   {[]string{"host", "--profile"}, "parse setup options"},
		"invalid remote port":    {[]string{"host", "--remote-port", "abc"}, "parse setup options"},
		"extra positional value": {[]string{"host", "extra"}, "unexpected setup arguments: extra"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseArgs(test.arguments)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

type harness struct {
	t      *testing.T
	calls  []string
	stdout bytes.Buffer

	existing                                                             companion.Profile
	loadErr, identityErr, tagErr, installErr, stopErr, pairErr, startErr error
	running                                                              bool
	runner                                                               Runner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, loadErr: errors.New("no saved profile")}
	h.runner = Runner{
		Stdout: &h.stdout,
		LoadProfile: func(name string) (companion.Profile, error) {
			h.calls = append(h.calls, "load "+name)
			return h.existing, h.loadErr
		},
		EnsureIdentity: func(destination, profileName, existing string) (string, error) {
			h.calls = append(h.calls, "identity "+destination+" "+profileName+" existing="+existing)
			if h.identityErr != nil {
				return "", h.identityErr
			}
			return "/keys/agentclip-" + profileName, nil
		},
		ReleaseTag: func(requested string) (string, error) {
			h.calls = append(h.calls, "tag "+requested)
			if h.tagErr != nil {
				return "", h.tagErr
			}
			return "v9.9.9", nil
		},
		Install: func(destination, identityFile, tag string) error {
			h.calls = append(h.calls, "install "+destination+" "+identityFile+" "+tag)
			return h.installErr
		},
		CompanionRunning: func(name string) bool {
			h.calls = append(h.calls, "running? "+name)
			return h.running
		},
		StopCompanion: func(name string) error {
			h.calls = append(h.calls, "stop "+name)
			return h.stopErr
		},
		Pair: func(name, destination string, port int, agent string, skipAgent bool, identityFile string) (companion.Profile, error) {
			h.calls = append(h.calls, "pair "+name+" "+destination+" port="+strconv.Itoa(port)+" agent="+agent+" skipAgent="+strconv.FormatBool(skipAgent)+" "+identityFile)
			if h.pairErr != nil {
				return companion.Profile{}, h.pairErr
			}
			return companion.Profile{Name: name, Destination: destination}, nil
		},
		StartCompanion: func(name string) error {
			h.calls = append(h.calls, "start "+name)
			return h.startErr
		},
	}
	return h
}

func (h *harness) expectCalls(want ...string) {
	h.t.Helper()
	if !reflect.DeepEqual(h.calls, want) {
		h.t.Fatalf("calls =\n  %q\nwant\n  %q", h.calls, want)
	}
}

func options(mutate func(*Options)) Options {
	o := Options{Destination: "bastion-m2", Profile: "m2", Agent: "all", RemotePort: 39123}
	if mutate != nil {
		mutate(&o)
	}
	return o
}

func TestSetupInstallsPairsAndStartsInOrder(t *testing.T) {
	h := newHarness(t)
	if err := h.runner.Run(options(nil)); err != nil {
		t.Fatal(err)
	}
	h.expectCalls(
		"load m2",
		"identity bastion-m2 m2 existing=",
		"tag ",
		"install bastion-m2 /keys/agentclip-m2 v9.9.9",
		"running? m2",
		"pair m2 bastion-m2 port=39123 agent=all skipAgent=false /keys/agentclip-m2",
		"start m2",
	)
	for _, want := range []string{"Installing AgentClip v9.9.9 on bastion-m2...", `Setup complete for "m2". SSH normally`} {
		if !strings.Contains(h.stdout.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, h.stdout.String())
		}
	}
}

func TestSetupReusesTheSSHIdentityOfAnExistingProfile(t *testing.T) {
	h := newHarness(t)
	h.loadErr = nil
	h.existing = companion.Profile{Name: "m2", SSHIdentityFile: "/keys/already-there"}
	if err := h.runner.Run(options(nil)); err != nil {
		t.Fatal(err)
	}
	if h.calls[1] != "identity bastion-m2 m2 existing=/keys/already-there" {
		t.Fatalf("identity call = %q", h.calls[1])
	}
}

func TestSetupForwardsPairingOptions(t *testing.T) {
	h := newHarness(t)
	if err := h.runner.Run(options(func(o *Options) {
		o.RemotePort = 40000
		o.Agent = "claude"
		o.SkipAgent = true
		o.ReleaseVersion = "0.7.1"
	})); err != nil {
		t.Fatal(err)
	}
	if got := h.calls[2]; got != "tag 0.7.1" {
		t.Fatalf("tag call = %q", got)
	}
	want := "pair m2 bastion-m2 port=40000 agent=claude skipAgent=true /keys/agentclip-m2"
	if h.calls[5] != want {
		t.Fatalf("pair call = %q, want %q", h.calls[5], want)
	}
}

func TestSetupSkipInstallNeverResolvesOrInstallsARelease(t *testing.T) {
	h := newHarness(t)
	if err := h.runner.Run(options(func(o *Options) { o.SkipInstall = true })); err != nil {
		t.Fatal(err)
	}
	for _, call := range h.calls {
		if strings.HasPrefix(call, "tag") || strings.HasPrefix(call, "install") {
			t.Fatalf("unexpected call %q", call)
		}
	}
}

func TestSetupStopsARunningCompanionBeforePairingSoTheOldTokenIsNotKept(t *testing.T) {
	h := newHarness(t)
	h.running = true
	if err := h.runner.Run(options(nil)); err != nil {
		t.Fatal(err)
	}
	stop, pair := -1, -1
	for i, call := range h.calls {
		if call == "stop m2" {
			stop = i
		}
		if strings.HasPrefix(call, "pair ") {
			pair = i
		}
	}
	if stop < 0 || pair < 0 || stop > pair {
		t.Fatalf("the Companion must be stopped before pairing: %q", h.calls)
	}
}

func TestSetupDoesNotStopACompanionThatIsNotRunning(t *testing.T) {
	h := newHarness(t)
	if err := h.runner.Run(options(nil)); err != nil {
		t.Fatal(err)
	}
	for _, call := range h.calls {
		if strings.HasPrefix(call, "stop") {
			t.Fatalf("unexpected call %q", call)
		}
	}
}

func TestSetupStopsAtTheFirstFailure(t *testing.T) {
	tests := map[string]struct {
		configure func(*harness)
		wantError string
		lastCall  string
	}{
		"identity":    {func(h *harness) { h.identityErr = errors.New("ssh denied") }, "ssh denied", "identity bastion-m2 m2 existing="},
		"release tag": {func(h *harness) { h.tagErr = errors.New("bad tag") }, "bad tag", "tag "},
		"install":     {func(h *harness) { h.installErr = errors.New("exit 255") }, "install AgentClip on bastion-m2: exit 255", "install bastion-m2 /keys/agentclip-m2 v9.9.9"},
		"stop":        {func(h *harness) { h.running = true; h.stopErr = errors.New("stuck") }, "stuck", "stop m2"},
		"pairing":     {func(h *harness) { h.pairErr = errors.New("no agent found") }, "no agent found", "pair m2 bastion-m2 port=39123 agent=all skipAgent=false /keys/agentclip-m2"},
		"start":       {func(h *harness) { h.startErr = errors.New("port busy") }, "port busy", "start m2"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			test.configure(h)
			err := h.runner.Run(options(nil))
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("err = %v, want it to contain %q", err, test.wantError)
			}
			if last := h.calls[len(h.calls)-1]; last != test.lastCall {
				t.Fatalf("last call = %q, want %q (nothing may run after the failure)", last, test.lastCall)
			}
			if strings.Contains(h.stdout.String(), "Setup complete") {
				t.Fatalf("a failed setup announced success: %q", h.stdout.String())
			}
		})
	}
}

func TestSetupNoStartPrintsHowToStartTheCompanion(t *testing.T) {
	h := newHarness(t)
	if err := h.runner.Run(options(func(o *Options) { o.NoStart = true })); err != nil {
		t.Fatal(err)
	}
	for _, call := range h.calls {
		if strings.HasPrefix(call, "start") {
			t.Fatalf("unexpected call %q", call)
		}
	}
	if !strings.Contains(h.stdout.String(), "Start it with: agentclip companion start m2") {
		t.Fatalf("output = %q", h.stdout.String())
	}
}
