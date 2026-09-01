package companion

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

type TunnelStatus struct {
	Connected bool      `json:"connected"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TunnelCommand builds the long-running reverse SSH forward for a paired
// profile. It never binds a public remote interface.
func TunnelCommand(profile Profile, localPort int) (*exec.Cmd, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if localPort < 1 || localPort > 65535 {
		return nil, fmt.Errorf("companion local port must be between 1 and 65535")
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", profile.RemotePort, localPort)
	return exec.Command("ssh",
		"-N",
		"-R", forward,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		profile.Destination,
	), nil
}

// RunTunnel reconnects after unexpected SSH exits. Cancellation stops the
// current child process and terminates the retry loop.
func RunTunnel(ctx context.Context, profile Profile, localPort int) error {
	return RunTunnelWithStatus(ctx, profile, localPort, nil)
}

// RunTunnelWithStatus reconnects after unexpected SSH exits and reports the
// effective state to callers that render a dashboard.
func RunTunnelWithStatus(ctx context.Context, profile Profile, localPort int, observe func(TunnelStatus)) error {
	notify := func(connected bool, err error) {
		if observe == nil {
			return
		}
		status := TunnelStatus{Connected: connected, UpdatedAt: time.Now().UTC()}
		if err != nil {
			status.LastError = err.Error()
		}
		observe(status)
	}
	delay := time.Second
	for {
		command, err := TunnelCommand(profile, localPort)
		if err != nil {
			notify(false, err)
			return err
		}
		if err := command.Start(); err != nil {
			err = fmt.Errorf("start companion SSH tunnel: %w", err)
			notify(false, err)
			return err
		}
		notify(true, nil)
		result := make(chan error, 1)
		go func() { result <- command.Wait() }()
		select {
		case <-ctx.Done():
			_ = command.Process.Kill()
			<-result
			notify(false, nil)
			return nil
		case err = <-result:
			notify(false, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}
