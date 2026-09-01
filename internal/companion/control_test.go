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
	}, func() { stopped <- struct{}{} })
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
	if err := StopRuntime(state); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop callback was not called")
	}
}
