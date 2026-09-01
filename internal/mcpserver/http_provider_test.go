package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestHTTPProviderStatusAndImage(t *testing.T) {
	const token = "session-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"armed":true,"width":2,"height":3,"bytes":4,"remaining":1}`))
		case "/v1/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{1, 2, 3})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := providerForServer(t, server, token)
	status, err := provider.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.Armed || status.Width != 2 || status.Height != 3 || status.Remaining != 1 {
		t.Fatalf("Status() = %#v", status)
	}
	image, err := provider.Image(context.Background())
	if err != nil {
		t.Fatalf("Image() error = %v", err)
	}
	if image.MIMEType != "image/png" || string(image.Data) != string([]byte{1, 2, 3}) {
		t.Fatalf("Image() = %#v", image)
	}
}

func TestHTTPProviderReturnsBridgeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"code":"IMAGE_CONSUMED","message":"image consumed"}`))
	}))
	defer server.Close()

	_, err := providerForServer(t, server, "token").Image(context.Background())
	if err == nil || err.Error() != "bridge IMAGE_CONSUMED: image consumed" {
		t.Fatalf("Image() error = %v", err)
	}
}

func TestNewHTTPProviderRejectsInvalidInput(t *testing.T) {
	if _, err := NewHTTPProvider(0, "token"); err == nil {
		t.Fatal("NewHTTPProvider() error = nil for port 0")
	}
	if _, err := NewHTTPProvider(1234, ""); err == nil {
		t.Fatal("NewHTTPProvider() error = nil for empty token")
	}
}

func TestCleanupInboxRemovesOnlyExpiredDirectories(t *testing.T) {
	root := t.TempDir()
	expired := filepath.Join(root, "expired")
	current := filepath.Join(root, "current")
	if err := os.Mkdir(expired, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(current, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-inboxRetention - time.Minute)
	if err := os.Chtimes(expired, old, old); err != nil {
		t.Fatal(err)
	}
	if err := cleanupInbox(root, time.Now().Add(-inboxRetention)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("expired directory still exists: %v", err)
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current directory missing: %v", err)
	}
}

func providerForServer(t *testing.T, server *httptest.Server, token string) *HTTPProvider {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewHTTPProvider(port, token)
	if err != nil {
		t.Fatal(err)
	}
	if provider.baseURL != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("baseURL = %q", provider.baseURL)
	}
	// httptest's listener is loopback; replace the canonical endpoint only in
	// this package test so the production constructor stays constrained.
	provider.baseURL = server.URL
	return provider
}
