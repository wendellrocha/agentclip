package control

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/bridge"
	"github.com/wendellrocha/agentclip/internal/daemon"
	"github.com/wendellrocha/agentclip/internal/release"
	"github.com/wendellrocha/agentclip/internal/testenv"
)

func stateFor(server *httptest.Server, token string) daemon.State {
	return daemon.State{Address: strings.TrimPrefix(server.URL, "http://"), ControlToken: token}
}

func TestValidLoopbackAddressOnlyAcceptsIPv4Loopback(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1:8080":  true,
		"127.0.0.1:65535": true,
		"127.0.0.1:0":     false,
		"127.0.0.1:65536": false,
		"127.0.0.1:abc":   false,
		"localhost:8080":  false,
		"0.0.0.0:8080":    false,
		"10.0.0.5:8080":   false,
		"evil.example:80": false,
		"127.0.0.1":       false,
		"":                false,
	} {
		if got := ValidLoopbackAddress(address); got != want {
			t.Errorf("ValidLoopbackAddress(%q) = %v, want %v", address, got, want)
		}
	}
}

func TestStatePort(t *testing.T) {
	if got := Port(daemon.State{Address: "127.0.0.1:4321"}); got != 4321 {
		t.Fatalf("Port = %d, want 4321", got)
	}
	if got := Port(daemon.State{Address: "garbage"}); got != 0 {
		t.Fatalf("Port of invalid address = %d, want 0", got)
	}
}

func TestControlClientSendsBearerTokenAndDecodesResponses(t *testing.T) {
	var gotAuth, gotBody, gotType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	state := stateFor(server, "control-secret")

	var got struct{ OK bool }
	if err := Get(state, "/v1/control/x", &got); err != nil || !got.OK {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if gotAuth != "Bearer control-secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}

	got.OK = false
	if err := Post(state, "/v1/control/x", []byte(`{"a":1}`), &got); err != nil || !got.OK {
		t.Fatalf("Post = %+v, %v", got, err)
	}
	if gotBody != `{"a":1}` || gotType != "application/json" {
		t.Fatalf("body = %q, content type = %q", gotBody, gotType)
	}
}

func TestControlClientReportsHTTPErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusConflict)
	}))
	defer server.Close()
	state := stateFor(server, "t")

	for name, err := range map[string]error{
		"get":  Get(state, "/v1/control/x", &struct{}{}),
		"post": Post(state, "/v1/control/x", nil, nil),
	} {
		if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "nope") {
			t.Errorf("%s error = %v, want HTTP 409 with body", name, err)
		}
	}
}

func TestControlClientRefusesNonLoopbackAddresses(t *testing.T) {
	var contacted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted = true }))
	defer server.Close()
	// "localhost" resolves to the test server but is not the accepted literal.
	state := daemon.State{Address: strings.Replace(strings.TrimPrefix(server.URL, "http://"), "127.0.0.1", "localhost", 1), ControlToken: "secret"}

	if err := Get(state, "/", &struct{}{}); err == nil {
		t.Error("Get must refuse a non-loopback address")
	}
	if err := Post(state, "/", nil, nil); err == nil {
		t.Error("Post must refuse a non-loopback address")
	}
	if _, err := InboundText(state, "offer"); err == nil {
		t.Error("InboundText must refuse a non-loopback address")
	}
	if Healthy(state) {
		t.Error("Healthy must refuse a non-loopback address")
	}
	if contacted {
		t.Fatal("the control token must never reach a non-loopback address")
	}
}

func TestControlInboundActionValidatesTheAction(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	state := stateFor(server, "t")

	if err := InboundAction(state, "offer-1", "delete"); err == nil {
		t.Fatal("unknown action must be rejected before any request")
	}
	if path != "" {
		t.Fatalf("unexpected request to %q", path)
	}
	if err := InboundAction(state, "offer-1", "accept"); err != nil || path != "/v1/control/inbound/offer-1/accept" {
		t.Fatalf("accept: err=%v path=%q", err, path)
	}
}

func TestControlInboundTextParsesFilenameAndPreviewHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing/file") {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/nameless/file") {
			w.Header().Set("Content-Length", "2")
			_, _ = w.Write([]byte("hi"))
			return
		}
		w.Header().Set("Content-Disposition", `inline; filename="report.csv"`)
		w.Header().Set("X-AgentClip-Previewable", "true")
		_, _ = w.Write([]byte("a,b\n"))
	}))
	defer server.Close()
	state := stateFor(server, "t")

	content, err := InboundText(state, "ok")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Reader.Close()
	data, _ := io.ReadAll(content.Reader)
	if content.Name != "report.csv" || !content.Previewable || content.Size != 4 || string(data) != "a,b\n" {
		t.Fatalf("content = %+v %q", content, data)
	}
	for _, id := range []string{"missing", "nameless", " "} {
		if _, err := InboundText(state, id); err == nil {
			t.Errorf("offer %q must be unavailable", id)
		}
	}
}

func TestCompanionInboundStatusFallsBackToEmptyOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	status := InboundStatus(stateFor(server, "t"))
	if len(status.Offers) != 0 || len(status.Received) != 0 {
		t.Fatalf("status = %+v, want empty", status)
	}
}

func TestBridgeHealthy(t *testing.T) {
	healthy := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	state := stateFor(server, "t")
	if !Healthy(state) {
		t.Fatal("bridge must be healthy")
	}
	healthy = false
	if Healthy(state) {
		t.Fatal("bridge must be unhealthy on 503")
	}
}

// The tests below run the client against the real daemon, so a payload the
// client sends is accepted by the handler that receives it.

func startDaemon(t *testing.T) (*daemon.Daemon, daemon.State) {
	t.Helper()
	testenv.IsolateUserDirs(t)
	d, err := daemon.Start(nil, "control-secret")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, d.State
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestClientAgainstTheRealDaemon(t *testing.T) {
	_, state := startDaemon(t)

	if !Healthy(state) {
		t.Fatal("a running daemon must be healthy")
	}
	if Port(state) == 0 {
		t.Fatalf("Port(%q) = 0", state.Address)
	}

	armed, err := Arm(state, daemon.Image{PNG: tinyPNG(t), Width: 4, Height: 3})
	if err != nil || armed.ID == "" || armed.ExpiresAt.IsZero() {
		t.Fatalf("Arm = %+v, %v", armed, err)
	}

	session, err := Session(state)
	if err != nil || session.ID == "" || session.Token == "" {
		t.Fatalf("Session = %+v, %v", session, err)
	}

	items := []bridge.Item{{ID: "text-1", Kind: bridge.ItemText, MIMEType: "text/plain", Name: "note.txt", Data: []byte("hello")}}
	if err := ArmSnapshot(state, items); err != nil {
		t.Fatalf("ArmSnapshot: %v", err)
	}

	if err := PublishReleaseStatus(state, release.Status{CurrentVersion: "v0.7.1", LatestVersion: "v0.8.0", UpdateAvailable: true}); err != nil {
		t.Fatalf("PublishReleaseStatus: %v", err)
	}
	if err := PublishReleaseStatus(state, release.Status{}); err == nil {
		t.Fatal("the daemon must refuse a release status without a current version")
	}

	status := InboundStatus(state)
	if len(status.Offers) != 0 || len(status.Received) != 0 {
		t.Fatalf("a fresh daemon has no inbound files: %+v", status)
	}
}

func TestClientIsRejectedWithAWrongControlToken(t *testing.T) {
	_, state := startDaemon(t)
	state.ControlToken = "not-the-token"
	if _, err := Session(state); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("Session with a wrong token = %v, want HTTP 401", err)
	}
	if err := ArmSnapshot(state, nil); err == nil {
		t.Fatal("ArmSnapshot with a wrong token must fail")
	}
}

func TestShutdownStopsTheDaemon(t *testing.T) {
	_, state := startDaemon(t)
	if err := Shutdown(state); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for Healthy(state) {
		if time.Now().After(deadline) {
			t.Fatal("the daemon is still healthy after Shutdown")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
