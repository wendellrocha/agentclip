// Package release resolves and caches AgentClip's stable GitHub release.
package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wendellrocha/agentclip/internal/buildinfo"
)

const (
	Repository = "wendellrocha/agentclip"
	CacheTTL   = 24 * time.Hour
)

var semanticTag = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?$`)

// Status is deliberately safe to expose to a remote MCP client. It contains
// neither authentication material nor any clipboard information.
type Status struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	UpdateAvailable bool      `json:"update_available"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type cacheEntry struct {
	LatestVersion string    `json:"latest_version"`
	CheckedAt     time.Time `json:"checked_at"`
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// Checker is configurable so callers and tests can use a private GitHub
// endpoint. A zero value uses the official AgentClip repository.
type Checker struct {
	Repository string
	CachePath  string
	Client     *http.Client
	Now        func() time.Time
}

func NewChecker() Checker { return Checker{} }

// Check returns a cached result for up to 24 hours. On a failed refresh a
// stale valid cache remains useful and does not expose the network failure to
// callers; without cache, Error is intentionally a short public message.
func (c Checker) Check(ctx context.Context, current string) (Status, error) {
	current = strings.TrimSpace(current)
	status := Status{CurrentVersion: current}
	if !validVersion(current) || strings.Contains(strings.ToLower(current), "dev") {
		return status, nil
	}
	now := c.now()
	entry, found := c.readCache()
	if found && now.Sub(entry.CheckedAt) >= 0 && now.Sub(entry.CheckedAt) < CacheTTL {
		return statusFromCache(status, entry), nil
	}
	tag, err := c.FetchLatest(ctx)
	if err != nil {
		if found {
			return statusFromCache(status, entry), err
		}
		status.CheckedAt = now.UTC()
		status.Error = "unable to check for updates"
		return status, err
	}
	entry = cacheEntry{LatestVersion: tag, CheckedAt: now.UTC()}
	if err := c.writeCache(entry); err != nil {
		// A cache write failure should not suppress a successful release check.
		return statusFromCache(status, entry), nil
	}
	return statusFromCache(status, entry), nil
}

// Current checks the version embedded in the running binary.
func (c Checker) Current(ctx context.Context) (Status, error) {
	return c.Check(ctx, buildinfo.Version)
}

// FetchLatest always resolves the current stable release; upgrades use this
// rather than the daily watcher cache.
func (c Checker) FetchLatest(ctx context.Context) (string, error) {
	repository := c.Repository
	if repository == "" {
		repository = Repository
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repository+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "agentclip-release-watcher")
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("request latest release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("latest release returned HTTP %d", response.StatusCode)
	}
	var payload githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode latest release: %w", err)
	}
	if payload.Draft || payload.Prerelease || !stableTag(payload.TagName) {
		return "", errors.New("latest release is not a stable semantic tag")
	}
	return payload.TagName, nil
}

func (c Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Checker) cachePath() (string, error) {
	if c.CachePath != "" {
		return c.CachePath, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agentclip", "release.json"), nil
}

func (c Checker) readCache() (cacheEntry, bool) {
	path, err := c.cachePath()
	if err != nil {
		return cacheEntry{}, false
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var entry cacheEntry
	if json.Unmarshal(payload, &entry) != nil || !stableTag(entry.LatestVersion) || entry.CheckedAt.IsZero() {
		return cacheEntry{}, false
	}
	return entry, true
}

func (c Checker) writeCache(entry cacheEntry) error {
	path, err := c.cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "release.json.tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(payload)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func statusFromCache(status Status, entry cacheEntry) Status {
	status.LatestVersion = entry.LatestVersion
	status.CheckedAt = entry.CheckedAt
	comparison, err := Compare(entry.LatestVersion, status.CurrentVersion)
	status.UpdateAvailable = err == nil && comparison > 0
	return status
}

func stableTag(value string) bool {
	match := semanticTag.FindStringSubmatch(strings.TrimSpace(value))
	return match != nil && match[4] == ""
}

func validVersion(value string) bool { return semanticTag.MatchString(strings.TrimSpace(value)) }

// Compare follows SemVer precedence, returning 1 when left is newer, -1 when
// right is newer, and 0 when both versions have equal precedence.
func Compare(left, right string) (int, error) {
	a, err := parse(left)
	if err != nil {
		return 0, err
	}
	b, err := parse(right)
	if err != nil {
		return 0, err
	}
	for index := range a.core {
		if a.core[index] > b.core[index] {
			return 1, nil
		}
		if a.core[index] < b.core[index] {
			return -1, nil
		}
	}
	if a.pre == "" && b.pre == "" {
		return 0, nil
	}
	if a.pre == "" {
		return 1, nil
	}
	if b.pre == "" {
		return -1, nil
	}
	ap, bp := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for index := 0; index < len(ap) || index < len(bp); index++ {
		if index == len(ap) {
			return -1, nil
		}
		if index == len(bp) {
			return 1, nil
		}
		ai, aNumber := numericIdentifier(ap[index])
		bi, bNumber := numericIdentifier(bp[index])
		if aNumber && bNumber {
			if ai > bi {
				return 1, nil
			}
			if ai < bi {
				return -1, nil
			}
			continue
		}
		if aNumber {
			return -1, nil
		}
		if bNumber {
			return 1, nil
		}
		if ap[index] > bp[index] {
			return 1, nil
		}
		if ap[index] < bp[index] {
			return -1, nil
		}
	}
	return 0, nil
}

type version struct {
	core [3]uint64
	pre  string
}

func parse(value string) (version, error) {
	match := semanticTag.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return version{}, fmt.Errorf("invalid semantic version %q", value)
	}
	var out version
	for index := range out.core {
		n, err := strconv.ParseUint(match[index+1], 10, 64)
		if err != nil {
			return version{}, fmt.Errorf("invalid semantic version %q", value)
		}
		out.core[index] = n
	}
	out.pre = match[4]
	return out, nil
}

func numericIdentifier(value string) (uint64, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return n, err == nil
}
