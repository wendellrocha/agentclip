package companion

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestControlServerServesPrivateStatusViewAndStop(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	stopped := make(chan struct{}, 1)
	control, err := StartControl("dev", func() any {
		return map[string]any{"profile": "dev", "tunnel": map[string]bool{"connected": true}}
	}, func() { stopped <- struct{}{} }, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	state, err := LoadRuntime("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !RuntimeHealthy(state) {
		t.Fatal("runtime health check failed")
	}
	payload, err := FetchRuntimeStatus(state)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(payload, &status); err != nil {
		t.Fatal(err)
	}
	if status["profile"] != "dev" {
		t.Fatalf("status = %#v", status)
	}
	response, err := http.Get(ViewURL(state))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("view status = %d", response.StatusCode)
	}
	markup, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markup), "AgentClip Companion") {
		t.Fatal("dashboard markup missing")
	}
	for _, expected := range []string{"btn-accept", "btn-reject", "Abrir conteúdo", "Baixar", "Copiar caminho", "overflow-wrap:anywhere", "Oferta recebida em", "Recebido em", "Recebidos recentemente", "formatDateTime", "formatBytes"} {
		if !strings.Contains(string(markup), expected) {
			t.Fatalf("dashboard markup missing %q", expected)
		}
	}
	if err := StopRuntime(state); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop callback was not called")
	}
}

func TestControlServerAllowsInboundActionFromPrivateView(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	actions := make(chan string, 1)
	control, err := StartControl("dev", func() any { return map[string]any{} }, func() {}, func(action, offerID string) error {
		actions <- action + ":" + offerID
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	request, err := http.NewRequest(http.MethodPost, ViewURL(control.State)+"api/inbound/offer-1/accept", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("action status = %d", response.StatusCode)
	}
	select {
	case action := <-actions:
		if action != "accept:offer-1" {
			t.Fatalf("action = %q", action)
		}
	case <-time.After(time.Second):
		t.Fatal("inbound action was not called")
	}
}

func TestControlServerServesPrivateInboundTextContent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	control, err := StartControl("dev", func() any { return map[string]any{} }, func() {}, nil, func(offerID string) (InboundFileContent, error) {
		if offerID != "offer-1" {
			t.Fatalf("offer ID = %q", offerID)
		}
		return InboundFileContent{Name: "report.csv", Size: 8, Previewable: true, Reader: io.NopCloser(strings.NewReader("a,b\n1,2\n"))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	response, err := http.Get(ViewURL(control.State) + "api/inbound/offer-1/content")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("content response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	data, err := io.ReadAll(response.Body)
	if err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("content = %q, %v", data, err)
	}
	download, err := http.Get(ViewURL(control.State) + "api/inbound/offer-1/download")
	if err != nil {
		t.Fatal(err)
	}
	defer download.Body.Close()
	if download.StatusCode != http.StatusOK || !strings.HasPrefix(download.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download response = %d %q", download.StatusCode, download.Header.Get("Content-Disposition"))
	}
	if data, err := io.ReadAll(download.Body); err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("download = %q, %v", data, err)
	}
}
