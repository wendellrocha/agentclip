// Package selfupgrade orchestrates `agentclip upgrade`: it updates every
// configured server, then replaces the running executable and restarts the
// Companions that were active. The effects (SSH, processes, the filesystem) are
// injected, so every ordering and recovery path can be tested without them.
package selfupgrade

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/release"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

// Runner holds the steps of an upgrade. Every field is required except the
// writers, which default to discarding output.
type Runner struct {
	// GOOS selects the replacement strategy: Windows cannot overwrite a running
	// executable, so it hands the swap to a helper process.
	GOOS   string
	Stdout io.Writer
	Stderr io.Writer
	// CurrentVersion is the running executable's version. When it is already the
	// latest release (or newer), this machine is left alone and its Companions
	// are not restarted. Empty means unknown, and the executable is replaced.
	CurrentVersion string

	LatestTag  func(ctx context.Context) (string, error)
	Executable func() (string, error)
	// Prepare downloads, verifies and stages the release next to the executable.
	Prepare          func(ctx context.Context, tag, executable string) (upgrader.StagedBinary, error)
	Profiles         func() ([]companion.Profile, error)
	ActiveCompanions func(profiles []companion.Profile) ([]string, error)
	// InstallRemote reports whether it changed the server: false means the server
	// already had this version, or a newer one.
	InstallRemote     func(profile companion.Profile, tag string) (bool, error)
	StopCompanions    func(names []string) error
	RestartCompanions func(names []string) error
	// Replace atomically moves the staged binary over the executable.
	Replace                  func(from, to string) error
	LaunchWindowsReplacement func(staged upgrader.StagedBinary, active []string) error
}

// RemoteResult is the outcome of updating one saved server.
type RemoteResult struct {
	Profile string
	// Updated is false when the server already had the version.
	Updated bool
	Err     error
}

// Run performs the upgrade. A failure before the local executable is replaced
// removes the staged binary and restarts the Companions that were stopped, so
// the machine is left as it was found.
func (r Runner) Run(ctx context.Context) error {
	stdout, stderr := r.Stdout, r.Stderr
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	tag, err := r.LatestTag(ctx)
	if err != nil {
		return fmt.Errorf("resolve latest AgentClip release: %w", err)
	}
	executable, err := r.Executable()
	if err != nil {
		return fmt.Errorf("locate running AgentClip executable: %w", err)
	}
	if r.localIsCurrent(tag) {
		return r.updateServersOnly(ctx, tag, stdout)
	}
	staged, err := r.Prepare(ctx, tag, executable)
	if err != nil {
		return fmt.Errorf("prepare AgentClip %s: %w", tag, err)
	}
	if staged.Notice != "" {
		fmt.Fprintln(stderr, "warning:", staged.Notice)
	}
	profiles, err := r.Profiles()
	if err != nil {
		staged.Cleanup()
		return err
	}
	// Capture this before remote work starts: only Companions that were healthy
	// when the upgrade began are eligible for a later restart.
	active, err := r.ActiveCompanions(profiles)
	if err != nil {
		staged.Cleanup()
		return err
	}
	results := make([]RemoteResult, 0, len(profiles))
	for _, profile := range profiles {
		fmt.Fprintf(stdout, "Updating %q on %s...\n", profile.Name, profile.Destination)
		updated, err := r.InstallRemote(profile, tag)
		results = append(results, RemoteResult{Profile: profile.Name, Updated: updated, Err: err})
	}
	if err := r.StopCompanions(active); err != nil {
		staged.Cleanup()
		_ = r.RestartCompanions(active)
		return err
	}
	if r.GOOS == "windows" {
		if err := r.LaunchWindowsReplacement(staged, active); err != nil {
			staged.Cleanup()
			_ = r.RestartCompanions(active)
			return err
		}
		PrintSummary(stdout, results)
		if HasFailure(results) {
			return errors.New("one or more remote hosts could not be updated; the local update will finish shortly")
		}
		fmt.Fprintf(stdout, "AgentClip %s will replace itself and restart %d Companion(s) shortly.\n", tag, len(active))
		return nil
	}
	if err := r.Replace(staged.Path, staged.Target); err != nil {
		staged.Cleanup()
		_ = r.RestartCompanions(active)
		return fmt.Errorf("replace AgentClip executable: %w", err)
	}
	if err := r.RestartCompanions(active); err != nil {
		PrintSummary(stdout, results)
		return err
	}
	PrintSummary(stdout, results)
	if HasFailure(results) {
		return errors.New("one or more remote hosts could not be updated")
	}
	fmt.Fprintf(stdout, "AgentClip updated to %s.\n", tag)
	return nil
}

func (r Runner) localIsCurrent(tag string) bool {
	if r.CurrentVersion == "" {
		return false
	}
	comparison, err := release.Compare(tag, r.CurrentVersion)
	return err == nil && comparison <= 0
}

// updateServersOnly is the upgrade of a machine that already runs the latest
// release: the servers may still lag, but there is nothing to download, replace
// or restart here.
func (r Runner) updateServersOnly(_ context.Context, tag string, stdout io.Writer) error {
	profiles, err := r.Profiles()
	if err != nil {
		return err
	}
	results := make([]RemoteResult, 0, len(profiles))
	for _, profile := range profiles {
		fmt.Fprintf(stdout, "Updating %q on %s...\n", profile.Name, profile.Destination)
		updated, err := r.InstallRemote(profile, tag)
		results = append(results, RemoteResult{Profile: profile.Name, Updated: updated, Err: err})
	}
	PrintSummary(stdout, results)
	if HasFailure(results) {
		return errors.New("one or more remote hosts could not be updated")
	}
	fmt.Fprintf(stdout, "AgentClip is already up to date (%s).\n", r.CurrentVersion)
	return nil
}

// PrintSummary lists how each saved server fared.
func PrintSummary(w io.Writer, results []RemoteResult) {
	if len(results) == 0 {
		fmt.Fprintln(w, "No remote profiles configured.")
		return
	}
	fmt.Fprintln(w, "Remote update summary:")
	for _, result := range results {
		switch {
		case result.Err == nil && result.Updated:
			fmt.Fprintf(w, "  %s: updated\n", result.Profile)
		case result.Err == nil:
			fmt.Fprintf(w, "  %s: already up to date\n", result.Profile)
		default:
			fmt.Fprintf(w, "  %s: failed (%v)\n", result.Profile, result.Err)
		}
	}
}

// HasFailure reports whether any saved server could not be updated.
func HasFailure(results []RemoteResult) bool {
	for _, result := range results {
		if result.Err != nil {
			return true
		}
	}
	return false
}
