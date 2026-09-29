// Package remote builds and runs the SSH commands AgentClip uses to prepare a
// server: preflight checks, installation and the dedicated SSH identity.
package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/release"
	"github.com/wendellrocha/agentclip/internal/upgrader"
)

func remotePreflightCommand(destination, agentExecutable string) *exec.Cmd {
	return remotePreflightCommandWithIdentity(destination, "", agentExecutable)
}

func PreflightForProfile(profile companion.Profile, agentExecutable string) *exec.Cmd {
	return remotePreflightCommandWithIdentity(profile.Destination, profile.SSHIdentityFile, agentExecutable)
}

func remotePreflightCommandWithIdentity(destination, identityFile, agentExecutable string) *exec.Cmd {
	// ssh combines all arguments after the destination into a remote shell
	// command. Use a login shell as well: remote AgentClip and Codex are often
	// installed through ~/.profile or Volta, neither of which a plain SSH
	// command is required to load.
	check := "export PATH=\"$HOME/.local/bin:$PATH\"; command -v agentclip >/dev/null && command -v " + shellQuote(agentExecutable) + " >/dev/null"
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(check))
}

// MinRemoteVersion is the oldest remote agentclip that understands what setup
// configures: the upload token for files sent from the server arrived in v0.5.0.
// An older one registers the MCP entry and then fails on the tools, silently.
const MinRemoteVersion = "v0.5.0"

// VersionForProfile asks the server which agentclip it would run.
func VersionForProfile(profile companion.Profile) *exec.Cmd {
	return versionCommand(profile.Destination, profile.SSHIdentityFile)
}

func versionCommand(destination, identityFile string) *exec.Cmd {
	// A login shell may print a banner or a warning first, so the version is
	// marked and CheckVersion reads only the marked line.
	script := "export PATH=\"$HOME/.local/bin:$PATH\"; printf '" + versionMarker + "%s\\n' \"$(agentclip version 2>/dev/null)\""
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(script))
}

// versionMarker prefixes the line that carries the server's version.
const versionMarker = "agentclip-version="

// CheckVersion decides whether the output of VersionForProfile from a server is
// new enough. It reports what to do when it is not, and refuses output it cannot
// read instead of assuming the server is fine.
func CheckVersion(destination, output string) error {
	version := ""
	for _, line := range strings.Split(output, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), versionMarker); found {
			version = strings.TrimSpace(value)
		}
	}
	comparison, err := release.Compare(version, MinRemoteVersion)
	if err != nil {
		return fmt.Errorf("could not read the agentclip version on %s (got %q); install or update it with `agentclip setup %s`", destination, version, destination)
	}
	if comparison < 0 {
		return fmt.Errorf("agentclip %s on %s is older than %s, the oldest that supports this setup; update it with `agentclip upgrade` or `agentclip setup %s`", version, destination, MinRemoteVersion, destination)
	}
	return nil
}

func remoteSupportedAgentsCommand(destination string) *exec.Cmd {
	return remoteSupportedAgentsCommandWithIdentity(destination, "")
}

func SupportedAgentsForProfile(profile companion.Profile) *exec.Cmd {
	return remoteSupportedAgentsCommandWithIdentity(profile.Destination, profile.SSHIdentityFile)
}

func remoteSupportedAgentsCommandWithIdentity(destination, identityFile string) *exec.Cmd {
	// This deliberately detects only built-in adapters. It neither installs
	// harnesses nor scans project-level configuration files.
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(remoteSupportedAgentsScript()))
}

func remoteSupportedAgentsScript() string {
	return "export PATH=\"$HOME/.local/bin:$PATH\"; command -v agentclip >/dev/null || exit 10; for agentclip_harness in codex claude gemini agy opencode pi; do command -v \"$agentclip_harness\" >/dev/null && printf '%s\\n' \"$agentclip_harness\"; done; true"
}

func remoteLoginCommand(destination string, arguments ...string) *exec.Cmd {
	return remoteLoginCommandWithIdentity(destination, "", arguments...)
}

func LoginForProfile(profile companion.Profile, arguments ...string) *exec.Cmd {
	return remoteLoginCommandWithIdentity(profile.Destination, profile.SSHIdentityFile, arguments...)
}

func remoteLoginCommandWithIdentity(destination, identityFile string, arguments ...string) *exec.Cmd {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = shellQuote(argument)
	}
	script := "export PATH=\"$HOME/.local/bin:$PATH\"; " + strings.Join(quoted, " ")
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(script))
}

func remoteSSHCommand(destination, identityFile, remoteCommand string) *exec.Cmd {
	arguments := []string{}
	if identityFile != "" {
		arguments = append(arguments, "-i", identityFile, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes")
	}
	arguments = append(arguments, destination, remoteCommand)
	return exec.Command("ssh", arguments...)
}

// ensureSetupSSHIdentity preserves an already-working SSH key setup. When the
// destination rejects non-interactive key authentication, it creates a
// dedicated AgentClip key and installs its public half through one interactive
// password login before the rest of setup runs.
func EnsureSetupSSHIdentity(destination, profileName, existingIdentityFile string) (string, error) {
	output, err := sshKeyCheckCommand(destination, existingIdentityFile).CombinedOutput()
	if err == nil {
		return existingIdentityFile, nil
	}
	if !sshAuthenticationFailure(output) {
		return "", fmt.Errorf("check SSH key authentication for %s: %s", destination, sshErrorSummary(output, err))
	}
	identityFile, publicKey, err := ensureAgentClipSSHKey(profileName)
	if err != nil {
		return "", err
	}
	fmt.Printf("Configuring a dedicated AgentClip SSH key for %s (your password may be requested once)...\n", destination)
	bootstrap := bootstrapSSHKeyCommand(destination, publicKey)
	bootstrap.Stdin, bootstrap.Stdout, bootstrap.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := bootstrap.Run(); err != nil {
		return "", fmt.Errorf("install AgentClip SSH key on %s: %w", destination, err)
	}
	output, err = sshKeyCheckCommand(destination, identityFile).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("verify AgentClip SSH key on %s: %s", destination, sshErrorSummary(output, err))
	}
	return identityFile, nil
}

func sshKeyCheckCommand(destination, identityFile string) *exec.Cmd {
	arguments := []string{"-o", "BatchMode=yes"}
	if identityFile != "" {
		arguments = append(arguments, "-i", identityFile, "-o", "IdentitiesOnly=yes")
	}
	arguments = append(arguments, destination, "true")
	return exec.Command("ssh", arguments...)
}

func sshAuthenticationFailure(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "permission denied") ||
		strings.Contains(message, "authentication failed") ||
		strings.Contains(message, "too many authentication failures") ||
		strings.Contains(message, "no supported authentication methods")
}

func sshErrorSummary(output []byte, err error) string {
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	if len(message) > 512 {
		message = message[:512] + "…"
	}
	return message
}

func ensureAgentClipSSHKey(profileName string) (string, string, error) {
	identityFile, err := companion.SSHIdentityPath(profileName)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(identityFile), 0700); err != nil {
		return "", "", fmt.Errorf("create AgentClip SSH key directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(identityFile), 0700); err != nil {
		return "", "", fmt.Errorf("secure AgentClip SSH key directory: %w", err)
	}
	publicFile := identityFile + ".pub"
	privateInfo, privateErr := os.Lstat(identityFile)
	publicInfo, publicErr := os.Lstat(publicFile)
	if privateErr == nil && (!privateInfo.Mode().IsRegular() || privateInfo.Mode()&os.ModeSymlink != 0) {
		return "", "", errors.New("AgentClip SSH private key is not a regular file")
	}
	if publicErr == nil && (!publicInfo.Mode().IsRegular() || publicInfo.Mode()&os.ModeSymlink != 0) {
		return "", "", errors.New("AgentClip SSH public key is not a regular file")
	}
	if privateErr != nil && !os.IsNotExist(privateErr) {
		return "", "", fmt.Errorf("inspect AgentClip SSH private key: %w", privateErr)
	}
	if publicErr != nil && !os.IsNotExist(publicErr) {
		return "", "", fmt.Errorf("inspect AgentClip SSH public key: %w", publicErr)
	}
	if os.IsNotExist(privateErr) && !os.IsNotExist(publicErr) {
		return "", "", errors.New("AgentClip SSH public key exists without its private key")
	}
	if os.IsNotExist(privateErr) {
		command := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "agentclip:"+profileName, "-f", identityFile)
		if output, err := command.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("generate AgentClip SSH key (requires ssh-keygen): %s", sshErrorSummary(output, err))
		}
	} else if os.IsNotExist(publicErr) {
		output, err := exec.Command("ssh-keygen", "-y", "-f", identityFile).Output()
		if err != nil {
			return "", "", fmt.Errorf("derive AgentClip SSH public key: %w", err)
		}
		if err := os.WriteFile(publicFile, output, 0600); err != nil {
			return "", "", fmt.Errorf("write AgentClip SSH public key: %w", err)
		}
	}
	if err := os.Chmod(identityFile, 0600); err != nil {
		return "", "", fmt.Errorf("secure AgentClip SSH private key: %w", err)
	}
	if err := os.Chmod(publicFile, 0600); err != nil {
		return "", "", fmt.Errorf("secure AgentClip SSH public key: %w", err)
	}
	publicKey, err := readAgentClipSSHPublicKey(publicFile)
	if err != nil {
		return "", "", err
	}
	return identityFile, publicKey, nil
}

func readAgentClipSSHPublicKey(path string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read AgentClip SSH public key: %w", err)
	}
	key := strings.TrimSpace(string(payload))
	if strings.ContainsAny(key, "\r\n") || !strings.HasPrefix(key, "ssh-ed25519 ") || len(strings.Fields(key)) < 2 {
		return "", errors.New("invalid AgentClip SSH public key")
	}
	return key, nil
}

func bootstrapSSHKeyCommand(destination, publicKey string) *exec.Cmd {
	script := strings.Join([]string{
		"set -eu",
		"umask 077",
		"mkdir -p \"$HOME/.ssh\"",
		"touch \"$HOME/.ssh/authorized_keys\"",
		"grep -qxF " + shellQuote(publicKey) + " \"$HOME/.ssh/authorized_keys\" || printf '%s\\n' " + shellQuote(publicKey) + " >> \"$HOME/.ssh/authorized_keys\"",
	}, "; ")
	return exec.Command("ssh", "-o", "NumberOfPasswordPrompts=1", destination, "sh -lc "+shellQuote(script))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// platformMarker prefixes the line that carries the server's `uname -sm`.
const platformMarker = "agentclip-platform="

func platformCommand(destination, identityFile string) *exec.Cmd {
	script := "printf '" + platformMarker + "%s\\n' \"$(uname -sm)\""
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(script))
}

// PlatformFromOutput reads the release platform out of platformCommand's output.
// Only Linux and macOS servers are supported, as the installers always were.
func PlatformFromOutput(output string) (goos, goarch string, err error) {
	var system, machine string
	for _, line := range strings.Split(output, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), platformMarker); found {
			fields := strings.Fields(value)
			if len(fields) == 2 {
				system, machine = fields[0], fields[1]
			}
		}
	}
	switch system {
	case "Linux":
		goos = "linux"
	case "Darwin":
		goos = "darwin"
	default:
		return "", "", fmt.Errorf("unsupported server operating system %q; AgentClip installs on Linux and macOS servers", system)
	}
	switch machine {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported server CPU architecture %q", machine)
	}
	return goos, goarch, nil
}

// uploadCommand receives an executable on stdin and installs it as
// ~/.local/bin/agentclip, but only if it hashes to digest. A dropped
// connection then leaves the previous binary in place instead of a truncated one.
func uploadCommand(destination, identityFile, digest string) *exec.Cmd {
	script := strings.Join([]string{
		"set -eu",
		"umask 077",
		"dir=\"$HOME/.local/bin\"",
		"mkdir -p \"$dir\"",
		"tmp=\"$dir/.agentclip-new.$$\"",
		"trap 'rm -f \"$tmp\"' EXIT",
		"cat > \"$tmp\"",
		"if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum \"$tmp\" | cut -d' ' -f1); else actual=$(shasum -a 256 \"$tmp\" | cut -d' ' -f1); fi",
		"[ \"$actual\" = " + shellQuote(digest) + " ] || { echo 'uploaded binary does not match the verified checksum' >&2; exit 1; }",
		"chmod 755 \"$tmp\"",
		"mv -f \"$tmp\" \"$dir/agentclip\"",
	}, "; ")
	return remoteSSHCommand(destination, identityFile, "sh -c "+shellQuote(script))
}

// fetchRelease is upgrader.Fetch, replaceable in tests.
var fetchRelease = upgrader.Fetch

// installedVersion reads the version of the agentclip already on the server, or
// "" when there is none or its output cannot be read as a version.
func installedVersion(destination, identityFile string) string {
	output, err := versionCommand(destination, identityFile).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), versionMarker); found {
			if _, err := release.Compare(strings.TrimSpace(value), "v0.0.0"); err == nil {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

// InstallVerified puts the release binary for tag on the server without
// downloading an installer script to it. This machine downloads the
// binary for the server's platform and verifies it (checksum, and the build
// attestation for releases that have one), then sends it over the existing SSH
// connection. The server runs only a short fixed command sequence: it confirms
// the bytes it received are the ones verified, then moves them into place.
// It reports whether it changed the server: a server that already has this
// version, or a newer one, is left alone and reported as unchanged.
// Notices about checks that did not apply go to stderr.
func InstallVerified(ctx context.Context, destination, identityFile, tag string, stderr io.Writer) (bool, error) {
	if installed := installedVersion(destination, identityFile); installed != "" {
		if comparison, err := release.Compare(tag, installed); err == nil && comparison <= 0 {
			if stderr != nil {
				if comparison == 0 {
					fmt.Fprintf(stderr, "AgentClip %s is already installed on %s.\n", installed, destination)
				} else {
					fmt.Fprintf(stderr, "%s has AgentClip %s, newer than %s; leaving it unchanged.\n", destination, installed, tag)
				}
			}
			return false, nil
		}
	}
	output, err := platformCommand(destination, identityFile).Output()
	if err != nil {
		return false, fmt.Errorf("read the platform of %s: %w", destination, err)
	}
	goos, goarch, err := PlatformFromOutput(string(output))
	if err != nil {
		return false, fmt.Errorf("%s: %w", destination, err)
	}
	fetched, err := fetchRelease(ctx, upgrader.Options{Version: tag, GOOS: goos, GOARCH: goarch, SkipAttestation: os.Getenv(upgrader.SkipAttestationEnv) == "1"})
	if err != nil {
		return false, fmt.Errorf("verify AgentClip %s for %s/%s: %w", tag, goos, goarch, err)
	}
	defer fetched.Cleanup()
	if fetched.Notice != "" && stderr != nil {
		fmt.Fprintln(stderr, fetched.Notice)
	}
	binary, err := os.Open(fetched.Path)
	if err != nil {
		return false, err
	}
	defer binary.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, binary); err != nil {
		return false, err
	}
	if _, err := binary.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	upload := uploadCommand(destination, identityFile, hex.EncodeToString(hash.Sum(nil)))
	upload.Stdin = binary
	if combined, err := upload.CombinedOutput(); err != nil {
		return false, fmt.Errorf("install AgentClip on %s: %w: %s", destination, err, strings.TrimSpace(string(combined)))
	}
	return true, nil
}
