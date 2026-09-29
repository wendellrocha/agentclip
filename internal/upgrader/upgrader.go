// Package upgrader downloads and verifies a release binary before it replaces
// the executable that is currently running.
package upgrader

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const Repository = "wendellrocha/agentclip"

type Options struct {
	Version    string
	Executable string
	GOOS       string
	GOARCH     string
	Repository string
	Client     *http.Client
	// APIBaseURL overrides https://api.github.com, for tests.
	APIBaseURL string
	// SkipAttestation disables the build attestation check.
	SkipAttestation bool
}

type StagedBinary struct {
	Path   string
	Target string
	// Notice explains a verification step that was not performed, for the
	// caller to show the user.
	Notice string
}

func (s StagedBinary) Cleanup() {
	if s.Path != "" {
		_ = os.Remove(s.Path)
	}
}

// Fetched is a release binary that has been downloaded and verified. Path is
// valid until Cleanup is called.
type Fetched struct {
	Path string
	// Notice explains a verification step that was not performed, for the
	// caller to show the user.
	Notice string
	dir    string
}

func (f Fetched) Cleanup() {
	if f.dir != "" {
		_ = os.RemoveAll(f.dir)
	}
}

// Fetch downloads the platform archive, checks it against checksums.txt and,
// for releases that have one, against the repository's build attestation, and
// extracts the binary. It never installs anything; the caller decides where the
// verified binary goes, on this machine or on another.
func Fetch(ctx context.Context, options Options) (Fetched, error) {
	if options.Version == "" {
		return Fetched{}, errors.New("release version is required")
	}
	goos, goarch := options.GOOS, options.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	extension, archiveFormat, err := platform(goos, goarch)
	if err != nil {
		return Fetched{}, err
	}
	repository := options.Repository
	if repository == "" {
		repository = Repository
	}
	asset := fmt.Sprintf("agentclip_%s_%s_%s.%s", options.Version, goos, goarch, archiveFormat)
	baseURL := "https://github.com/" + repository + "/releases/download/" + options.Version
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	temporaryDirectory, err := os.MkdirTemp("", "agentclip-upgrade-")
	if err != nil {
		return Fetched{}, fmt.Errorf("create upgrade temporary directory: %w", err)
	}
	fail := func(err error) (Fetched, error) {
		_ = os.RemoveAll(temporaryDirectory)
		return Fetched{}, err
	}
	archivePath := filepath.Join(temporaryDirectory, asset)
	if err := download(ctx, client, baseURL+"/"+asset, archivePath); err != nil {
		return fail(fmt.Errorf("download release asset: %w", err))
	}
	checksumsPath := filepath.Join(temporaryDirectory, "checksums.txt")
	if err := download(ctx, client, baseURL+"/checksums.txt", checksumsPath); err != nil {
		return fail(fmt.Errorf("download release checksums: %w", err))
	}
	expected, err := checksumFor(checksumsPath, asset)
	if err != nil {
		return fail(err)
	}
	actual, err := checksumFile(archivePath)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(expected, actual) {
		return fail(errors.New("release checksum does not match checksums.txt"))
	}
	notice, err := checkAttestation(ctx, client, options, repository, actual)
	if err != nil {
		return fail(err)
	}
	binaryName := "agentclip" + extension
	archiveBinary := filepath.ToSlash(filepath.Join(strings.TrimSuffix(asset, "."+archiveFormat), binaryName))
	preparedPath := filepath.Join(temporaryDirectory, binaryName)
	if archiveFormat == "tar.gz" {
		err = extractTarGz(archivePath, archiveBinary, preparedPath)
	} else {
		err = extractZip(archivePath, archiveBinary, preparedPath)
	}
	if err != nil {
		return fail(err)
	}
	return Fetched{Path: preparedPath, Notice: notice, dir: temporaryDirectory}, nil
}

// Prepare fetches and verifies the release binary for this machine and stages
// it in the target directory, which verifies that the eventual atomic
// replacement has the needed local write permission.
func Prepare(ctx context.Context, options Options) (StagedBinary, error) {
	if options.Executable == "" {
		return StagedBinary{}, errors.New("executable path is required")
	}
	if !filepath.IsAbs(options.Executable) {
		return StagedBinary{}, errors.New("executable path must be absolute")
	}
	fetched, err := Fetch(ctx, options)
	if err != nil {
		return StagedBinary{}, err
	}
	defer fetched.Cleanup()
	preparedPath, notice := fetched.Path, fetched.Notice
	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	target := options.Executable
	directory := filepath.Dir(target)
	staged, err := os.CreateTemp(directory, ".agentclip-upgrade-*")
	if err != nil {
		return StagedBinary{}, fmt.Errorf("prepare replacement next to executable: %w", err)
	}
	stagedPath := staged.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(stagedPath)
		}
	}()
	input, err := os.Open(preparedPath)
	if err == nil {
		_, err = io.Copy(staged, input)
		_ = input.Close()
	}
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(stagedPath)
		return StagedBinary{}, fmt.Errorf("stage replacement executable: %w", err)
	}
	if err := os.Chmod(stagedPath, 0755); err != nil && goos != "windows" {
		_ = os.Remove(stagedPath)
		return StagedBinary{}, fmt.Errorf("mark replacement executable: %w", err)
	}
	return StagedBinary{Path: stagedPath, Target: target, Notice: notice}, nil
}

// checkAttestation enforces the build attestation and returns a notice when it
// is legitimately not applied.
func checkAttestation(ctx context.Context, client *http.Client, options Options, repository, digest string) (string, error) {
	switch {
	case options.SkipAttestation:
		return "Build attestation check skipped (" + SkipAttestationEnv + " is set); only the published SHA-256 was verified.", nil
	case !attestationRequired(options.Version):
		return options.Version + " predates build attestations, so only its published SHA-256 was verified.", nil
	}
	apiBase := options.APIBaseURL
	if apiBase == "" {
		apiBase = defaultAPIBaseURL
	}
	err := verifyAttestation(ctx, client, apiBase, repository, options.Version, digest)
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, errNoAttestation):
		return "", fmt.Errorf("refusing to install %s: %w; the download may have been tampered with", options.Version, err)
	default:
		return "", fmt.Errorf("could not verify the build attestation of %s: %w (set %s=1 to install with the SHA-256 check only)", options.Version, err, SkipAttestationEnv)
	}
}

func platform(goos, goarch string) (extension, archiveFormat string, err error) {
	switch goos {
	case "darwin", "linux":
		if goarch != "amd64" && goarch != "arm64" {
			return "", "", fmt.Errorf("unsupported CPU architecture: %s", goarch)
		}
		return "", "tar.gz", nil
	case "windows":
		if goarch != "amd64" {
			return "", "", fmt.Errorf("unsupported CPU architecture: %s", goarch)
		}
		return ".exe", "zip", nil
	default:
		return "", "", fmt.Errorf("unsupported operating system: %s", goos)
	}
}

func download(ctx context.Context, client *http.Client, url, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "agentclip-upgrade")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release download returned HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, io.LimitReader(response.Body, 256<<20))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func checksumFor(path, asset string) (string, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(payload), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		if len(fields[0]) != 64 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err == nil {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s was not found in checksums.txt", asset)
}

func checksumFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractTarGz(archivePath, wanted, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("read release archive: %w", err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read release archive: %w", err)
		}
		if header.Name != wanted {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 100<<20 {
			return errors.New("release archive contains an invalid executable")
		}
		return writeExtracted(reader, destination, header.Size)
	}
	return errors.New("release archive did not contain the expected executable")
}

func extractZip(archivePath, wanted, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("read release archive: %w", err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if entry.Name != wanted {
			continue
		}
		if entry.FileInfo().IsDir() || entry.UncompressedSize64 < 1 || entry.UncompressedSize64 > 100<<20 {
			return errors.New("release archive contains an invalid executable")
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		err = writeExtracted(reader, destination, int64(entry.UncompressedSize64))
		_ = reader.Close()
		return err
	}
	return errors.New("release archive did not contain the expected executable")
}

func writeExtracted(reader io.Reader, destination string, size int64) error {
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, io.LimitReader(reader, size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	info, err := os.Stat(destination)
	if err != nil || info.Size() != size {
		return errors.New("release executable has unexpected size")
	}
	return nil
}
