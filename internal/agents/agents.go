// Package agents knows how to register AgentClip's MCP server with each
// supported remote coding agent (Codex, Claude Code, Gemini CLI and others).
package agents

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/remote"
)

// Adapter describes how to register AgentClip with one remote agent.
type Adapter struct {
	ID              string
	DisplayName     string
	Executable      string
	AddArguments    func(companion.Profile, string) []string
	RemoveArguments func(string) []string
}

// Configure registers AgentClip with the selected agent, or with every
// supported agent found on the server when selection is "all".
func Configure(profile companion.Profile, selection string) ([]Adapter, error) {
	if strings.HasPrefix(profile.Destination, "-") {
		return nil, errors.New("SSH destination must not start with a dash")
	}
	if strings.EqualFold(strings.TrimSpace(selection), "all") {
		return configureAll(profile)
	}
	adapter, err := Resolve(selection)
	if err != nil {
		return nil, err
	}
	if err := remote.PreflightForProfile(profile, adapter.Executable).Run(); err != nil {
		return nil, fmt.Errorf("remote preflight failed: install `agentclip` and `%s` on %s, or retry with --skip-agent: %w", adapter.Executable, profile.Destination, err)
	}
	if err := checkRemoteVersion(profile); err != nil {
		return nil, err
	}
	if err := configureOne(profile, adapter); err != nil {
		return nil, err
	}
	return []Adapter{adapter}, nil
}

// checkRemoteVersion refuses a server whose agentclip is too old for what is
// about to be configured, instead of registering an MCP entry that fails later.
func checkRemoteVersion(profile companion.Profile) error {
	output, err := remote.VersionForProfile(profile).Output()
	if err != nil {
		return fmt.Errorf("read the agentclip version on %s: %w", profile.Destination, err)
	}
	return remote.CheckVersion(profile.Destination, string(output))
}

func configureAll(profile companion.Profile) ([]Adapter, error) {
	output, err := remote.SupportedAgentsForProfile(profile).Output()
	if err != nil {
		return nil, fmt.Errorf("remote preflight failed: install `agentclip` on %s, or retry with --skip-agent: %w", profile.Destination, err)
	}
	if err := checkRemoteVersion(profile); err != nil {
		return nil, err
	}
	var configured []Adapter
	for _, agentID := range strings.Fields(string(output)) {
		adapter, err := Resolve(agentID)
		if err != nil {
			return nil, fmt.Errorf("read supported harnesses on %s: %w", profile.Destination, err)
		}
		if err := configureOne(profile, adapter); err != nil {
			return nil, err
		}
		configured = append(configured, adapter)
	}
	if len(configured) == 0 {
		return nil, fmt.Errorf("no supported harness is installed on %s; supported harnesses: codex, claude, gemini, agy, opencode, pi", profile.Destination)
	}
	return configured, nil
}

func configureOne(profile companion.Profile, adapter Adapter) error {
	name := "agentclip-" + profile.Name
	// Re-pairing deliberately replaces only AgentClip's own named MCP entry.
	_ = remote.LoginForProfile(profile, adapter.RemoveArguments(name)...).Run()
	if err := remote.LoginForProfile(profile, adapter.AddArguments(profile, name)...).Run(); err != nil {
		return fmt.Errorf("configure %s MCP on %s: %w", adapter.DisplayName, profile.Destination, err)
	}
	return nil
}

// Resolve returns the adapter for a supported agent identifier.
func Resolve(agentID string) (Adapter, error) {
	switch strings.ToLower(strings.TrimSpace(agentID)) {
	case "codex":
		return Adapter{
			ID: "codex", DisplayName: "Codex", Executable: "codex",
			RemoveArguments: func(name string) []string {
				return []string{"codex", "mcp", "remove", name}
			},
			AddArguments: func(profile companion.Profile, name string) []string {
				return append(agentEnvironmentArguments([]string{"codex", "mcp", "add", name}, profile), "--", "agentclip", "mcp")
			},
		}, nil
	case "claude", "claude-code":
		return Adapter{
			ID: "claude", DisplayName: "Claude Code", Executable: "claude",
			RemoveArguments: func(name string) []string {
				return []string{"claude", "mcp", "remove", "--scope", "user", name}
			},
			AddArguments: func(profile companion.Profile, name string) []string {
				arguments := []string{"claude", "mcp", "add", name, "--scope", "user"}
				return append(agentEnvironmentArguments(arguments, profile), "--", "agentclip", "mcp")
			},
		}, nil
	case "gemini", "gemini-cli":
		return Adapter{
			ID: "gemini", DisplayName: "Gemini CLI", Executable: "gemini",
			RemoveArguments: func(name string) []string {
				return []string{"gemini", "mcp", "remove", "--scope", "user", name}
			},
			AddArguments: func(profile companion.Profile, name string) []string {
				arguments := []string{"gemini", "mcp", "add", name, "agentclip", "mcp", "--scope", "user"}
				return agentEnvironmentArguments(arguments, profile)
			},
		}, nil
	case "agy", "antigravity", "antigravity-cli":
		return Adapter{
			ID: "agy", DisplayName: "AGY / Antigravity CLI", Executable: "agy",
			RemoveArguments: func(name string) []string {
				return []string{"agentclip", "harness", "remove", "agy", "--name", name}
			},
			AddArguments: func(profile companion.Profile, name string) []string {
				return harnessInstallArguments("agy", profile, name)
			},
		}, nil
	case "opencode":
		return Adapter{
			ID: "opencode", DisplayName: "OpenCode", Executable: "opencode",
			RemoveArguments: func(name string) []string {
				return []string{"agentclip", "harness", "remove", "opencode", "--name", name}
			},
			AddArguments: func(profile companion.Profile, name string) []string {
				return harnessInstallArguments("opencode", profile, name)
			},
		}, nil
	case "pi", "pi-coding-agent":
		return Adapter{
			ID: "pi", DisplayName: "Pi Coding Agent", Executable: "pi",
			RemoveArguments: func(name string) []string {
				return []string{"agentclip", "harness", "remove", "pi", "--name", name}
			},
			AddArguments: func(profile companion.Profile, name string) []string {
				return harnessInstallArguments("pi", profile, name)
			},
		}, nil
	default:
		return Adapter{}, fmt.Errorf("unsupported agent %q; supported agents: codex, claude, gemini, agy, opencode, pi", agentID)
	}
}

func harnessInstallArguments(harness string, profile companion.Profile, name string) []string {
	arguments := []string{"agentclip", "harness", "install", harness, "--name", name, "--port", strconv.Itoa(profile.RemotePort), "--token", profile.Token}
	if profile.HasUploadToken() {
		arguments = append(arguments, "--upload-token", profile.UploadToken)
	}
	return arguments
}

func agentEnvironmentArguments(arguments []string, profile companion.Profile) []string {
	arguments = append(arguments,
		"--env", fmt.Sprintf("AGENTCLIP_BRIDGE_PORT=%d", profile.RemotePort),
		"--env", "AGENTCLIP_SESSION_TOKEN="+profile.Token,
	)
	if profile.HasUploadToken() {
		arguments = append(arguments, "--env", "AGENTCLIP_UPLOAD_TOKEN="+profile.UploadToken)
	}
	return arguments
}

// Display joins adapter names for user-facing messages.
func Display(adapters []Adapter) string {
	names := make([]string, len(adapters))
	for index, adapter := range adapters {
		names[index] = adapter.DisplayName
	}
	return strings.Join(names, ", ")
}
