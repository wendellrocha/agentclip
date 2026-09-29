// Package remote builds and runs the SSH commands AgentClip uses to prepare a
// server: preflight checks, installation and the dedicated SSH identity.
package remote

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wendellrocha/agentclip/internal/companion"
)

const (
	releaseRepository = "wendellrocha/agentclip"
	// skipAttestationEnv opts the installer out of the build attestation
	// check. It mirrors upgrader.SkipAttestationEnv.
	skipAttestationEnv = "AGENTCLIP_SKIP_ATTESTATION"
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

func remoteInstallCommand(destination, tag string) *exec.Cmd {
	return InstallCommandWithIdentity(destination, "", tag)
}

func InstallCommandWithIdentity(destination, identityFile, tag string) *exec.Cmd {
	return remoteSSHCommand(destination, identityFile, "sh -lc "+shellQuote(remoteInstallScript(tag)))
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

func remoteInstallScript(tag string) string {
	// The installer detects the remote OS and architecture, verifies the release
	// checksum, and installs only into the remote user's home directory.
	installerURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/scripts/install.sh", releaseRepository, tag)
	// The attestation opt-out is command-wide: without forwarding it, an
	// upgrade run with it set would update this machine and then fail on every
	// server while the GitHub API is unavailable.
	shell := "sh"
	if os.Getenv(skipAttestationEnv) == "1" {
		shell = skipAttestationEnv + "=1 sh"
	}
	return strings.Join([]string{
		"set -eu",
		"curl -fsSL --retry 3 " + shellQuote(installerURL) + " | " + shell + " -s -- --version " + shellQuote(tag),
	}, "; ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
