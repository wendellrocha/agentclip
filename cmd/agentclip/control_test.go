package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/daemon"
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
		if got := validLoopbackAddress(address); got != want {
			t.Errorf("validLoopbackAddress(%q) = %v, want %v", address, got, want)
		}
	}
}

func TestStatePort(t *testing.T) {
	if got := statePort(daemon.State{Address: "127.0.0.1:4321"}); got != 4321 {
		t.Fatalf("statePort = %d, want 4321", got)
	}
	if got := statePort(daemon.State{Address: "garbage"}); got != 0 {
		t.Fatalf("statePort of invalid address = %d, want 0", got)
	}
}

func TestRandomTokenAndPort(t *testing.T) {
	first, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 43 {
		t.Fatalf("tokens must be unique and long enough: %q %q", first, second)
	}
	for i := 0; i < 200; i++ {
		port, err := randomPort()
		if err != nil {
			t.Fatal(err)
		}
		if port < remotePortMin || port > remotePortMax {
			t.Fatalf("port %d outside [%d, %d]", port, remotePortMin, remotePortMax)
		}
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
	if err := controlGet(state, "/v1/control/x", &got); err != nil || !got.OK {
		t.Fatalf("controlGet = %+v, %v", got, err)
	}
	if gotAuth != "Bearer control-secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}

	got.OK = false
	if err := controlPost(state, "/v1/control/x", []byte(`{"a":1}`), &got); err != nil || !got.OK {
		t.Fatalf("controlPost = %+v, %v", got, err)
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
		"get":  controlGet(state, "/v1/control/x", &struct{}{}),
		"post": controlPost(state, "/v1/control/x", nil, nil),
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

	if err := controlGet(state, "/", &struct{}{}); err == nil {
		t.Error("controlGet must refuse a non-loopback address")
	}
	if err := controlPost(state, "/", nil, nil); err == nil {
		t.Error("controlPost must refuse a non-loopback address")
	}
	if _, err := controlInboundText(state, "offer"); err == nil {
		t.Error("controlInboundText must refuse a non-loopback address")
	}
	if bridgeHealthy(state) {
		t.Error("bridgeHealthy must refuse a non-loopback address")
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

	if err := controlInboundAction(state, "offer-1", "delete"); err == nil {
		t.Fatal("unknown action must be rejected before any request")
	}
	if path != "" {
		t.Fatalf("unexpected request to %q", path)
	}
	if err := controlInboundAction(state, "offer-1", "accept"); err != nil || path != "/v1/control/inbound/offer-1/accept" {
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

	content, err := controlInboundText(state, "ok")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Reader.Close()
	data, _ := io.ReadAll(content.Reader)
	if content.Name != "report.csv" || !content.Previewable || content.Size != 4 || string(data) != "a,b\n" {
		t.Fatalf("content = %+v %q", content, data)
	}
	for _, id := range []string{"missing", "nameless", " "} {
		if _, err := controlInboundText(state, id); err == nil {
			t.Errorf("offer %q must be unavailable", id)
		}
	}
}

func TestCompanionInboundStatusFallsBackToEmptyOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()
	status := companionInboundStatus(stateFor(server, "t"))
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
	if !bridgeHealthy(state) {
		t.Fatal("bridge must be healthy")
	}
	healthy = false
	if bridgeHealthy(state) {
		t.Fatal("bridge must be unhealthy on 503")
	}
}

func TestHasRemoteUpgradeFailure(t *testing.T) {
	if hasRemoteUpgradeFailure(nil) {
		t.Fatal("no results means no failure")
	}
	ok := []remoteUpgradeResult{{Profile: "a"}}
	if hasRemoteUpgradeFailure(ok) {
		t.Fatal("successful results must not fail")
	}
	failed := append(ok, remoteUpgradeResult{Profile: "b", Err: errors.New("ssh")})
	if !hasRemoteUpgradeFailure(failed) {
		t.Fatal("a failed remote must be reported")
	}
}
