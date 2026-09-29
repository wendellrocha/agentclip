package upgrader

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// attestationBody builds a GitHub attestations API response holding one SLSA
// provenance statement for digest, made by the given workflow run.
func attestationBody(t *testing.T, digest, repository, workflowPath, ref string) []byte {
	t.Helper()
	statement := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"predicateType": "https://slsa.dev/provenance/v1",
		"subject":       []any{map[string]any{"name": "other-file", "digest": map[string]string{"sha256": strings.Repeat("0", 64)}}, map[string]any{"name": "archive", "digest": map[string]string{"sha256": digest}}},
		"predicate": map[string]any{"buildDefinition": map[string]any{"externalParameters": map[string]any{
			"workflow": map[string]string{"ref": ref, "repository": "https://github.com/" + repository, "path": workflowPath},
		}}},
	}
	raw, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"attestations": []any{map[string]any{"bundle": map[string]any{"dsseEnvelope": map[string]string{"payload": base64.StdEncoding.EncodeToString(raw)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

type releaseFixture struct {
	version     string
	attestation func(t *testing.T, digest string) (status int, body []byte)
	apiRequests int
}

func (f *releaseFixture) prepare(t *testing.T, options Options) (StagedBinary, error) {
	t.Helper()
	asset := "agentclip_" + f.version + "_linux_amd64.tar.gz"
	archive := tarFixture(t, "agentclip_"+f.version+"_linux_amd64/agentclip", []byte("new executable"))
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case filepath.Base(request.URL.Path) == asset:
			_, _ = w.Write(archive)
		case filepath.Base(request.URL.Path) == "checksums.txt":
			_, _ = w.Write([]byte(digest + "  " + asset + "\n"))
		case strings.Contains(request.URL.Path, "/attestations/sha256:"):
			f.apiRequests++
			if got := strings.TrimPrefix(filepath.Base(request.URL.Path), "sha256:"); got != digest {
				t.Errorf("API was asked about %s, want the archive digest %s", got, digest)
			}
			status, body := f.attestation(t, digest)
			w.WriteHeader(status)
			_, _ = w.Write(body)
		default:
			http.NotFound(w, request)
		}
	}))
	t.Cleanup(server.Close)
	options.Version, options.GOOS, options.GOARCH = f.version, "linux", "amd64"
	options.Executable = filepath.Join(t.TempDir(), "agentclip")
	options.Repository = "example/agentclip"
	options.Client = rewriteClient(server)
	staged, err := Prepare(context.Background(), options)
	if err == nil {
		t.Cleanup(staged.Cleanup)
	}
	return staged, err
}

func valid(version string) func(*testing.T, string) (int, []byte) {
	return func(t *testing.T, digest string) (int, []byte) {
		return http.StatusOK, attestationBody(t, digest, "example/agentclip", releaseWorkflowPath, "refs/tags/"+version)
	}
}

func TestPrepareAcceptsAFileWithAMatchingAttestation(t *testing.T) {
	fixture := &releaseFixture{version: "v1.2.3", attestation: valid("v1.2.3")}
	staged, err := fixture.prepare(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if staged.Notice != "" || fixture.apiRequests != 1 {
		t.Fatalf("notice = %q, API requests = %d, want none and one", staged.Notice, fixture.apiRequests)
	}
}

func TestPrepareRefusesFilesThatTheAttestationDoesNotVouchFor(t *testing.T) {
	const version = "v1.2.3"
	tests := map[string]struct {
		attestation func(*testing.T, string) (int, []byte)
		wantError   string
	}{
		"no attestation for the file": {func(*testing.T, string) (int, []byte) { return http.StatusNotFound, []byte(`{}`) }, "no build attestation"},
		"empty attestation list":      {func(*testing.T, string) (int, []byte) { return http.StatusOK, []byte(`{"attestations":[]}`) }, "no build attestation"},
		"attestation for another file": {func(t *testing.T, _ string) (int, []byte) {
			return http.StatusOK, attestationBody(t, strings.Repeat("a", 64), "example/agentclip", releaseWorkflowPath, "refs/tags/"+version)
		}, "does not cover this file"},
		"made from a branch, not the tag": {func(t *testing.T, d string) (int, []byte) {
			return http.StatusOK, attestationBody(t, d, "example/agentclip", releaseWorkflowPath, "refs/heads/main")
		}, "not the release tag"},
		"made for a different tag": {func(t *testing.T, d string) (int, []byte) {
			return http.StatusOK, attestationBody(t, d, "example/agentclip", releaseWorkflowPath, "refs/tags/v9.9.9")
		}, "not the release tag"},
		"made by another workflow": {func(t *testing.T, d string) (int, []byte) {
			return http.StatusOK, attestationBody(t, d, "example/agentclip", ".github/workflows/evil.yml", "refs/tags/"+version)
		}, "not the release workflow"},
		"made in another repository": {func(t *testing.T, d string) (int, []byte) {
			return http.StatusOK, attestationBody(t, d, "attacker/agentclip", releaseWorkflowPath, "refs/tags/"+version)
		}, "made for repository"},
		"malformed payload": {func(*testing.T, string) (int, []byte) {
			return http.StatusOK, []byte(`{"attestations":[{"bundle":{"dsseEnvelope":{"payload":"%%%"}}}]}`)
		}, "not valid base64"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := &releaseFixture{version: version, attestation: test.attestation}
			staged, err := fixture.prepare(t, Options{})
			if err == nil {
				t.Fatalf("Prepare accepted the file and staged %q", staged.Path)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want it to contain %q", err, test.wantError)
			}
		})
	}
}

func TestPrepareFailsClosedWhenTheAttestationAPIIsUnavailable(t *testing.T) {
	fixture := &releaseFixture{version: "v1.2.3", attestation: func(*testing.T, string) (int, []byte) {
		return http.StatusForbidden, []byte(`{"message":"API rate limit exceeded"}`)
	}}
	_, err := fixture.prepare(t, Options{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), SkipAttestationEnv) {
		t.Fatalf("error = %v, want HTTP 403 and a hint about %s", err, SkipAttestationEnv)
	}
}

func TestPrepareAcceptsAnyMatchingAttestationInTheList(t *testing.T) {
	fixture := &releaseFixture{version: "v1.2.3", attestation: func(t *testing.T, digest string) (int, []byte) {
		var bad, good map[string][]map[string]any
		if err := json.Unmarshal(attestationBody(t, strings.Repeat("b", 64), "example/agentclip", releaseWorkflowPath, "refs/tags/v1.2.3"), &bad); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(attestationBody(t, digest, "example/agentclip", releaseWorkflowPath, "refs/tags/v1.2.3"), &good); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"attestations": append(bad["attestations"], good["attestations"]...)})
		return http.StatusOK, body
	}}
	if _, err := fixture.prepare(t, Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSkipsTheCheckForReleasesThatPredateAttestations(t *testing.T) {
	fixture := &releaseFixture{version: "v0.7.0", attestation: func(*testing.T, string) (int, []byte) { return http.StatusNotFound, nil }}
	staged, err := fixture.prepare(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.apiRequests != 0 || !strings.Contains(staged.Notice, "predates build attestations") {
		t.Fatalf("API requests = %d, notice = %q", fixture.apiRequests, staged.Notice)
	}
}

func TestPrepareHonoursTheExplicitOptOut(t *testing.T) {
	fixture := &releaseFixture{version: "v1.2.3", attestation: func(*testing.T, string) (int, []byte) { return http.StatusNotFound, nil }}
	staged, err := fixture.prepare(t, Options{SkipAttestation: true})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.apiRequests != 0 || !strings.Contains(staged.Notice, SkipAttestationEnv) {
		t.Fatalf("API requests = %d, notice = %q", fixture.apiRequests, staged.Notice)
	}
}

func TestAttestationRequiredFromTheFirstAttestedRelease(t *testing.T) {
	for version, want := range map[string]bool{
		"v0.6.0": false, "v0.7.0": false, "v0.7.1-rc.1": true, "v0.7.1": true, "v0.8.0": true, "v1.0.0": true, "not-a-version": true,
	} {
		if got := attestationRequired(version); got != want {
			t.Errorf("attestationRequired(%q) = %v, want %v", version, got, want)
		}
	}
}
