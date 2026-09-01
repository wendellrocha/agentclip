// Package sshsession builds the SSH invocation that connects AgentClip's local
// bridge to an interactive remote shell.
package sshsession

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const tokenBytes = 32

// Session contains the short-lived values that let the remote MCP process use
// the loopback SSH forward for one interactive shell session.
type Session struct {
	ID    string
	Token string
}

// New creates a session whose fields are safe to pass as POSIX-shell values.
func New() (Session, error) {
	id, err := randomValue(16)
	if err != nil {
		return Session{}, err
	}
	token, err := randomValue(tokenBytes)
	if err != nil {
		return Session{}, err
	}
	return Session{ID: id, Token: token}, nil
}

func randomValue(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random session value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// Options describes a single interactive SSH connection.
type Options struct {
	Destination   string
	SSHArgs       []string
	RemoteCommand []string
	LocalPort     int
	RemotePort    int
	Session       Session
}

// Command builds an ssh invocation without executing it.
func Command(options Options) (*exec.Cmd, error) {
	if strings.TrimSpace(options.Destination) == "" {
		return nil, fmt.Errorf("ssh destination is required")
	}
	if options.LocalPort < 1 || options.LocalPort > 65535 {
		return nil, fmt.Errorf("local bridge port must be between 1 and 65535")
	}
	if options.RemotePort < 1 || options.RemotePort > 65535 {
		return nil, fmt.Errorf("remote bridge port must be between 1 and 65535")
	}
	if options.Session.ID == "" || options.Session.Token == "" {
		return nil, fmt.Errorf("ssh session is required")
	}

	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", options.RemotePort, options.LocalPort)
	args := []string{
		"-R", forward,
		"-o", "ExitOnForwardFailure=yes",
		"-tt",
	}
	args = append(args, options.SSHArgs...)
	args = append(args, options.Destination, remoteShell(options.RemotePort, options.Session, options.RemoteCommand))
	return exec.Command("ssh", args...), nil
}

func remoteShell(remotePort int, session Session, remoteCommand []string) string {
	// Session values use RawURLEncoding and the port is an integer, so they do
	// not require shell interpolation or quote escaping.
	commands := []string{
		"export AGENTCLIP_SESSION=" + session.ID,
		"export AGENTCLIP_BRIDGE_PORT=" + strconv.Itoa(remotePort),
		"export AGENTCLIP_SESSION_TOKEN=" + session.Token,
	}
	if len(remoteCommand) == 0 {
		commands = append(commands, `exec "${SHELL:-/bin/sh}" -l`)
	} else {
		quoted := make([]string, len(remoteCommand))
		for i, argument := range remoteCommand {
			quoted[i] = shellQuote(argument)
		}
		commands = append(commands, "exec "+strings.Join(quoted, " "))
	}
	return strings.Join(commands, "; ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
