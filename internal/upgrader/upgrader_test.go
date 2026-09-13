package upgrader

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestPrepareSelectsAssetVerifiesChecksumAndStagesBinary(t *testing.T) {
	const version = "v1.2.3"
	const asset = "agentclip_v1.2.3_linux_amd64.tar.gz"
	archive := tarFixture(t, "agentclip_v1.2.3_linux_amd64/agentclip", []byte("new executable"))
	sum := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch filepath.Base(request.URL.Path) {
		case asset:
			_, _ = w.Write(archive)
		case "checksums.txt":
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n"))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	client := rewriteClient(server)
	target := filepath.Join(t.TempDir(), "agentclip")
	staged, err := Prepare(context.Background(), Options{Version: version, Executable: target, GOOS: "linux", GOARCH: "amd64", Repository: "example/agentclip", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Cleanup()
	if staged.Target != target || filepath.Dir(staged.Path) != filepath.Dir(target) {
		t.Fatalf("stage = %#v", staged)
	}
	payload, err := os.ReadFile(staged.Path)
	if err != nil || string(payload) != "new executable" {
		t.Fatalf("staged payload = %q, %v", payload, err)
	}
}

func TestPrepareRefusesInvalidChecksumAndUnsupportedPlatform(t *testing.T) {
	if _, _, err := platform("linux", "386"); err == nil {
		t.Fatal("386 platform accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if filepath.Base(request.URL.Path) == "checksums.txt" {
			_, _ = w.Write([]byte("00  asset\n"))
			return
		}
		_, _ = w.Write([]byte("not an archive"))
	}))
	defer server.Close()
	_, err := Prepare(context.Background(), Options{Version: "v1.2.3", Executable: filepath.Join(t.TempDir(), "agentclip"), GOOS: "linux", GOARCH: "amd64", Repository: "example/agentclip", Client: rewriteClient(server)})
	if err == nil {
		t.Fatal("invalid checksum accepted")
	}
}

func rewriteClient(server *httptest.Server) *http.Client {
	return &http.Client{Transport: testTransport(func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme = "http"
		request.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(request)
	})}
}

func tarFixture(t *testing.T, name string, contents []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
