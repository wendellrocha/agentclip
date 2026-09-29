package companion

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

type trackedReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackedReadCloser) Close() error {
	r.closed = true
	return nil
}

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
	// The renderer lives in its own script, served next to the page.
	scriptResponse, err := http.Get(ViewURL(control.State) + "assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer scriptResponse.Body.Close()
	script, err := io.ReadAll(scriptResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	markup = append(markup, script...)
	for _, expected := range []string{"btn-accept", "btn-reject", "Abrir conteúdo", "Copiar conteúdo", "Baixar", "Copiar caminho", "Aguardando sua aprovação", "Recebido em", "Arquivos recebidos", "formatDateTime", "formatBytes", "Atualização", "agentclip upgrade"} {
		if !strings.Contains(string(markup), expected) {
			t.Fatalf("dashboard markup missing %q", expected)
		}
	}
	fileCardStart := strings.Index(string(markup), "function fileCard(o){")
	if fileCardStart < 0 {
		t.Fatal("dashboard file card renderer missing")
	}
	fileCardEnd := strings.Index(string(markup)[fileCardStart:], "async function refresh()")
	if fileCardEnd < 0 {
		t.Fatal("dashboard file card renderer boundary missing")
	}
	fileCard := string(markup)[fileCardStart : fileCardStart+fileCardEnd]
	previewBranch := strings.Index(fileCard, "o.previewable?")
	copyButton := strings.Index(fileCard, ">Copiar conteúdo</button>")
	if previewBranch < 0 || copyButton < 0 {
		t.Fatal("previewable file copy action missing")
	}
	previewBranchEnd := strings.Index(fileCard[previewBranch:], "':'')")
	if previewBranchEnd < 0 || copyButton < previewBranch || copyButton > previewBranch+previewBranchEnd {
		t.Fatal("Copiar conteúdo must be rendered only inside the previewable file branch")
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

func TestControlServerServesDashboardBrandAssets(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	control, err := StartControl("dev", func() any { return map[string]any{} }, func() {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	for _, asset := range []struct {
		path        string
		contentType string
	}{
		{"assets/agentclip-mark.svg", "image/svg+xml"},
		{"assets/favicon.svg", "image/svg+xml"},
		{"assets/styles.css", "text/css; charset=utf-8"},
		{"assets/app.js", "text/javascript; charset=utf-8"},
	} {
		response, err := http.Get(ViewURL(control.State) + asset.path)
		if err != nil {
			t.Fatalf("get %s: %v", asset.path, err)
		}
		data, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", asset.path, readErr)
		}
		if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != asset.contentType || len(data) == 0 {
			t.Fatalf("asset %s response = %d %q (%d bytes)", asset.path, response.StatusCode, response.Header.Get("Content-Type"), len(data))
		}
	}
}

// File names come from the remote server, so the page must not depend on inline
// code: with 'self' only, an injected <script>, event handler or style attribute
// would not run even if the manual escaping missed a field.
func TestDashboardCSPForbidsInlineCodeAndThePageNeedsNone(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	control, err := StartControl("dev", func() any { return map[string]any{} }, func() {}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	response, err := http.Get(ViewURL(control.State))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	response.Body.Close()
	policy := response.Header.Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self'", "style-src 'self'", "object-src 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("CSP %q lacks %q", policy, want)
		}
	}
	for _, forbidden := range []string{"unsafe-inline", "unsafe-eval", "data:  script", "*"} {
		if strings.Contains(strings.ReplaceAll(policy, "img-src 'self' data:", ""), forbidden) {
			t.Errorf("CSP %q contains %q", policy, forbidden)
		}
	}

	html := string(page)
	if strings.Contains(html, "{{BASE}}") {
		t.Error("the page still holds an unreplaced {{BASE}}")
	}
	if !strings.Contains(html, `<script src="`+strings.TrimSuffix(strings.TrimPrefix(ViewURL(control.State), "http://"+control.State.Address), "/")+`/assets/app.js">`) {
		t.Errorf("the page does not load its script from its own assets:\n%s", html)
	}
	inline := regexp.MustCompile(`(?i)<script>|<script\s+[^>]*>\s*[^<\s]|<style|\sstyle=|\son[a-z]+=`)
	if match := inline.FindString(html); match != "" {
		t.Errorf("the page contains inline code (%q)", match)
	}

	// The markup the script builds is subject to the same policy.
	script, err := dashboardFiles.ReadFile("ui/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if match := regexp.MustCompile(`(?i)\sstyle=|\son(click|error|load|mouse[a-z]*)=|javascript:|setAttribute\(.style`).FindString(string(script)); match != "" {
		t.Errorf("app.js builds markup the policy would block (%q)", match)
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

func TestControlServerDoesNotServeNonPreviewableInboundContent(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	reader := &trackedReadCloser{Reader: strings.NewReader("data")}
	control, err := StartControl("dev", func() any { return map[string]any{} }, func() {}, nil, func(string) (InboundFileContent, error) {
		return InboundFileContent{Name: "image.png", Size: 4, Previewable: false, Reader: reader}, nil
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
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-previewable content status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
	if !reader.closed {
		t.Fatal("rejected non-previewable content reader was not closed")
	}
}
