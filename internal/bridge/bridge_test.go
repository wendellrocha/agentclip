package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/release"
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

func TestReleaseStatusDoesNotRequireAnArmedClipboard(t *testing.T) {
	b := New(time.Minute)
	if err := b.RegisterPersistentSession("companion", "pair-token"); err != nil {
		t.Fatal(err)
	}
	b.SetReleaseStatus(release.Status{CurrentVersion: "1.0.0", LatestVersion: "v1.1.0", UpdateAvailable: true, CheckedAt: time.Now().UTC()})
	request := httptest.NewRequest(http.MethodGet, "/v1/release", nil)
	request.Header.Set("Authorization", "Bearer pair-token")
	response := httptest.NewRecorder()
	b.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"update_available":true`) || strings.Contains(response.Body.String(), "clipboard") {
		t.Fatalf("release status = %d %s", response.Code, response.Body.String())
	}
	unauthorized := httptest.NewRecorder()
	b.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/release", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
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

func TestInboundLocalStatusHidesApprovedOffer(t *testing.T) {
	b := New(time.Minute)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	offer, err := b.CreateInboundOffer("companion:dev", "report.csv", 3, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if got := b.InboundLocalStatus(); len(got.Offers) != 1 {
		t.Fatalf("pending offers = %#v, want one", got.Offers)
	}
	if _, err := b.AcceptInboundOffer(offer.ID); err != nil {
		t.Fatal(err)
	}
	if got := b.InboundLocalStatus(); len(got.Offers) != 0 {
		t.Fatalf("approved offer must not remain actionable: %#v", got.Offers)
	}
}

func TestInboundLocalStatusShowsNewestOffersAndDeliveriesFirst(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	b := New(time.Minute)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	emptyHash := sha256.Sum256(nil)
	checksum := hex.EncodeToString(emptyHash[:])
	older, err := b.CreateInboundOffer("companion:dev", "older.csv", 0, checksum)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	newer, err := b.CreateInboundOffer("companion:dev", "newer.csv", 0, checksum)
	if err != nil {
		t.Fatal(err)
	}
	status := b.InboundLocalStatus()
	if len(status.Offers) != 2 || status.Offers[0].ID != newer.ID || status.Offers[1].ID != older.ID {
		t.Fatalf("offers = %#v, want newest first", status.Offers)
	}

	for _, offer := range []InboundOffer{older, newer} {
		if _, err := b.AcceptInboundOffer(offer.ID); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
		if _, err := b.DeliverInboundOffer("companion:dev", offer.ID, strings.NewReader(""), 0); err != nil {
			t.Fatal(err)
		}
	}
	status = b.InboundLocalStatus()
	if len(status.Received) != 2 || status.Received[0].ID != newer.ID || status.Received[1].ID != older.ID {
		t.Fatalf("received = %#v, want newest first", status.Received)
	}
}

func TestOpenInboundTextFileAllowsCSVAndRejectsBinary(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	b := New(time.Minute)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	contents := []byte("month,total\n2026-09,84\n")
	checksum := sha256.Sum256(contents)
	offer, err := b.CreateInboundOffer("companion:dev", "report.csv", int64(len(contents)), hex.EncodeToString(checksum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AcceptInboundOffer(offer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DeliverInboundOffer("companion:dev", offer.ID, bytes.NewReader(contents), int64(len(contents))); err != nil {
		t.Fatal(err)
	}
	file, opened, err := b.OpenInboundTextFile(offer.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(data, contents) || opened.Name != "report.csv" {
		t.Fatalf("opened file = %q %#v %v", data, opened, err)
	}

	binary := []byte{0, 1, 2}
	binaryHash := sha256.Sum256(binary)
	binaryOffer, err := b.CreateInboundOffer("companion:dev", "report.png", int64(len(binary)), hex.EncodeToString(binaryHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AcceptInboundOffer(binaryOffer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DeliverInboundOffer("companion:dev", binaryOffer.ID, bytes.NewReader(binary), int64(len(binary))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.OpenInboundTextFile(binaryOffer.ID); err == nil {
		t.Fatal("binary file unexpectedly opened as text")
	}
	binaryFile, openedBinary, err := b.OpenInboundFile(binaryOffer.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer binaryFile.Close()
	if data, err := io.ReadAll(binaryFile); err != nil || !bytes.Equal(data, binary) || openedBinary.Name != "report.png" {
		t.Fatalf("opened binary = %q %#v %v", data, openedBinary, err)
	}
}

func TestInboundOffersAreCappedPerProfile(t *testing.T) {
	b := New(time.Minute)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	checksum := strings.Repeat("a", 64)
	for i := 0; i < MaxOpenInboundOffers; i++ {
		if _, err := b.CreateInboundOffer("companion:dev", "report.csv", 1, checksum); err != nil {
			t.Fatalf("offer %d: %v", i, err)
		}
	}
	if _, err := b.CreateInboundOffer("companion:dev", "report.csv", 1, checksum); err == nil {
		t.Fatal("offer beyond the cap must be rejected")
	}
}

func TestSettledInboundOffersArePruned(t *testing.T) {
	b := New(time.Minute)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	checksum := strings.Repeat("a", 64)
	rejected, err := b.CreateInboundOffer("companion:dev", "a.csv", 1, checksum)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.RejectInboundOffer(rejected.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateInboundOffer("companion:dev", "b.csv", 1, checksum); err != nil {
		t.Fatal(err)
	}
	now = now.Add(InboundOfferTTL + InboundRetention)
	b.InboundLocalStatus()
	if len(b.inbound) != 0 {
		t.Fatalf("settled offers leaked: %d", len(b.inbound))
	}
}

type recordingLogger struct {
	mu     sync.Mutex
	events []string
}

func (l *recordingLogger) Info(message string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, fmt.Sprintf(message, args...))
}

func (l *recordingLogger) Error(message string, args ...any) { l.Info(message, args...) }

func TestSecurityEventsAreLoggedWithoutSecrets(t *testing.T) {
	b := New(time.Minute)
	logger := &recordingLogger{}
	b.SetLogger(logger)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request.Header.Set("Authorization", "Bearer wrong-secret")
	response := httptest.NewRecorder()
	b.Handler().ServeHTTP(response, request)
	offer, err := b.CreateInboundOffer("companion:dev", "report.csv", 3, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.RejectInboundOffer(offer.ID); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(logger.events, "\n")
	for _, want := range []string{"request rejected: status=401 code=UNAUTHORIZED", "inbound offer created: id=" + offer.ID, "inbound offer rejected: id=" + offer.ID} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing event %q in:\n%s", want, joined)
		}
	}
	for _, secret := range []string{"wrong-secret", "read-token", "upload-token", "report.csv"} {
		if strings.Contains(joined, secret) {
			t.Errorf("log leaked %q", secret)
		}
	}
}

func TestRejectionLogsAreRateLimitedAndSummarized(t *testing.T) {
	b := New(time.Minute)
	logger := &recordingLogger{}
	b.SetLogger(logger)
	now := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }

	reject := func() {
		request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
		request.Header.Set("Authorization", "Bearer nope")
		b.Handler().ServeHTTP(httptest.NewRecorder(), request)
	}
	for i := 0; i < 100; i++ {
		reject()
	}
	if got := len(logger.events); got != rejectionLogBurst {
		t.Fatalf("logged %d rejections within a window, want %d", got, rejectionLogBurst)
	}

	now = now.Add(rejectionLogWindow)
	reject()
	joined := strings.Join(logger.events, "\n")
	if !strings.Contains(joined, "request rejections suppressed: code=UNAUTHORIZED count=95") {
		t.Fatalf("missing suppression summary in:\n%s", joined)
	}
}

func TestLogValuesCannotForgeLines(t *testing.T) {
	b := New(time.Minute)
	logger := &recordingLogger{}
	b.SetLogger(logger)
	b.logEvent("value=%s", "a\nfake [INFO] line\r")
	if len(logger.events) != 1 || strings.ContainsAny(logger.events[0], "\r\n") {
		t.Fatalf("events = %q", logger.events)
	}
}

func TestFailedInboundDeliveryDoesNotLeakHostPaths(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	t.Setenv("LocalAppData", cache)
	root, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache directory on this platform")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	// A regular file where the AgentClip cache directory belongs makes the
	// write fail with an error that embeds the full host path.
	if err := os.WriteFile(filepath.Join(root, "agentclip"), nil, 0600); err != nil {
		t.Fatal(err)
	}

	b := New(time.Minute)
	logger := &recordingLogger{}
	b.SetLogger(logger)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	emptyHash := sha256.Sum256(nil)
	offer, err := b.CreateInboundOffer("companion:dev", "secret-report.csv", 0, hex.EncodeToString(emptyHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AcceptInboundOffer(offer.ID); err != nil {
		t.Fatal(err)
	}
	_, err = b.DeliverInboundOffer("companion:dev", offer.ID, strings.NewReader(""), 0)
	if err == nil {
		t.Fatal("delivery into an unusable cache directory must fail")
	}
	for _, text := range []string{err.Error(), strings.Join(logger.events, "\n")} {
		if strings.Contains(text, root) || strings.Contains(text, "secret-report.csv") {
			t.Fatalf("host path or filename leaked: %s", text)
		}
	}
}

// The path-only carrier is what the Companion hands to the control plane. It
// validates cheaply, holds no measurements, and is never armed as it is.
func TestFilePathItemCarriesOnlyThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(path, []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := FilePathItem(path, "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != ItemFile || item.Name != "report.csv" || item.File == nil || *item.File != (FileRef{Path: path}) {
		t.Fatalf("carrier = %+v", item)
	}
	b := New(time.Minute)
	if _, err := b.ArmItems([]Item{item}); err == nil {
		t.Fatal("an item without measurements must not be armed")
	}
	for _, bad := range []string{"", "relative.csv", filepath.Dir(path)} {
		if _, err := FilePathItem(bad, ""); err == nil {
			t.Errorf("FilePathItem(%q) was accepted", bad)
		}
	}
}

// A response never carries an error's own words unless they are a fixed sentence
// written for callers. An OS error names the host path it failed on, and this is
// what keeps it from reaching the remote side of the tunnel.
func TestReasonSharesOnlyPublicErrors(t *testing.T) {
	for _, err := range []error{ErrInvalidFile, ErrFileTooLarge, ErrImageTooLarge, ErrInvalidText, errOfferNotFound, errApprovalRequired, errInboundStorage} {
		if got := Reason(err); got != err.Error() {
			t.Errorf("Reason(%v) = %q, want its own text", err, got)
		}
	}
	if got := Reason(fmt.Errorf("prepare: %w", ErrInvalidFile)); got != ErrInvalidFile.Error() {
		t.Errorf("a wrapped public error lost its text: %q", got)
	}

	_, osErr := os.Open(filepath.Join(t.TempDir(), "secret-host-file.txt"))
	for _, err := range []error{osErr, fmt.Errorf("find inbox: %w", osErr), errors.New("read /Users/someone/private.txt"), nil} {
		got := Reason(err)
		if got != "request rejected" || strings.Contains(got, "/") {
			t.Errorf("Reason(%v) = %q, want the generic sentence", err, got)
		}
	}
}
