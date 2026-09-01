package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/bridge"
)

func validPNG(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestControlEndpointsRequireTokenAndArm(t *testing.T) {
	d, err := Start(nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	url := "http://" + d.State.Address + "/v1/control/arm"
	body := `{"png":"` + base64.StdEncoding.EncodeToString(validPNG(t)) + `","width":2,"height":3}`
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized: %v %v", err, res)
	}
	req, _ = http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("arm status %d", res.StatusCode)
	}
}

func TestPersistentSessionCanReadLaterImage(t *testing.T) {
	d, err := Start(nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	baseURL := "http://" + d.State.Address
	register, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/control/persistent-session", strings.NewReader(`{"id":"companion:dev","token":"pair-token"}`))
	register.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(register)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("register status %d", response.StatusCode)
	}
	armBody := `{"png":"` + base64.StdEncoding.EncodeToString(validPNG(t)) + `","width":2,"height":3}`
	arm, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/control/arm", strings.NewReader(armBody))
	arm.Header.Set("Authorization", "Bearer secret")
	response, err = http.DefaultClient.Do(arm)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("arm status %d", response.StatusCode)
	}
	image, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/image", nil)
	image.Header.Set("Authorization", "Bearer pair-token")
	response, err = http.DefaultClient.Do(image)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("image status %d", response.StatusCode)
	}
}

func TestSnapshotControlArmsLocalFileReference(t *testing.T) {
	d, err := Start(nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	path := filepath.Join(t.TempDir(), "fixture.csv")
	if err := os.WriteFile(path, []byte("a,b\n1,2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := bridge.FileItem(path, "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(snapshotRequest{Items: []snapshotItemRequest{{Kind: item.Kind, MIMEType: item.MIMEType, Name: item.Name, File: item.File}}})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "http://"+d.State.Address+"/v1/control/snapshot", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", response.StatusCode)
	}
	_, token, err := d.Bridge.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	status := httptest.NewRecorder()
	remoteRequest := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	remoteRequest.Header.Set("Authorization", "Bearer "+token)
	d.Bridge.Handler().ServeHTTP(status, remoteRequest)
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), path) || !strings.Contains(status.Body.String(), "fixture.csv") {
		t.Fatalf("bridge status = %d %s", status.Code, status.Body.String())
	}
}

func TestInboundTextContentRequiresControlToken(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	d, err := Start(nil, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Bridge.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	contents := []byte("a,b\n1,2\n")
	hash := sha256.Sum256(contents)
	offer, err := d.Bridge.CreateInboundOffer("companion:dev", "report.csv", int64(len(contents)), hex.EncodeToString(hash[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Bridge.AcceptInboundOffer(offer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Bridge.DeliverInboundOffer("companion:dev", offer.ID, bytes.NewReader(contents), int64(len(contents))); err != nil {
		t.Fatal(err)
	}
	url := "http://" + d.State.Address + "/v1/control/inbound/" + offer.ID + "/file"
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized content status = %d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/octet-stream" || response.Header.Get("X-AgentClip-Previewable") != "true" {
		t.Fatalf("content status = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if data, err := io.ReadAll(response.Body); err != nil || !bytes.Equal(data, contents) {
		t.Fatalf("content = %q, %v", data, err)
	}
}
