// Package setup implements `agentclip setup`: it prepares a server end to end by
// installing AgentClip there, pairing a profile, registering it with the
// server's agents and starting the local Companion. The effects (SSH, the
// profile store, processes) are injected, so every branch can be tested without
// them.
package setup

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/wendellrocha/agentclip/internal/buildinfo"
	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/i18n"
)

// Usage is the message shown when the arguments are missing.
const Usage = "usage: agentclip setup <ssh-destination> [--profile NAME] [--agent all|codex|claude|gemini|agy|opencode|pi] [--version vX.Y.Z] [--remote-port 39123] [--skip-agent] [--skip-install] [--no-start]"

var releaseTagPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+([-.][0-9A-Za-z.-]+)?$`)

// Options are the parsed `setup` arguments.
type Options struct {
	Destination    string
	Profile        string
	Agent          string
	ReleaseVersion string
	RemotePort     int
	SkipAgent      bool
	SkipInstall    bool
	NoStart        bool
}

// ParseArgs parses the arguments that follow `agentclip setup`.
func ParseArgs(arguments []string) (Options, error) {
	if len(arguments) < 1 {
		return Options{}, errors.New(Usage)
	}
	if strings.HasPrefix(arguments[0], "-") {
		return Options{}, errors.New("SSH destination must not start with a dash")
	}
	settings := flag.NewFlagSet("setup", flag.ContinueOnError)
	settings.SetOutput(io.Discard)
	options := Options{Destination: arguments[0]}
	settings.StringVar(&options.Profile, "profile", "", "local Companion profile name")
	settings.StringVar(&options.Agent, "agent", "all", "remote agent: all, codex, claude, gemini, agy, opencode, or pi")
	settings.StringVar(&options.ReleaseVersion, "version", "", "AgentClip release tag to install remotely")
	settings.IntVar(&options.RemotePort, "remote-port", 39123, "remote loopback port")
	settings.BoolVar(&options.SkipAgent, "skip-agent", false, "do not configure an agent on the server")
	skipCodex := settings.Bool("skip-codex", false, "deprecated alias for --skip-agent")
	settings.BoolVar(&options.SkipInstall, "skip-install", false, "do not install AgentClip on the server")
	settings.BoolVar(&options.NoStart, "no-start", false, "do not start the local Companion")
	if err := settings.Parse(arguments[1:]); err != nil {
		return Options{}, fmt.Errorf("parse setup options: %w", err)
	}
	if settings.NArg() != 0 {
		return Options{}, fmt.Errorf("unexpected setup arguments: %s", strings.Join(settings.Args(), " "))
	}
	options.SkipAgent = options.SkipAgent || *skipCodex
	if options.Profile == "" {
		options.Profile = DefaultProfileName(options.Destination)
	}
	return options, nil
}

// DefaultProfileName derives a valid profile name from an SSH destination.
func DefaultProfileName(destination string) string {
	name := destination
	if index := strings.LastIndex(name, "@"); index >= 0 {
		name = name[index+1:]
	}
	var builder strings.Builder
	for _, character := range name {
		valid := (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_'
		if valid {
			builder.WriteRune(character)
		} else if builder.Len() == 0 || !strings.HasSuffix(builder.String(), "-") {
			builder.WriteByte('-')
		}
	}
	name = strings.Trim(builder.String(), "-_")
	if name == "" {
		name = "server"
	}
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// ReleaseTag resolves the release to install remotely: the requested one, or the
// version of this build.
func ReleaseTag(requested string) (string, error) {
	tag := strings.TrimSpace(requested)
	if tag == "" {
		tag = buildinfo.Version
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if strings.Contains(tag, "-dev") {
		return "", errors.New("the development build has no downloadable release; pass --version vX.Y.Z after publishing it, or use `pair` for a manual development setup")
	}
	if !releaseTagPattern.MatchString(tag) {
		return "", fmt.Errorf("release version must be a semantic tag such as v0.2.0, got %q", tag)
	}
	return tag, nil
}

// Runner holds the steps of a setup. Every field is required except Stdout,
// which defaults to discarding output.
type Runner struct {
	Stdout io.Writer

	// LoadProfile reads a saved profile, to reuse its SSH identity.
	LoadProfile func(name string) (companion.Profile, error)
	// EnsureIdentity returns the SSH key to use for the destination, creating
	// and installing a dedicated one when the server still needs a password.
	EnsureIdentity func(destination, profileName, existingIdentityFile string) (string, error)
	ReleaseTag     func(requested string) (string, error)
	Install        func(destination, identityFile, tag string) error
	// CompanionRunning reports whether a Companion for the profile is healthy.
	CompanionRunning func(name string) bool
	StopCompanion    func(name string) error
	Pair             func(name, destination string, remotePort int, agent string, skipAgent bool, identityFile string) (companion.Profile, error)
	StartCompanion   func(name string) error
}

// Run performs the setup.
func (r Runner) Run(options Options) error {
	stdout := r.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	existingIdentityFile := ""
	if existing, err := r.LoadProfile(options.Profile); err == nil {
		existingIdentityFile = existing.SSHIdentityFile
	}
	identityFile, err := r.EnsureIdentity(options.Destination, options.Profile, existingIdentityFile)
	if err != nil {
		return err
	}
	if !options.SkipInstall {
		tag, err := r.ReleaseTag(options.ReleaseVersion)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, i18n.T("Installing AgentClip %s on %s...", tag, options.Destination))
		if err := r.Install(options.Destination, identityFile, tag); err != nil {
			return fmt.Errorf("install AgentClip on %s: %w", options.Destination, err)
		}
	}

	// A running Companion would retain its old pairing token after re-setup.
	if r.CompanionRunning(options.Profile) {
		if err := r.StopCompanion(options.Profile); err != nil {
			return err
		}
	}
	profile, err := r.Pair(options.Profile, options.Destination, options.RemotePort, options.Agent, options.SkipAgent, identityFile)
	if err != nil {
		return err
	}
	if options.NoStart {
		fmt.Fprintln(stdout, i18n.T("Setup complete for %q. Start it with: agentclip companion start %s", profile.Name, profile.Name))
		return nil
	}
	if err := r.StartCompanion(profile.Name); err != nil {
		return err
	}
	fmt.Fprintln(stdout, i18n.T("Setup complete for %q. SSH normally, then ask your agent to inspect the clipboard.", profile.Name))
	return nil
}
