package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wendellrocha/agentclip/internal/autostart"
	"github.com/wendellrocha/agentclip/internal/companion"
)

const autostartUsage = "usage: agentclip companion autostart <enable|disable|status> <profile>"

// runAutostart turns on, off or reports the start of a profile's Companion at
// login. The profile must exist to enable it; it need not to disable, so a
// service outlives a deleted profile only until the user asks for it to go.
func runAutostart(arguments []string, stdout io.Writer) error {
	if len(arguments) != 2 {
		return errors.New(autostartUsage)
	}
	action, name := arguments[0], arguments[1]
	spec, err := autostartSpec(name)
	if err != nil {
		return err
	}
	runner := systemAutostartRunner()
	switch action {
	case "enable":
		if _, err := companion.LoadProfile(name); err != nil {
			return err
		}
		if err := runner.Enable(spec); err != nil {
			return fmt.Errorf("turn on autostart for %q: %w", name, err)
		}
		fmt.Fprintf(stdout, "Companion %q will start when you log in. Turn it off with: agentclip companion autostart disable %s\n", name, name)
	case "disable":
		if err := runner.Disable(spec); err != nil {
			return fmt.Errorf("turn off autostart for %q: %w", name, err)
		}
		fmt.Fprintf(stdout, "Companion %q will no longer start when you log in. A Companion the service started is stopped; one you started yourself keeps running.\n", name)
	case "status":
		on, err := runner.Enabled(spec)
		if err != nil {
			return err
		}
		if on {
			fmt.Fprintf(stdout, "Companion %q starts when you log in.\n", name)
		} else {
			fmt.Fprintf(stdout, "Companion %q does not start when you log in.\n", name)
		}
	default:
		return errors.New(autostartUsage)
	}
	return nil
}

func autostartSpec(name string) (autostart.Spec, error) {
	executable, err := os.Executable()
	if err != nil {
		return autostart.Spec{}, fmt.Errorf("locate AgentClip executable: %w", err)
	}
	// The service should keep working when the binary is replaced in place by
	// an upgrade, so name the path the user runs, not a versioned location.
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return autostart.Spec{}, fmt.Errorf("locate the home directory: %w", err)
	}
	spec := autostart.Spec{Profile: name, Executable: executable, HomeDir: home, UID: os.Getuid(), LogPath: companionLogPath(name) + ".autostart"}
	// The profile lives where AGENTCLIP_CONFIG_DIR says; a login service does not
	// inherit this shell, so it has to be written into the service.
	if directory := strings.TrimSpace(os.Getenv("AGENTCLIP_CONFIG_DIR")); directory != "" {
		spec.Environment = map[string]string{"AGENTCLIP_CONFIG_DIR": directory}
	}
	return spec, nil
}

func systemAutostartRunner() autostart.Runner {
	return autostart.Runner{
		GOOS:     runtime.GOOS,
		MkdirAll: func(path string) error { return os.MkdirAll(path, 0o700) },
		WriteFile: func(path string, content []byte) error {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, content, 0o644)
		},
		Remove: func(path string) error {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		},
		Exists: func(path string) bool { _, err := os.Stat(path); return err == nil },
		Run: func(command autostart.Command) error {
			output, err := exec.Command(command.Name, command.Args...).CombinedOutput()
			if err != nil {
				if summary := strings.TrimSpace(string(output)); summary != "" {
					return fmt.Errorf("%w: %s", err, summary)
				}
				return err
			}
			return nil
		},
	}
}
