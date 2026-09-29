package upgrader

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/wendellrocha/agentclip/internal/release"
)

const (
	// AttestationMinVersion is the first release published with a build
	// attestation. Earlier releases predate it, so only their SHA-256 can be
	// checked.
	AttestationMinVersion = "v0.7.1-rc.1"
	// SkipAttestationEnv opts out of the attestation check, for example on a
	// network that cannot reach api.github.com.
	SkipAttestationEnv = "AGENTCLIP_SKIP_ATTESTATION"

	defaultAPIBaseURL   = "https://api.github.com"
	releaseWorkflowPath = ".github/workflows/release.yml"
	maxAttestationBytes = 4 << 20
)

var errNoAttestation = errors.New("GitHub has no build attestation for this file")

type attestationResponse struct {
	Attestations []struct {
		Bundle struct {
			DSSEEnvelope struct {
				Payload string `json:"payload"`
			} `json:"dsseEnvelope"`
		} `json:"bundle"`
	} `json:"attestations"`
}

// provenanceStatement is the part of the SLSA provenance that GitHub records
// for a workflow run and that identifies what built which file.
type provenanceStatement struct {
	Subject []struct {
		Digest struct {
			SHA256 string `json:"sha256"`
		} `json:"digest"`
	} `json:"subject"`
	Predicate struct {
		BuildDefinition struct {
			ExternalParameters struct {
				Workflow struct {
					Ref        string `json:"ref"`
					Repository string `json:"repository"`
					Path       string `json:"path"`
				} `json:"workflow"`
			} `json:"externalParameters"`
		} `json:"buildDefinition"`
	} `json:"predicate"`
}

// attestationRequired reports whether version must carry a build attestation.
// An unparseable version is treated as requiring one.
func attestationRequired(version string) bool {
	comparison, err := release.Compare(version, AttestationMinVersion)
	return err != nil || comparison >= 0
}

// verifyAttestation asks the GitHub attestations API whether this repository
// holds a build attestation for the archive's SHA-256, made by the release
// workflow on the tag being installed. Someone who can replace release files
// cannot add such a record without running a workflow in the repository.
//
// The lookup trusts api.github.com over TLS; it does not check the Sigstore
// signature itself. `gh attestation verify` does that, and is documented in
// the README for manual verification.
func verifyAttestation(ctx context.Context, client *http.Client, apiBaseURL, repository, version, digest string) error {
	url := strings.TrimRight(apiBaseURL, "/") + "/repos/" + repository + "/attestations/sha256:" + strings.ToLower(digest)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "agentclip-upgrade")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("query GitHub attestations: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return errNoAttestation
	default:
		return fmt.Errorf("GitHub attestations API returned HTTP %d", response.StatusCode)
	}
	var payload attestationResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAttestationBytes)).Decode(&payload); err != nil {
		return fmt.Errorf("read GitHub attestations: %w", err)
	}
	var lastMismatch error
	for _, attestation := range payload.Attestations {
		statement, err := decodeStatement(attestation.Bundle.DSSEEnvelope.Payload)
		if err != nil {
			lastMismatch = err
			continue
		}
		if err := matchStatement(statement, repository, version, digest); err != nil {
			lastMismatch = err
			continue
		}
		return nil
	}
	if lastMismatch != nil {
		return lastMismatch
	}
	return errNoAttestation
}

func decodeStatement(encoded string) (provenanceStatement, error) {
	var statement provenanceStatement
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return statement, errors.New("attestation payload is not valid base64")
	}
	if err := json.Unmarshal(raw, &statement); err != nil {
		return statement, errors.New("attestation payload is not valid JSON")
	}
	return statement, nil
}

func matchStatement(statement provenanceStatement, repository, version, digest string) error {
	found := false
	for _, subject := range statement.Subject {
		if strings.EqualFold(subject.Digest.SHA256, digest) {
			found = true
			break
		}
	}
	if !found {
		return errors.New("attestation does not cover this file")
	}
	workflow := statement.Predicate.BuildDefinition.ExternalParameters.Workflow
	switch {
	case workflow.Repository != "https://github.com/"+repository:
		return fmt.Errorf("attestation was made for repository %q, not %q", workflow.Repository, repository)
	case workflow.Path != releaseWorkflowPath:
		return fmt.Errorf("attestation was made by workflow %q, not the release workflow", workflow.Path)
	case workflow.Ref != "refs/tags/"+version:
		return fmt.Errorf("attestation was made for %q, not the release tag %s", workflow.Ref, version)
	}
	return nil
}
