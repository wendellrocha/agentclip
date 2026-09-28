package companion

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed ui/index.html ui/assets/*
var dashboardFiles embed.FS

// ControlServer exposes a private loopback dashboard and lifecycle API for a
// running Companion. The view URL contains an unguessable local capability.
type ControlServer struct {
	State    RuntimeState
	server   *http.Server
	listener net.Listener
}

// InboundFileContent is a verified local file that the Companion may serve
// through its private view URL. Its reader is always closed by Control.
type InboundFileContent struct {
	Name        string
	Size        int64
	Previewable bool
	Reader      io.ReadCloser
}

func StartControl(profile string, snapshot func() any, stop func(), inboundAction func(string, string) error, inboundContent func(string) (InboundFileContent, error)) (*ControlServer, error) {
	if !validProfileName(profile) {
		return nil, fmt.Errorf("invalid companion profile %q", profile)
	}
	if snapshot == nil || stop == nil {
		return nil, fmt.Errorf("Companion control callbacks are required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	controlToken, err := randomToken(32)
	if err != nil {
		listener.Close()
		return nil, err
	}
	viewToken, err := randomToken(24)
	if err != nil {
		listener.Close()
		return nil, err
	}
	control := &ControlServer{
		State: RuntimeState{
			Profile: profile, Address: listener.Addr().String(), ControlToken: controlToken,
			ViewToken: viewToken, PID: os.Getpid(), StartedAt: time.Now().UTC(),
		},
		listener: listener,
	}
	prefix := "/view/" + viewToken
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/healthz", control.requireControl(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux.HandleFunc("/v1/status", control.requireControl(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, snapshot())
	}))
	mux.HandleFunc("/v1/control/stop", control.requireControl(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, map[string]bool{"stopping": true})
		go stop()
	}))
	viewHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix || r.URL.Path == prefix+"/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'")
			page, err := dashboardFiles.ReadFile("ui/index.html")
			if err != nil {
				http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(strings.ReplaceAll(string(page), "{{BASE}}", prefix)))
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/assets/") && r.Method == http.MethodGet {
			name := strings.TrimPrefix(r.URL.Path, prefix+"/")
			asset, err := dashboardFiles.ReadFile("ui/" + name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			contentType := "application/octet-stream"
			switch {
			case strings.HasSuffix(name, ".css"):
				contentType = "text/css; charset=utf-8"
			case strings.HasSuffix(name, ".svg"):
				contentType = "image/svg+xml"
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(asset)
			return
		}
		if r.URL.Path == prefix+"/api/status" && r.Method == http.MethodGet {
			writeJSON(w, snapshot())
			return
		}
		if r.URL.Path == prefix+"/api/stop" && r.Method == http.MethodPost {
			writeJSON(w, map[string]bool{"stopping": true})
			go stop()
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/api/inbound/") && r.Method == http.MethodGet {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/api/inbound/"), "/")
			if inboundContent == nil || len(parts) != 2 || parts[0] == "" || (parts[1] != "content" && parts[1] != "download") {
				http.NotFound(w, r)
				return
			}
			content, err := inboundContent(parts[0])
			if content.Reader != nil {
				defer content.Reader.Close()
			}
			if err != nil || content.Reader == nil || content.Size < 0 || (parts[1] == "content" && !content.Previewable) {
				http.Error(w, "received file is unavailable", http.StatusNotFound)
				return
			}
			dispositionType, contentType := "attachment", "application/octet-stream"
			if parts[1] == "content" {
				dispositionType, contentType = "inline", "text/plain; charset=utf-8"
			}
			disposition := mime.FormatMediaType(dispositionType, map[string]string{"filename": content.Name})
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Content-Disposition", disposition)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", content.Size))
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
			_, _ = io.Copy(w, io.LimitReader(content.Reader, content.Size))
			return
		}
		if strings.HasPrefix(r.URL.Path, prefix+"/api/inbound/") && r.Method == http.MethodPost {
			if inboundAction == nil {
				http.NotFound(w, r)
				return
			}
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix+"/api/inbound/"), "/")
			if len(parts) != 2 || parts[0] == "" || (parts[1] != "accept" && parts[1] != "reject") {
				http.NotFound(w, r)
				return
			}
			if err := inboundAction(parts[1], parts[0]); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writeJSON(w, map[string]bool{"updated": true})
			return
		}
		http.NotFound(w, r)
	}
	mux.HandleFunc(prefix, viewHandler)
	mux.HandleFunc(prefix+"/", viewHandler)
	control.server = &http.Server{
		Handler:           noStore(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	if err := SaveRuntime(control.State); err != nil {
		listener.Close()
		return nil, err
	}
	go func() { _ = control.server.Serve(listener) }()
	return control, nil
}

func (c *ControlServer) Close() error {
	if c.server == nil {
		return nil
	}
	err := c.server.Shutdown(context.Background())
	RemoveRuntime(c.State)
	return err
}

func (c *ControlServer) requireControl(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || subtle.ConstantTimeCompare([]byte(parts[1]), []byte(c.State.ControlToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
