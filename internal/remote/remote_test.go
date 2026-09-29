package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/upgrader"
)

func TestRemotePreflightPassesTheWholeCheckAsOneRemoteCommand(t *testing.T) {
	command := remotePreflightCommand("bastion-m2", "codex")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	if !strings.HasPrefix(command.Args[2], "sh -lc '") || !strings.Contains(command.Args[2], "command -v agentclip") || !strings.Contains(command.Args[2], "codex") {
		t.Fatalf("remote command = %q", command.Args[2])
	}
}

func TestRemoteSupportedAgentsChecksOnlyBuiltInHarnesses(t *testing.T) {
	command := remoteSupportedAgentsCommand("bastion-m2")
	if len(command.Args) != 3 {
		t.Fatalf("ssh arguments = %#v; expected destination plus one remote command", command.Args)
	}
	for _, required := range []string{"command -v agentclip", "codex", "claude", "gemini", "agy", "opencode", "pi", "agentclip_harness"} {
		if !strings.Contains(command.Args[2], required) {
			t.Fatalf("remote command = %q; missing %q", command.Args[2], required)
		}
	}
}

func TestRemoteSupportedAgentsScriptSucceedsWhenSomeHarnessesAreAbsent(t *testing.T) {
	if !strings.HasSuffix(remoteSupportedAgentsScript(), "; true") {
		t.Fatalf("detection script must not return a missing harness status: %q", remoteSupportedAgentsScript())
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

func TestRemoteInstallForwardsTheAttestationOptOutOnlyWhenExplicitlySet(t *testing.T) {
	for value, wantForwarded := range map[string]bool{"1": true, "": false, "0": false, "true": false, "yes": false} {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv(skipAttestationEnv, value)
			script := remoteInstallScript("v0.7.1")
			forwarded := strings.Contains(script, "| "+skipAttestationEnv+"=1 sh -s -- --version 'v0.7.1'")
			if forwarded != wantForwarded {
				t.Fatalf("forwarded = %v, want %v in %q", forwarded, wantForwarded, script)
			}
			if !wantForwarded && strings.Contains(script, skipAttestationEnv) {
				t.Fatalf("opt-out leaked into %q", script)
			}
			command := InstallCommandWithIdentity("bastion-m2", "", "v0.7.1")
			if got := strings.Contains(command.Args[len(command.Args)-1], skipAttestationEnv+"=1"); got != wantForwarded {
				t.Fatalf("ssh command forwards the opt-out = %v, want %v", got, wantForwarded)
			}
		})
	}
}

func TestSkipAttestationEnvMatchesTheUpgrader(t *testing.T) {
	if skipAttestationEnv != upgrader.SkipAttestationEnv {
		t.Fatalf("remote uses %q but the upgrader uses %q", skipAttestationEnv, upgrader.SkipAttestationEnv)
	}
}

func TestManagedSSHCommandsUseDedicatedIdentityWithoutPrompts(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "agentclip-key")
	install := InstallCommandWithIdentity("bastion-m2", identity, "v0.2.0")
	login := remoteLoginCommandWithIdentity("bastion-m2", identity, "true")
	for _, command := range []*exec.Cmd{install, login, sshKeyCheckCommand("bastion-m2", identity)} {
		arguments := strings.Join(command.Args, " ")
		for _, required := range []string{"-i " + identity, "IdentitiesOnly=yes", "BatchMode=yes", "bastion-m2"} {
			if !strings.Contains(arguments, required) {
				t.Fatalf("SSH command %q does not contain %q", arguments, required)
			}
		}
	}
}

func TestBootstrapSSHKeyCommandIsIdempotentAndLimitsPasswordPrompts(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest agentclip:dev"
	command := bootstrapSSHKeyCommand("bastion-m2", key)
	arguments := strings.Join(command.Args, " ")
	for _, required := range []string{"NumberOfPasswordPrompts=1", "grep -qxF", "authorized_keys", key} {
		if !strings.Contains(arguments, required) {
			t.Fatalf("bootstrap command %q does not contain %q", arguments, required)
		}
	}
}

func TestSSHAuthenticationFailureRecognition(t *testing.T) {
	for _, output := range []string{
		"Permission denied (publickey,password).",
		"Authentication failed.",
		"Received disconnect: Too many authentication failures",
	} {
		if !sshAuthenticationFailure([]byte(output)) {
			t.Fatalf("authentication failure %q was not recognized", output)
		}
	}
	if sshAuthenticationFailure([]byte("ssh: connect to host bastion port 22: Connection refused")) {
		t.Fatal("network failure must not trigger key bootstrap")
	}
}

func TestEnsureAgentClipSSHKeyCreatesPrivateEd25519Key(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not available")
	}
	t.Setenv("AGENTCLIP_CONFIG_DIR", t.TempDir())
	identity, publicKey, err := ensureAgentClipSSHKey("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(publicKey, "ssh-ed25519 ") {
		t.Fatalf("public key = %q", publicKey)
	}
	for _, path := range []string{identity, identity + ".pub"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("key permissions for %s = %o, want 600", path, info.Mode().Perm())
		}
	}
	if secondIdentity, secondPublicKey, err := ensureAgentClipSSHKey("dev"); err != nil || secondIdentity != identity || secondPublicKey != publicKey {
		t.Fatalf("key reuse = (%q, %q, %v), want existing key", secondIdentity, secondPublicKey, err)
	}
}
