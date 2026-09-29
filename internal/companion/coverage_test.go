package companion

import (
	"context"
	"errors"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/bridge"
	"github.com/wendellrocha/agentclip/internal/clipboard"
	"github.com/wendellrocha/agentclip/internal/testenv"
)

type imageFake struct {
	data []byte
	err  error
}

func (f imageFake) ReadImage(context.Context) ([]byte, error) { return f.data, f.err }

func filesFake(paths ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) {
		if len(paths) == 0 {
			return nil, clipboard.ErrNoFiles
		}
		return paths, nil
	}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The fingerprint is what the watcher polls, so it must change exactly when the
// files change and never read their content.
func TestFingerprintOfCopiedFilesTracksNamesSizesAndTimes(t *testing.T) {
	first, second := writeFile(t, "a.csv", "1,2"), writeFile(t, "b.csv", "3,4,5")
	source := HostSnapshotSource{FileReader: filesFake(first, second)}
	before, err := source.Fingerprint(context.Background())
	if err != nil || !strings.HasPrefix(before, "files:") {
		t.Fatalf("fingerprint = %q, %v", before, err)
	}
	// The order the clipboard lists them in must not matter.
	swapped, _ := HostSnapshotSource{FileReader: filesFake(second, first)}.Fingerprint(context.Background())
	if swapped != before {
		t.Errorf("the same files in another order changed the fingerprint")
	}
	if err := os.WriteFile(first, []byte("1,2,3,4"), 0o600); err != nil {
		t.Fatal(err)
	}
	if after, _ := source.Fingerprint(context.Background()); after == before {
		t.Error("a changed file did not change the fingerprint")
	}
	// A directory or a missing file is refused, not fingerprinted.
	for _, bad := range []string{filepath.Dir(first), filepath.Join(filepath.Dir(first), "missing")} {
		if _, err := (HostSnapshotSource{FileReader: filesFake(bad)}).Fingerprint(context.Background()); !errors.Is(err, bridge.ErrInvalidFile) {
			t.Errorf("Fingerprint(%q) = %v, want ErrInvalidFile", bad, err)
		}
	}
}

func TestFingerprintFallsBackToTheImageWhenNoFilesAreCopied(t *testing.T) {
	source := HostSnapshotSource{FileReader: filesFake(), ImageReader: imageFake{data: pngFixture(t, color.RGBA{R: 9, A: 255})}}
	fingerprint, err := source.Fingerprint(context.Background())
	if err != nil || !strings.HasPrefix(fingerprint, "image:") {
		t.Fatalf("fingerprint = %q, %v", fingerprint, err)
	}
	// A failure reading files that is not "there are none" is reported, not hidden.
	broken := HostSnapshotSource{FileReader: func(context.Context) ([]string, error) { return nil, errors.New("clipboard busy") }}
	if _, err := broken.Fingerprint(context.Background()); err == nil || !strings.Contains(err.Error(), "clipboard busy") {
		t.Fatalf("err = %v, want the clipboard error", err)
	}
}

func TestReadSnapshotOfFilesCarriesOnlyPathsAndNeverExposesTheDirectory(t *testing.T) {
	csv := writeFile(t, "report.CSV", "a,b\n")
	items, err := HostSnapshotSource{FileReader: filesFake(csv)}.ReadSnapshot(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v, %v", items, err)
	}
	item := items[0]
	if item.Kind != bridge.ItemFile || item.Name != "report.CSV" || !strings.HasPrefix(item.MIMEType, "text/csv") {
		t.Errorf("item = %+v, want a csv file named report.CSV", item)
	}
	if item.File == nil || *item.File != (bridge.FileRef{Path: csv}) {
		t.Errorf("file reference = %+v, want the path alone: the daemon measures the file itself", item.File)
	}

	missing := filepath.Join(t.TempDir(), "private-folder", "gone.txt")
	_, err = HostSnapshotSource{FileReader: filesFake(missing)}.ReadSnapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "gone.txt") || strings.Contains(err.Error(), "private-folder") {
		t.Fatalf("err = %v, want the file name without its directory", err)
	}
}

func TestReadSnapshotOfAnImage(t *testing.T) {
	items, err := HostSnapshotSource{FileReader: filesFake(), ImageReader: imageFake{data: pngFixture(t, color.RGBA{R: 9, A: 255})}}.ReadSnapshot(context.Background())
	if err != nil || len(items) != 1 || items[0].Kind != bridge.ItemImage || items[0].MIMEType != "image/png" || items[0].Width != 2 || items[0].Height != 2 {
		t.Fatalf("items = %+v, %v", items, err)
	}
}

func TestLoggerWritesLevelsEscapesNewlinesAndExportsPrivately(t *testing.T) {
	testenv.IsolateUserDirs(t)
	logger, err := NewLogger("logs")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	logger.Info("started %s", "ok")
	logger.Error("bad\nINJECTED [INFO] fake line")
	logger.Debug("hidden unless debugging")
	data, err := os.ReadFile(logger.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "[INFO] started ok") || !strings.Contains(content, "[ERROR] bad") {
		t.Errorf("log = %q", content)
	}
	if strings.Count(content, "\n") != 2 || strings.Contains(content, "hidden") {
		t.Errorf("a newline in a message must not start a line of its own, and debug stays off: %q", content)
	}

	destination := filepath.Join(t.TempDir(), "export.log")
	if err := logger.Export(destination); err != nil {
		t.Fatal(err)
	}
	if exported, _ := os.ReadFile(destination); string(exported) != content {
		t.Errorf("export differs from the log")
	}
	if info, _ := os.Stat(destination); info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("export mode = %v, want 0600", info.Mode().Perm())
	}
	if err := logger.Export("  "); err == nil {
		t.Error("an empty export destination was accepted")
	}
	if err := logger.Export(filepath.Join(t.TempDir(), "no", "such", "dir", "x.log")); err == nil {
		t.Error("an export into a missing directory was accepted")
	}
}

func TestDebugLoggingIsOptInThroughTheEnvironment(t *testing.T) {
	testenv.IsolateUserDirs(t)
	t.Setenv("AGENTCLIP_LOG_LEVEL", "debug")
	logger, err := NewLogger("verbose")
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()
	logger.Debug("now visible")
	if data, _ := os.ReadFile(logger.Path()); !strings.Contains(string(data), "[DEBUG] now visible") {
		t.Errorf("log = %q", data)
	}
	// A nil logger is safe to use: callers pass one when logging is off.
	var none *Logger
	none.Debug("ignored")
}

func TestProfileHelpers(t *testing.T) {
	if (Profile{UploadToken: "  "}).HasUploadToken() || !(Profile{UploadToken: "x"}).HasUploadToken() {
		t.Error("HasUploadToken must ignore blank tokens")
	}
	testenv.IsolateUserDirs(t)
	path, err := SSHIdentityPath("m2")
	if err != nil || filepath.Base(path) != "m2" || filepath.Base(filepath.Dir(path)) != "keys" {
		t.Fatalf("SSHIdentityPath = %q, %v", path, err)
	}
	for _, name := range []string{"", "../x", "a/b", "a b"} {
		if _, err := SSHIdentityPath(name); err == nil {
			t.Errorf("SSHIdentityPath(%q) was accepted", name)
		}
	}
}

func TestInboundActionCallsTheControlEndpointWithTheTokenAndOnlyForSafeIDs(t *testing.T) {
	var method, path, auth string
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(status)
	}))
	defer server.Close()
	state := RuntimeState{Address: server.Listener.Addr().String(), ControlToken: "control-secret"}

	if err := InboundAction(state, "abc123", "accept"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/v1/control/inbound/abc123/accept" || auth != "Bearer control-secret" {
		t.Errorf("request = %s %s %q", method, path, auth)
	}
	status = http.StatusConflict
	if err := InboundAction(state, "abc123", "reject"); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Errorf("err = %v, want the status reported", err)
	}

	// The id is typed by the user and lands in a URL that carries the token.
	path = ""
	for _, id := range []string{"", "../stop", "a/b", "a?b=c", "a#b", "a b", "%2e%2e", strings.Repeat("a", 65)} {
		if err := InboundAction(state, id, "accept"); err == nil {
			t.Errorf("id %q was accepted", id)
		}
	}
	if err := InboundAction(state, "abc", "delete"); err == nil {
		t.Error("an unknown action was accepted")
	}
	if path != "" {
		t.Errorf("a refused request still reached the server: %s", path)
	}
}
