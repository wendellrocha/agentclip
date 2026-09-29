package remote

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/testenv"
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

func TestPlatformFromOutputMapsServersAndRefusesTheRest(t *testing.T) {
	for output, want := range map[string]string{
		"agentclip-platform=Linux x86_64\n":  "linux/amd64",
		"agentclip-platform=Linux aarch64\n": "linux/arm64",
		"agentclip-platform=Darwin arm64\n":  "darwin/arm64",
		"agentclip-platform=Darwin x86_64\n": "darwin/amd64",
		// A login shell may print a banner before the marked line.
		"Welcome to host\nLinux 6.1 is great\nagentclip-platform=Linux x86_64\n": "linux/amd64",
	} {
		goos, goarch, err := PlatformFromOutput(output)
		if err != nil || goos+"/"+goarch != want {
			t.Errorf("PlatformFromOutput(%q) = %s/%s, %v; want %s", output, goos, goarch, err, want)
		}
	}
	for _, output := range []string{"", "Linux x86_64\n", "agentclip-platform=FreeBSD amd64\n", "agentclip-platform=Linux riscv64\n", "agentclip-platform=Linux\n", "agentclip-platform=\n"} {
		if _, _, err := PlatformFromOutput(output); err == nil {
			t.Errorf("PlatformFromOutput(%q) was accepted", output)
		}
	}
}

// fakeSSH returns the home directory of a fake server: an `ssh` that runs the
// remote command locally, with only what the test installs there to find.
func fakeSSH(t *testing.T) (home string) {
	t.Helper()
	return testenv.NewFakeServer(t).Home
}

func stubFetch(t *testing.T, content string, notice string, captured *upgrader.Options) {
	t.Helper()
	previous := fetchRelease
	t.Cleanup(func() { fetchRelease = previous })
	fetchRelease = func(_ context.Context, options upgrader.Options) (upgrader.Fetched, error) {
		if captured != nil {
			*captured = options
		}
		path := filepath.Join(t.TempDir(), "agentclip")
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return upgrader.Fetched{Path: path, Notice: notice}, nil
	}
}

func TestInstallVerifiedSendsTheVerifiedBinaryWithoutRunningAScriptFromTheNetwork(t *testing.T) {
	home := fakeSSH(t)
	var options upgrader.Options
	stubFetch(t, "verified binary", "no attestation for this release", &options)
	var stderr strings.Builder
	if changed, err := InstallVerified(context.Background(), "host", "", "v0.7.2", &stderr); err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	installed := filepath.Join(home, ".local", "bin", "agentclip")
	data, err := os.ReadFile(installed)
	if err != nil || string(data) != "verified binary" {
		t.Fatalf("installed = %q, %v", data, err)
	}
	if info, _ := os.Stat(installed); info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}
	if options.Version != "v0.7.2" || options.GOOS != runtime.GOOS || options.SkipAttestation {
		t.Errorf("fetch options = %+v", options)
	}
	if !strings.Contains(stderr.String(), "no attestation for this release") {
		t.Errorf("the notice was not shown: %q", stderr.String())
	}
	leftovers, _ := filepath.Glob(filepath.Join(home, ".local", "bin", ".agentclip-new.*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

// The previous installer never downgraded a server and did nothing when the
// version was already there; setup from an older client must keep that.
func TestInstallVerifiedNeverDowngradesAndSkipsWhatIsAlreadyThere(t *testing.T) {
	for _, test := range []struct {
		name, installed string
		wantReplaced    bool
	}{
		{"older is replaced", "v0.7.1", true},
		{"same is left alone", "v0.7.2", false},
		{"newer is left alone", "v0.9.0", false},
		{"a newer pre-release is left alone", "v0.8.0-rc.1", false},
		{"unreadable output is replaced", "not a version", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := fakeSSH(t)
			installed := filepath.Join(home, ".local", "bin", "agentclip")
			if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(installed, []byte("#!/bin/sh\necho '"+test.installed+"'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			fetched := false
			stubFetch(t, "new binary", "", nil)
			inner := fetchRelease
			fetchRelease = func(ctx context.Context, options upgrader.Options) (upgrader.Fetched, error) {
				fetched = true
				return inner(ctx, options)
			}
			var stderr strings.Builder
			changed, err := InstallVerified(context.Background(), "host", "", "v0.7.2", &stderr)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(installed)
			replaced := string(data) == "new binary"
			if changed != replaced {
				t.Fatalf("reported changed = %v but the binary was replaced = %v", changed, replaced)
			}
			if replaced != test.wantReplaced || fetched != test.wantReplaced {
				t.Fatalf("replaced = %v, downloaded = %v, want %v (stderr %q)", replaced, fetched, test.wantReplaced, stderr.String())
			}
			if !test.wantReplaced && stderr.Len() == 0 {
				t.Error("leaving the server alone was not explained")
			}
		})
	}
}

func TestInstallVerifiedForwardsTheAttestationOptOutOnlyWhenExplicitlySet(t *testing.T) {
	for value, want := range map[string]bool{"1": true, "": false, "0": false, "true": false} {
		t.Run("value="+value, func(t *testing.T) {
			fakeSSH(t)
			t.Setenv(upgrader.SkipAttestationEnv, value)
			var options upgrader.Options
			stubFetch(t, "binary", "", &options)
			if _, err := InstallVerified(context.Background(), "host", "", "v0.7.2", nil); err != nil {
				t.Fatal(err)
			}
			if options.SkipAttestation != want {
				t.Fatalf("SkipAttestation = %v, want %v", options.SkipAttestation, want)
			}
		})
	}
}

// A binary that fails verification never reaches the server, and one that
// arrives damaged never replaces the working one.
func TestInstallVerifiedRefusesUnverifiedAndDamagedBinaries(t *testing.T) {
	home := fakeSSH(t)
	installed := filepath.Join(home, ".local", "bin", "agentclip")
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}

	previous := fetchRelease
	t.Cleanup(func() { fetchRelease = previous })
	fetchRelease = func(context.Context, upgrader.Options) (upgrader.Fetched, error) {
		return upgrader.Fetched{}, errors.New("release checksum does not match checksums.txt")
	}
	_, err := InstallVerified(context.Background(), "host", "", "v0.7.2", nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("an unverified binary was not refused: %v", err)
	}
	if data, _ := os.ReadFile(installed); string(data) != "previous" {
		t.Fatalf("the installed binary changed to %q", data)
	}

	// The bytes that arrive do not match the digest that was verified.
	command := uploadCommand(context.Background(), "host", "", strings.Repeat("0", 64))
	command.Stdin = strings.NewReader("truncated or tampered")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "does not match") {
		t.Fatalf("a mismatching upload was accepted: %v %s", err, output)
	}
	if data, _ := os.ReadFile(installed); string(data) != "previous" {
		t.Fatalf("a damaged upload replaced the installed binary: %q", data)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(installed), ".agentclip-new.*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestManagedSSHCommandsUseDedicatedIdentityWithoutPrompts(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "agentclip-key")
	install := uploadCommand(context.Background(), "bastion-m2", identity, strings.Repeat("0", 64))
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

func TestCheckVersionAcceptsCurrentAndRefusesOldOrUnreadableServers(t *testing.T) {
	marked := func(version string) string { return versionMarker + version + "\n" }
	for _, output := range []string{
		marked("v0.7.1"), marked("0.7.0"), marked("v0.5.0"), marked("v0.8.0-rc.1"),
		// A login shell may print anything before the marked line.
		"Welcome to host\nv0.1.0 is the kernel\n" + marked("v0.7.1"),
		marked("v0.7.1") + "trailing noise\n",
	} {
		if err := CheckVersion("host", output); err != nil {
			t.Errorf("CheckVersion(%q) = %v, want it accepted", output, err)
		}
	}
	for _, output := range []string{marked("v0.4.9"), marked("0.1.0"), marked("v0.5.0-rc.1"), "v0.1.0\n" + marked("v0.4.0")} {
		err := CheckVersion("host", output)
		if err == nil || !strings.Contains(err.Error(), "agentclip upgrade") || !strings.Contains(err.Error(), MinRemoteVersion) {
			t.Errorf("CheckVersion(%q) = %v, want a refusal that says how to update", output, err)
		}
	}
	for _, output := range []string{"", marked(""), marked("dev"), marked("agentclip: command not found"), marked("v1"), "v0.7.1\n", "Welcome\n"} {
		err := CheckVersion("host", output)
		if err == nil || !strings.Contains(err.Error(), "could not read") {
			t.Errorf("CheckVersion(%q) = %v, want it refused as unreadable", output, err)
		}
	}
}

func TestVersionForProfileRunsAgentclipVersionThroughALoginShell(t *testing.T) {
	command := VersionForProfile(companion.Profile{Destination: "bastion-m2", SSHIdentityFile: "/keys/m2"})
	arguments := strings.Join(command.Args, " ")
	for _, want := range []string{"bastion-m2", "/keys/m2", "sh -lc", ".local/bin", "agentclip version", versionMarker} {
		if !strings.Contains(arguments, want) {
			t.Errorf("version command %q lacks %q", arguments, want)
		}
	}
}

// Every SSH round trip of an install obeys the caller's context, so a server
// that stalls cannot hold an upgrade past its deadline.
func TestInstallVerifiedStopsWhenItsContextIsDone(t *testing.T) {
	home := fakeSSH(t)
	fetched := false
	stubFetch(t, "binary", "", nil)
	inner := fetchRelease
	fetchRelease = func(ctx context.Context, options upgrader.Options) (upgrader.Fetched, error) {
		fetched = true
		return inner(ctx, options)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed, err := InstallVerified(ctx, "host", "", "v0.7.2", nil)
	if err == nil || !errors.Is(err, context.Canceled) || changed {
		t.Fatalf("changed = %v, err = %v; want the cancellation reported", changed, err)
	}
	if fetched {
		t.Error("a release was downloaded although the context was already done")
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "bin", "agentclip")); err == nil {
		t.Error("something was installed although the context was already done")
	}
}
