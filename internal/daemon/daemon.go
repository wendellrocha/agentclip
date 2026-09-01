// Package daemon runs the persistent loopback bridge and its local control API.
package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wendellrocha/agentclip/internal/bridge"
)

type Image struct {
	PNG    []byte `json:"-"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type armRequest struct {
	PNG    string `json:"png"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
type snapshotRequest struct {
	Items []snapshotItemRequest `json:"items"`
}
type snapshotItemRequest struct {
	ID       string          `json:"id"`
	Kind     bridge.ItemKind `json:"kind"`
	MIMEType string          `json:"mime_type"`
	Name     string          `json:"name"`
	Data     string          `json:"data,omitempty"`
	Width    int             `json:"width,omitempty"`
	Height   int             `json:"height,omitempty"`
	File     *bridge.FileRef `json:"file,omitempty"`
}
type persistentSessionRequest struct {
	ID          string `json:"id"`
	Token       string `json:"token"`
	UploadToken string `json:"upload_token,omitempty"`
}

type State struct {
	Address      string    `json:"address"`
	ControlToken string    `json:"control_token"`
	PID          int       `json:"pid"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Daemon struct {
	Bridge   *bridge.Bridge
	State    State
	server   *http.Server
	listener net.Listener
}

// Start launches a loopback daemon. initial may be nil. The control token is
// required for both control endpoints and is never accepted from a remote bind.
func Start(initial *Image, controlToken string) (*Daemon, error) {
	if controlToken == "" {
		return nil, errors.New("control token is required")
	}
	b := bridge.New(bridge.DefaultTTL)
	if initial != nil {
		if _, err := b.Arm(initial.PNG, initial.Width, initial.Height); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	d := &Daemon{Bridge: b, listener: l}
	d.State = State{Address: l.Addr().String(), ControlToken: controlToken, PID: os.Getpid(), UpdatedAt: time.Now().UTC()}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/control/arm", d.arm)
	mux.HandleFunc("/v1/control/snapshot", d.snapshot)
	mux.HandleFunc("/v1/control/sessions", d.session)
	mux.HandleFunc("/v1/control/persistent-session", d.persistentSession)
	mux.HandleFunc("/v1/control/inbound", d.inbound)
	mux.HandleFunc("/v1/control/inbound/", d.inboundAction)
	mux.HandleFunc("/v1/control/shutdown", d.shutdown)
	mux.Handle("/", b.Handler())
	d.server = &http.Server{
		Handler:           noStore(authControl(controlToken, mux)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       bridge.InboundTransferTimeout,
		WriteTimeout:      bridge.InboundTransferTimeout,
	}
	if err := saveState(d.State); err != nil {
		l.Close()
		return nil, err
	}
	go func() { _ = d.server.Serve(l) }()
	return d, nil
}

func (d *Daemon) Close() error {
	if d.server == nil {
		return nil
	}
	err := d.server.Shutdown(context.Background())
	if current, loadErr := LoadState(); loadErr == nil && current.PID == d.State.PID && current.ControlToken == d.State.ControlToken {
		if path, pathErr := statePath(); pathErr == nil {
			_ = os.Remove(path)
		}
	}
	return err
}
func LoadState() (State, error) {
	p, err := statePath()
	if err != nil {
		return State{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	var s State
	return s, json.NewDecoder(f).Decode(&s)
}

func (d *Daemon) arm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req armRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, bridge.MaxImageBytes*2)).Decode(&req) != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	png, err := base64.StdEncoding.DecodeString(req.PNG)
	if err != nil {
		http.Error(w, "png must be base64", 400)
		return
	}
	im, err := d.Bridge.Arm(png, req.Width, req.Height)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, map[string]any{"id": im.ID, "expires_at": im.ExpiresAt})
}
func (d *Daemon) snapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request snapshotRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, bridge.MaxImageBytes*2)).Decode(&request); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	items := make([]bridge.Item, 0, len(request.Items))
	for _, input := range request.Items {
		var data []byte
		var err error
		if input.Data != "" {
			data, err = base64.StdEncoding.DecodeString(input.Data)
			if err != nil {
				http.Error(w, "item data must be base64", http.StatusBadRequest)
				return
			}
		}
		items = append(items, bridge.Item{ID: input.ID, Kind: input.Kind, MIMEType: input.MIMEType, Name: input.Name, Data: data, Width: input.Width, Height: input.Height, File: input.File})
	}
	snapshot, err := d.Bridge.ArmItems(items)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, snapshot)
}
func (d *Daemon) session(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	s, t, err := d.Bridge.CreateSession(0)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	writeJSON(w, map[string]any{"id": s.ID, "token": t, "expires_at": s.ExpiresAt})
}
func (d *Daemon) persistentSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request persistentSessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&request); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := d.Bridge.RegisterPersistentSessionWithUpload(request.ID, request.Token, request.UploadToken); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"registered": true})
}

func (d *Daemon) inbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, d.Bridge.InboundLocalStatus())
}

func (d *Daemon) inboundAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/control/inbound/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && parts[1] == "file" {
		file, offer, err := d.Bridge.OpenInboundFile(parts[0])
		if err != nil {
			http.Error(w, "received file is unavailable", http.StatusNotFound)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": offer.Name}))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", offer.Size))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if bridge.InboundTextPreviewable(offer.Name) {
			w.Header().Set("X-AgentClip-Previewable", "true")
		}
		_, _ = io.Copy(w, io.LimitReader(file, offer.Size))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var (
		offer any
		err   error
	)
	switch parts[1] {
	case "accept":
		offer, err = d.Bridge.AcceptInboundOffer(parts[0])
	case "reject":
		offer, err = d.Bridge.RejectInboundOffer(parts[0])
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, offer)
}
func (d *Daemon) shutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]bool{"stopping": true})
	go func() { _ = d.Close() }()
}
func authControl(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/control/") {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", 401)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func statePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agentclip", "bridge.json"), nil
}
func saveState(s State) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "bridge.json.tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		enc := json.NewEncoder(tmp)
		enc.SetIndent("", "  ")
		err = enc.Encode(s)
	}
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(name, 0600); err != nil {
		return err
	}
	return os.Rename(name, p)
}
