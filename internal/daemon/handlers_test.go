package daemon

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/bridge"
)

func controlRequest(t *testing.T, d *Daemon, method, path string, body []byte) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+d.State.Address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(payload)
}

func startForHandlers(t *testing.T) *Daemon {
	t.Helper()
	d, err := Start(nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// Every control endpoint accepts exactly one method and says so otherwise.
func TestControlEndpointsRejectTheWrongMethod(t *testing.T) {
	d := startForHandlers(t)
	for path, wrong := range map[string]string{
		"/v1/control/arm":                http.MethodGet,
		"/v1/control/snapshot":           http.MethodGet,
		"/v1/control/sessions":           http.MethodGet,
		"/v1/control/persistent-session": http.MethodGet,
		"/v1/control/release":            http.MethodGet,
		"/v1/control/inbound":            http.MethodPost,
		"/v1/control/shutdown":           http.MethodGet,
	} {
		t.Run(path, func(t *testing.T) {
			status, body := controlRequest(t, d, wrong, path, nil)
			if status != http.StatusMethodNotAllowed || strings.TrimSpace(body) != "method not allowed" {
				t.Fatalf("%s %s = %d %q, want 405 \"method not allowed\"", wrong, path, status, body)
			}
		})
	}
	// The inbound action endpoint serves a file on GET and actions on POST.
	if status, _ := controlRequest(t, d, http.MethodDelete, "/v1/control/inbound/abc/accept", nil); status != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE inbound action = %d, want 405", status)
	}
}

// A body that is not JSON, or is larger than the endpoint allows, is a 400 that
// says "invalid json" and never reaches the bridge.
func TestControlEndpointsRejectInvalidOrOversizedJSON(t *testing.T) {
	d := startForHandlers(t)
	limits := map[string]int{
		"/v1/control/release":            8 * 1024,
		"/v1/control/persistent-session": 4 * 1024,
		"/v1/control/arm":                bridge.MaxImageBytes * 2,
		"/v1/control/snapshot":           bridge.MaxImageBytes * 2,
	}
	for path, limit := range limits {
		t.Run(path+"/malformed", func(t *testing.T) {
			status, body := controlRequest(t, d, http.MethodPost, path, []byte("{not json"))
			if status != http.StatusBadRequest || strings.TrimSpace(body) != "invalid json" {
				t.Fatalf("malformed body = %d %q, want 400 \"invalid json\"", status, body)
			}
		})
		t.Run(path+"/oversized", func(t *testing.T) {
			// One byte over the limit in total, framing included.
			framing := len(`{"padding":""}`)
			huge := []byte(`{"padding":"` + strings.Repeat("a", limit+1-framing) + `"}`)
			status, body := controlRequest(t, d, http.MethodPost, path, huge)
			if status != http.StatusBadRequest || strings.TrimSpace(body) != "invalid json" {
				t.Fatalf("oversized body = %d %q, want 400 \"invalid json\"", status, body)
			}
		})
	}
}

// A valid value followed by more data is not a valid request either.
func TestControlEndpointsRejectTrailingData(t *testing.T) {
	d := startForHandlers(t)
	for _, body := range []string{`{"current_version":"v1"} {}`, `{"current_version":"v1"} x`} {
		status, reply := controlRequest(t, d, http.MethodPost, "/v1/control/release", []byte(body))
		if status != http.StatusBadRequest || strings.TrimSpace(reply) != "invalid json" {
			t.Fatalf("body %q = %d %q, want 400 \"invalid json\"", body, status, reply)
		}
	}
}

func TestControlEndpointsKeepTheirSpecificValidationMessages(t *testing.T) {
	d := startForHandlers(t)
	if status, body := controlRequest(t, d, http.MethodPost, "/v1/control/release", []byte(`{}`)); status != http.StatusBadRequest || !strings.Contains(body, "current_version is required") {
		t.Fatalf("release without a version = %d %q", status, body)
	}
	if status, body := controlRequest(t, d, http.MethodPost, "/v1/control/arm", []byte(`{"png":"%%%","width":1,"height":1}`)); status != http.StatusBadRequest || !strings.Contains(body, "png must be base64") {
		t.Fatalf("arm with bad base64 = %d %q", status, body)
	}
	if status, _ := controlRequest(t, d, http.MethodPost, "/v1/control/persistent-session", []byte(`{"id":"","token":""}`)); status != http.StatusBadRequest {
		t.Fatalf("persistent session without credentials = %d, want 400", status)
	}
}

// The control plane carries a path and nothing else about the file: the bridge
// measures size, time and SHA-256 itself, so a caller cannot vouch for a file
// with numbers of its own.
func TestSnapshotArmsFilesFromThePathAlone(t *testing.T) {
	d := startForHandlers(t)
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A leftover "file" object with made-up metadata is ignored, not trusted.
	body := fmt.Sprintf(`{"items":[{"kind":"file","path":%q,"file":{"Path":%q,"Size":1,"SHA256":"00"}}]}`, path, path)
	status, reply := controlRequest(t, d, http.MethodPost, "/v1/control/snapshot", []byte(body))
	if status != http.StatusOK {
		t.Fatalf("snapshot = %d %q", status, reply)
	}
	if !strings.Contains(reply, `"size":5`) || !strings.Contains(reply, "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824") {
		t.Fatalf("metadata was not measured from the file: %s", reply)
	}
}

func TestSnapshotRefusesFilePathsThatAreNotOneAbsoluteRegularFile(t *testing.T) {
	d := startForHandlers(t)
	dir := t.TempDir()
	regular := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathSeparator) // Join would clean the ".." away
	paths := []string{"", "a.txt", dir + sep + ".." + sep + filepath.Base(dir) + sep + "a.txt", dir, filepath.Join(dir, "missing")}
	link := filepath.Join(dir, "link")
	if os.Symlink(regular, link) == nil {
		paths = append(paths, link)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			body := fmt.Sprintf(`{"items":[{"kind":"file","path":%q}]}`, path)
			if status, reply := controlRequest(t, d, http.MethodPost, "/v1/control/snapshot", []byte(body)); status != http.StatusBadRequest {
				t.Fatalf("path %q = %d %q, want 400", path, status, reply)
			}
		})
	}
}
