package release

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestCompareSemanticVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"v1.2.0", "1.1.9", 1}, {"1.2.0", "v1.2.0", 0}, {"1.2.0-rc.1", "1.2.0", -1}, {"1.2.0-rc.10", "1.2.0-rc.2", 1}, {"1.2.0-alpha", "1.2.0-1", 1},
	}
	for _, test := range tests {
		got, err := Compare(test.left, test.right)
		if err != nil || got != test.want {
			t.Fatalf("Compare(%q, %q) = %d, %v; want %d", test.left, test.right, got, err, test.want)
		}
	}
}

func TestCheckerCachesLatestReleaseFor24Hours(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/repos/example/agentclip/releases/latest" {
			http.NotFound(w, request)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","prerelease":false,"draft":false}`))
	}))
	defer server.Close()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme = "http"
		request.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(request)
	})}
	checker := Checker{Repository: "example/agentclip", CachePath: t.TempDir() + "/release.json", Client: client, Now: func() time.Time { return now }}
	first, err := checker.Check(context.Background(), "1.0.0")
	if err != nil || !first.UpdateAvailable || first.LatestVersion != "v1.2.0" {
		t.Fatalf("first check = %#v, %v", first, err)
	}
	checker.Client = &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("network should not be used") })}
	now = now.Add(23 * time.Hour)
	second, err := checker.Check(context.Background(), "1.0.0")
	if err != nil || second.LatestVersion != "v1.2.0" || !second.CheckedAt.Equal(first.CheckedAt) {
		t.Fatalf("cached check = %#v, %v", second, err)
	}
}

func TestCheckerUsesStaleCacheWhenNetworkFailsAndDevelopmentBuildDoesNotCheck(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	checker := Checker{CachePath: t.TempDir() + "/release.json", Now: func() time.Time { return now }, Client: &http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })}}
	if err := checker.writeCache(cacheEntry{LatestVersion: "v1.1.0", CheckedAt: now.Add(-CacheTTL - time.Minute)}); err != nil {
		t.Fatal(err)
	}
	status, err := checker.Check(context.Background(), "1.0.0")
	if err == nil || status.LatestVersion != "v1.1.0" || !status.UpdateAvailable || status.Error != "" {
		t.Fatalf("stale cache = %#v, %v", status, err)
	}
	dev, err := checker.Check(context.Background(), "1.0.0-dev")
	if err != nil || dev.LatestVersion != "" || dev.UpdateAvailable {
		t.Fatalf("development build = %#v, %v", dev, err)
	}
}

func TestFetchLatestRejectsPrereleaseAndInvalidTag(t *testing.T) {
	for _, body := range []string{`{"tag_name":"v1.2.0-rc.1","prerelease":true}`, `{"tag_name":"not-a-version"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		client := &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
			request.URL.Scheme = "http"
			request.URL.Host = server.Listener.Addr().String()
			return http.DefaultTransport.RoundTrip(request)
		})}
		if _, err := (Checker{Repository: "example/agentclip", Client: client}).FetchLatest(context.Background()); err == nil {
			t.Fatalf("body %s accepted", body)
		}
		server.Close()
	}
}
