package main

import (
	"strings"
	"testing"
)

func TestRemotePreflightPassesTheWholeCheckAsOneRemoteCommand(t *testing.T) {
	command := remotePreflightCommand("bastion-m2")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	if command.Args[2] != "sh -lc 'export PATH=\"$HOME/.local/bin:$PATH\"; command -v agentclip >/dev/null && command -v codex >/dev/null'" {
		t.Fatalf("remote command = %q", command.Args[2])
	}
}

func TestRemoteLoginCommandEscapesArgumentsInsideLoginShell(t *testing.T) {
	command := remoteLoginCommand("bastion-m2", "codex", "mcp", "add", "agentclip-m2", "--env", "TOKEN=has'aquote")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	if !strings.HasPrefix(command.Args[2], "sh -lc '") || !strings.Contains(command.Args[2], "'\"'\"'") {
		t.Fatalf("remote login command is not safely quoted: %q", command.Args[2])
	}
}

func TestRemoteInstallCommandPinsTheRequestedRelease(t *testing.T) {
	command := remoteInstallCommand("bastion-m2", "v0.2.0")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	if !strings.HasPrefix(command.Args[2], "sh -lc '") {
		t.Fatalf("remote installer command = %q", command.Args[2])
	}
	script := remoteInstallScript("v0.2.0")
	if !strings.Contains(script, "https://raw.githubusercontent.com/wendellrocha/agentclip/v0.2.0/scripts/install.sh") || !strings.Contains(script, "--version 'v0.2.0'") {
		t.Fatalf("remote installer script = %q", script)
	}
}

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
			got, err := releaseTag(test.requested)
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
				t.Fatalf("releaseTag(%q) = %q, want %q", test.requested, got, test.want)
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
		if got := defaultProfileName(destination); got != want {
			t.Errorf("defaultProfileName(%q) = %q, want %q", destination, got, want)
		}
	}
}
