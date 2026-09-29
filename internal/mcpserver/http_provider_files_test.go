package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/testenv"
)

func sumOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fileBridge serves a status listing and the bytes of each file item, the way
// the host's bridge does. Tests tamper with the response through the hooks.
type fileBridge struct {
	items     []ItemMetadata
	content   map[string][]byte
	tamper    func(id string, w http.ResponseWriter) (handled bool)
	downloads int
}

func (f *fileBridge) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"armed": true, "items": f.items})
		case strings.HasPrefix(r.URL.Path, "/v1/items/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/items/")
			f.downloads++
			if f.tamper != nil && f.tamper(id, w) {
				return
			}
			data := f.content[id]
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("X-AgentClip-SHA256", sumOf(data))
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	})
}

func newFileBridge(t *testing.T, files map[string][]byte, names map[string]string) (*fileBridge, *HTTPProvider) {
	t.Helper()
	bridge := &fileBridge{content: files}
	for id, data := range files {
		// An entry that is present but empty stays empty: that is the edge case.
		name, present := names[id]
		if !present {
			name = id + ".txt"
		}
		bridge.items = append(bridge.items, ItemMetadata{ID: id, Kind: "file", Name: name, Size: int64(len(data)), SHA256: sumOf(data), MIMEType: "text/plain"})
	}
	server := httptest.NewServer(bridge.handler(t))
	t.Cleanup(server.Close)
	return bridge, providerForServer(t, server, "session-token")
}

func TestMaterializeFilesWritesVerifiedFilesToAPrivateDirectory(t *testing.T) {
	_, provider := newFileBridge(t, map[string][]byte{"a": []byte("alpha"), "b": []byte("bravo")}, map[string]string{"a": "report.csv", "b": "notes.md"})
	result, err := provider.MaterializeFiles(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 {
		t.Fatalf("files = %+v", result.Files)
	}
	for _, file := range result.Files {
		data, err := os.ReadFile(file.Path)
		if err != nil || sumOf(data) != file.SHA256 || int64(len(data)) != file.Size {
			t.Fatalf("file %+v: %q, %v", file, data, err)
		}
		if filepath.Dir(file.Path) != result.Directory {
			t.Fatalf("%q escaped the materialization directory %q", file.Path, result.Directory)
		}
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{result.Directory, result.Files[0].Path} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				t.Errorf("%s has permissions %v; group and others must have no access", path, perm)
			}
		}
	}
}

// The id names the fallback file and the request path, so an id that is not
// filename-safe is refused before anything is written.
func TestMaterializeFilesRefusesUnsafeItemIDs(t *testing.T) {
	for _, id := range []string{"../../escape", "a/b", `a\b`, "..", "", strings.Repeat("a", 65)} {
		t.Run("id="+id, func(t *testing.T) {
			bridge := &fileBridge{content: map[string][]byte{id: []byte("data")}}
			bridge.items = []ItemMetadata{{ID: id, Kind: "file", Name: "..", Size: 4, SHA256: sumOf([]byte("data")), MIMEType: "text/plain"}}
			server := httptest.NewServer(bridge.handler(t))
			t.Cleanup(server.Close)
			provider := providerForServer(t, server, "session-token")
			if result, err := provider.MaterializeFiles(context.Background(), []string{id}); err == nil {
				t.Fatalf("id %q was accepted: %+v", id, result)
			}
		})
	}
}

func TestMaterializeFilesNeverWritesOutsideItsDirectory(t *testing.T) {
	for _, name := range []string{"../../escape.txt", "/etc/passwd", "sub/../../up.txt", "..", ".", ""} {
		t.Run("name="+name, func(t *testing.T) {
			_, provider := newFileBridge(t, map[string][]byte{"x": []byte("data")}, map[string]string{"x": name})
			result, err := provider.MaterializeFiles(context.Background(), []string{"x"})
			if err != nil {
				t.Fatal(err)
			}
			path := result.Files[0].Path
			relative, err := filepath.Rel(result.Directory, path)
			if err != nil || filepath.Dir(path) != result.Directory || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
				t.Fatalf("name %q produced %q outside %q (relative %q, %v)", name, path, result.Directory, relative, err)
			}
			if base := filepath.Base(path); base == ".." || base == "." {
				t.Fatalf("name %q produced the special name %q", name, base)
			}
		})
	}
}

func TestMaterializeFilesKeepsBothFilesThatShareAName(t *testing.T) {
	_, provider := newFileBridge(t, map[string][]byte{"a": []byte("one"), "b": []byte("two")}, map[string]string{"a": "same.txt", "b": "same.txt"})
	result, err := provider.MaterializeFiles(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files[0].Path == result.Files[1].Path {
		t.Fatalf("both files were written to %q", result.Files[0].Path)
	}
	first, _ := os.ReadFile(result.Files[0].Path)
	second, _ := os.ReadFile(result.Files[1].Path)
	if string(first) != "one" || string(second) != "two" {
		t.Fatalf("contents = %q, %q", first, second)
	}
}

func TestMaterializeFilesRejectsBadSelections(t *testing.T) {
	bridge, provider := newFileBridge(t, map[string][]byte{"a": []byte("x")}, nil)
	bridge.items = append(bridge.items,
		ItemMetadata{ID: "consumed", Kind: "file", Name: "c", Size: 1, SHA256: sumOf([]byte("x")), Consumed: true},
		ItemMetadata{ID: "text", Kind: "text", Name: "t", Size: 1},
		ItemMetadata{ID: "huge", Kind: "file", Name: "h", Size: 51 << 20},
		ItemMetadata{ID: "negative", Kind: "file", Name: "n", Size: -1},
	)
	tests := map[string][]string{
		"no ids":           nil,
		"six ids":          {"a", "a", "a", "a", "a", "a"},
		"unknown item":     {"missing"},
		"consumed item":    {"consumed"},
		"text item":        {"text"},
		"file over 50 MiB": {"huge"},
		"negative size":    {"negative"},
	}
	for name, ids := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := provider.MaterializeFiles(context.Background(), ids); err == nil {
				t.Fatalf("selection %v was accepted", ids)
			}
		})
	}
	if bridge.downloads != 0 {
		t.Fatalf("a rejected selection downloaded %d file(s)", bridge.downloads)
	}
}

func TestMaterializeFilesEnforcesTheTotalTransferLimit(t *testing.T) {
	bridge, provider := newFileBridge(t, map[string][]byte{"a": []byte("x")}, nil)
	bridge.items = []ItemMetadata{
		{ID: "a", Kind: "file", Name: "a", Size: 40 << 20},
		{ID: "b", Kind: "file", Name: "b", Size: 40 << 20},
		{ID: "c", Kind: "file", Name: "c", Size: 40 << 20},
	}
	if _, err := provider.MaterializeFiles(context.Background(), []string{"a", "b", "c"}); err == nil || !strings.Contains(err.Error(), "transfer limits") {
		t.Fatalf("err = %v, want the transfer limit", err)
	}
	if bridge.downloads != 0 {
		t.Fatalf("downloaded %d file(s) before checking the limit", bridge.downloads)
	}
}

func TestMaterializeFilesRefusesAndCleansUpWhenTheTransferIsTampered(t *testing.T) {
	tests := map[string]func(id string, w http.ResponseWriter) bool{
		"content differs from the announced hash": func(id string, w http.ResponseWriter) bool {
			w.Header().Set("Content-Length", "5")
			w.Header().Set("X-AgentClip-SHA256", sumOf([]byte("alpha")))
			_, _ = w.Write([]byte("EVIL!"))
			return true
		},
		"header hash differs from the listed one": func(id string, w http.ResponseWriter) bool {
			w.Header().Set("Content-Length", "5")
			w.Header().Set("X-AgentClip-SHA256", sumOf([]byte("other")))
			_, _ = w.Write([]byte("alpha"))
			return true
		},
		"length differs from the listed size": func(id string, w http.ResponseWriter) bool {
			w.Header().Set("Content-Length", "6")
			w.Header().Set("X-AgentClip-SHA256", sumOf([]byte("alpha")))
			_, _ = w.Write([]byte("alpha!"))
			return true
		},
		"bridge error": func(id string, w http.ResponseWriter) bool {
			http.Error(w, "gone", http.StatusGone)
			return true
		},
	}
	for name, tamper := range tests {
		t.Run(name, func(t *testing.T) {
			// A private cache, so the inbox holds only what this transfer left.
			testenv.IsolateUserDirs(t)
			bridge, provider := newFileBridge(t, map[string][]byte{"a": []byte("alpha")}, map[string]string{"a": "a.txt"})
			bridge.tamper = tamper
			result, err := provider.MaterializeFiles(context.Background(), []string{"a"})
			if err == nil {
				t.Fatalf("a tampered transfer was accepted: %+v", result)
			}
			cache, _ := os.UserCacheDir()
			entries, _ := os.ReadDir(filepath.Join(cache, "agentclip", "inbox"))
			for _, entry := range entries {
				t.Errorf("a failed transfer left %q behind in the inbox", entry.Name())
			}
		})
	}
}

func TestTextReturnsPlainTextAndEnforcesItsLimits(t *testing.T) {
	serve := func(contentType string, body []byte) *HTTPProvider {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			_, _ = w.Write(body)
		}))
		t.Cleanup(server.Close)
		return providerForServer(t, server, "session-token")
	}

	text, err := serve("text/plain; charset=utf-8", []byte("hello é")).Text(context.Background(), "t1")
	if err != nil || string(text.Data) != "hello é" || !strings.HasPrefix(text.MIMEType, "text/plain") {
		t.Fatalf("Text = %+v, %v", text, err)
	}
	if _, err := serve("text/plain", []byte("x")).Text(context.Background(), "  "); err == nil {
		t.Error("an empty item id must be refused")
	}
	if _, err := serve("image/png", []byte("x")).Text(context.Background(), "t1"); err == nil || !strings.Contains(err.Error(), "not text") {
		t.Errorf("a non-text item must be refused, got %v", err)
	}
	if _, err := serve("text/plain", nil).Text(context.Background(), "t1"); err == nil {
		t.Error("empty text must be refused")
	}
	if _, err := serve("text/plain", []byte(strings.Repeat("a", 1<<20+1))).Text(context.Background(), "t1"); err == nil || !strings.Contains(err.Error(), "maximum size") {
		t.Errorf("text over 1 MiB must be refused, got %v", err)
	}
	if _, err := serve("text/plain", []byte(strings.Repeat("a", 1<<20))).Text(context.Background(), "t1"); err != nil {
		t.Errorf("text of exactly 1 MiB must be accepted, got %v", err)
	}
}

func TestRemoteFileMetadataOnlyAcceptsRegularFilesWithinTheLimit(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "report.csv")
	if err := os.WriteFile(file, []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := remoteFileMetadata(file)
	if err != nil || metadata.Name != "report.csv" || metadata.Size != 4 || metadata.SHA256 != sumOf([]byte("a,b\n")) {
		t.Fatalf("metadata = %+v, %v", metadata, err)
	}
	if _, err := remoteFileMetadata(dir); err == nil {
		t.Error("a directory must be refused")
	}
	if _, err := remoteFileMetadata(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing file must be refused")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(file, link); err != nil {
			t.Fatal(err)
		}
		if _, err := remoteFileMetadata(link); err == nil {
			t.Error("a symlink must be refused, so a link cannot smuggle another file in")
		}
	}
	big := filepath.Join(dir, "big")
	handle, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Truncate(50<<20 + 1); err != nil {
		t.Fatal(err)
	}
	handle.Close()
	if _, err := remoteFileMetadata(big); err == nil || !strings.Contains(err.Error(), "transfer limits") {
		t.Errorf("a file over 50 MiB must be refused, got %v", err)
	}
}

// hostInbox stands in for the bridge's inbound endpoints.
type hostInbox struct {
	offer     HostFileOffer
	uploaded  []byte
	uploadLen int64
	auth      []string
	created   remoteFileOffer
}

func newHostInbox(t *testing.T, state string) (*hostInbox, *HTTPProvider) {
	t.Helper()
	inbox := &hostInbox{offer: HostFileOffer{ID: "offer-1", State: state}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inbox.auth = append(inbox.auth, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/inbound/offers":
			_ = json.NewDecoder(r.Body).Decode(&inbox.created)
			inbox.offer.Name, inbox.offer.Size, inbox.offer.SHA256 = inbox.created.Name, inbox.created.Size, inbox.created.SHA256
			_ = json.NewEncoder(w).Encode(inbox.offer)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/inbound/offers/offer-1":
			_ = json.NewEncoder(w).Encode(inbox.offer)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/inbound/offers/offer-1/content":
			inbox.uploadLen = r.ContentLength
			inbox.uploaded, _ = io.ReadAll(r.Body)
			inbox.offer.State = "delivered"
			_ = json.NewEncoder(w).Encode(inbox.offer)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	parsed, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(parsed.Port())
	provider, err := NewHTTPProvider(port, "session-token", "upload-token")
	if err != nil {
		t.Fatal(err)
	}
	return inbox, provider
}

func TestOfferFileToHostSendsOnlyVerifiedMetadataWithTheUploadToken(t *testing.T) {
	inbox, provider := newHostInbox(t, "pending")
	file := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(file, []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	offer, err := provider.OfferFileToHost(context.Background(), file)
	if err != nil || offer.ID != "offer-1" {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	want := remoteFileOffer{Name: "report.csv", Size: 4, SHA256: sumOf([]byte("a,b\n"))}
	if inbox.created != want {
		t.Fatalf("the host received %+v, want %+v", inbox.created, want)
	}
	if strings.Contains(inbox.created.Name, string(filepath.Separator)) {
		t.Fatalf("the offer leaked a path: %q", inbox.created.Name)
	}
	for _, header := range inbox.auth {
		if header != "Bearer upload-token" {
			t.Fatalf("Authorization = %q; offers must use the upload token, not the read token", header)
		}
	}
	if _, err := provider.OfferFileToHost(context.Background(), t.TempDir()); err == nil {
		t.Error("a directory must not be offered")
	}
}

func TestUploadsAreUnavailableWithoutAnUploadToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("no request expected") }))
	defer server.Close()
	provider := providerForServer(t, server, "session-token")
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	if _, err := provider.OfferFileToHost(context.Background(), file); err == nil || !strings.Contains(err.Error(), "run agentclip setup again") {
		t.Fatalf("err = %v", err)
	}
	if _, err := provider.HostFileOfferStatus(context.Background(), "offer-1"); err == nil {
		t.Fatal("status needs the upload token too")
	}
}

func TestDeliverFileToHostStreamsOnlyAnAcceptedUnchangedFile(t *testing.T) {
	content := []byte("a,b\n1,2\n")
	file := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	prepare := func(state string) (*hostInbox, *HTTPProvider) {
		inbox, provider := newHostInbox(t, state)
		inbox.offer.Name, inbox.offer.Size, inbox.offer.SHA256 = "report.csv", int64(len(content)), sumOf(content)
		return inbox, provider
	}

	inbox, provider := prepare("accepted")
	delivered, err := provider.DeliverFileToHost(context.Background(), "offer-1", file)
	if err != nil || delivered.State != "delivered" {
		t.Fatalf("delivered = %+v, %v", delivered, err)
	}
	if string(inbox.uploaded) != string(content) || inbox.uploadLen != int64(len(content)) {
		t.Fatalf("host received %q (length %d)", inbox.uploaded, inbox.uploadLen)
	}

	for _, state := range []string{"pending", "rejected", "expired", "delivered"} {
		inbox, provider := prepare(state)
		if _, err := provider.DeliverFileToHost(context.Background(), "offer-1", file); err == nil || !strings.Contains(err.Error(), "host file offer is "+state) {
			t.Errorf("state %q: err = %v", state, err)
		}
		if inbox.uploaded != nil {
			t.Errorf("state %q: bytes were uploaded without approval", state)
		}
	}

	inbox, provider = prepare("accepted")
	if err := os.WriteFile(file, []byte("a,b\n9,9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DeliverFileToHost(context.Background(), "offer-1", file); err == nil || !strings.Contains(err.Error(), "changed after it was offered") {
		t.Errorf("a file changed after the offer must be refused, got %v", err)
	}
	if inbox.uploaded != nil {
		t.Error("a changed file was uploaded")
	}
	if _, err := provider.DeliverFileToHost(context.Background(), " ", file); err == nil {
		t.Error("an empty offer id must be refused")
	}
}

func TestCleanupInboxLeavesPlainFilesAndToleratesAMissingInbox(t *testing.T) {
	root := t.TempDir()
	stray := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stray, past, past); err != nil {
		t.Fatal(err)
	}
	if err := cleanupInbox(root, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("only snapshot directories are cleaned; a plain file must be left alone")
	}
	if err := cleanupInbox(filepath.Join(root, "missing"), time.Now()); err != nil {
		t.Errorf("a missing inbox is not an error: %v", err)
	}
}
