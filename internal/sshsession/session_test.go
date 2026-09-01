package sshsession

import (
	"strings"
	"testing"
)

func TestNewGeneratesURLSafeValues(t *testing.T) {
	session, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if session.ID == "" || session.Token == "" {
		t.Fatal("New() returned an empty field")
	}
	if strings.ContainsAny(session.ID+session.Token, "=+/ ") {
		t.Fatalf("session contains an unsafe shell character: %#v", session)
	}
}

func TestCommandBuildsLoopbackForwardAndSessionEnvironment(t *testing.T) {
	command, err := Command(Options{
		Destination:   "dev",
		SSHArgs:       []string{"-J", "bastion"},
		RemoteCommand: []string{"codex", "--model", "gpt-5"},
		LocalPort:     45678,
		RemotePort:    39123,
		Session:       Session{ID: "session", Token: "token"},
	})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	arguments := strings.Join(command.Args, " ")
	for _, expected := range []string{
		"-R 127.0.0.1:39123:127.0.0.1:45678",
		"ExitOnForwardFailure=yes",
		"-J bastion",
		"dev",
		"export AGENTCLIP_SESSION=session",
		"export AGENTCLIP_BRIDGE_PORT=39123",
		"export AGENTCLIP_SESSION_TOKEN=token",
		"exec 'codex' '--model' 'gpt-5'",
	} {
		if !strings.Contains(arguments, expected) {
			t.Errorf("command %q does not contain %q", arguments, expected)
		}
	}
}

func TestCommandRejectsInvalidOptions(t *testing.T) {
	_, err := Command(Options{})
	if err == nil {
		t.Fatal("Command() error = nil, want validation error")
	}
}
