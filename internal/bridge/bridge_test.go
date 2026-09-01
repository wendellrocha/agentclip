package bridge

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func pngFixture(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func testSession(t *testing.T, b *Bridge) (string, string) {
	t.Helper()
	if _, err := b.Arm(pngFixture(t), 2, 2); err != nil {
		t.Fatal(err)
	}
	s, token, err := b.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return s.ID, token
}

func requestImage(h http.Handler, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/v1/image", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestImageConsumedOnceConcurrently(t *testing.T) {
	b := New(time.Minute)
	_, token := testSession(t, b)
	h := b.Handler()
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- requestImage(h, token).Code }()
	}
	wg.Wait()
	close(results)
	var ok, consumed int
	for code := range results {
		if code == http.StatusOK {
			ok++
		}
		if code == http.StatusGone {
			consumed++
		}
	}
	if ok != 1 || consumed != 1 {
		t.Fatalf("statuses: ok=%d consumed=%d", ok, consumed)
	}
}

func TestInvalidToken(t *testing.T) {
	b := New(time.Minute)
	testSession(t, b)
	w := requestImage(b.Handler(), "wrong")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", w.Code)
	}
}

func TestImageTTL(t *testing.T) {
	b := New(10 * time.Millisecond)
	if _, err := b.Arm(pngFixture(t), 2, 2); err != nil {
		t.Fatal(err)
	}
	_, token, err := b.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	w := requestImage(b.Handler(), token)
	if w.Code != http.StatusGone {
		t.Fatalf("got %d", w.Code)
	}
}

func TestOldSessionCannotReadNewArm(t *testing.T) {
	b := New(time.Minute)
	_, token := testSession(t, b)
	if _, err := b.Arm(pngFixture(t), 2, 2); err != nil {
		t.Fatal(err)
	}
	w := requestImage(b.Handler(), token)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", w.Code)
	}
}

func TestPersistentSessionReadsImageArmedAfterRegistration(t *testing.T) {
	b := New(time.Minute)
	if err := b.RegisterPersistentSession("companion", "pair-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Arm(pngFixture(t), 2, 2); err != nil {
		t.Fatal(err)
	}
	w := requestImage(b.Handler(), "pair-token")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want %d", w.Code, http.StatusOK)
	}
}

func TestFileItemStreamsOnceWithoutLeakingHostPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vendas.csv")
	contents := []byte("month,total\n2026-08,42\n")
	if err := os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}
	item, err := FileItem(path, "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	b := New(time.Minute)
	snapshot, err := b.ArmItems([]Item{item})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := b.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	statusRequest.Header.Set("Authorization", "Bearer "+token)
	statusResponse := httptest.NewRecorder()
	b.Handler().ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status = %d", statusResponse.Code)
	}
	if strings.Contains(statusResponse.Body.String(), path) {
		t.Fatal("status leaked local file path")
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/items/"+snapshot.Items[0].ID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	b.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/csv" || !bytes.Equal(response.Body.Bytes(), contents) {
		t.Fatalf("file response = %d %q %q", response.Code, response.Header().Get("Content-Type"), response.Body.Bytes())
	}
	again := httptest.NewRecorder()
	b.Handler().ServeHTTP(again, request)
	if again.Code != http.StatusGone {
		t.Fatalf("second file response = %d", again.Code)
	}
}

func TestChangedFileIsRejectedBeforeTransfer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vendas.csv")
	if err := os.WriteFile(path, []byte("a,b\n1,2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := FileItem(path, "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	b := New(time.Minute)
	snapshot, err := b.ArmItems([]Item{item})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := b.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("a,b\n100,200\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/items/"+snapshot.Items[0].ID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	b.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), path) {
		t.Fatalf("changed file response = %d %s", response.Code, response.Body.String())
	}
}

func TestTextItemIsIndependentFromFileAndImageEndpoints(t *testing.T) {
	b := New(time.Minute)
	snapshot, err := b.ArmItems([]Item{{Kind: ItemText, MIMEType: "text/plain; charset=utf-8", Data: []byte("olá")}})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := b.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/items/"+snapshot.Items[0].ID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	b.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/plain; charset=utf-8" || response.Body.String() != "olá" {
		t.Fatalf("text response = %d %q %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}
