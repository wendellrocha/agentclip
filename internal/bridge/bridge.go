// Package bridge provides the authenticated, loopback HTTP bridge used by the
// remote MCP process to retrieve an armed clipboard snapshot.
package bridge

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	DefaultTTL    = 90 * time.Second
	FileTTL       = 10 * time.Minute
	MaxImageBytes = 12 << 20
	MaxTextBytes  = 1 << 20
	MaxFileBytes  = 50 << 20
	MaxFiles      = 5
	MaxDimension  = 4096
)

var (
	ErrImageTooLarge   = errors.New("image exceeds maximum size")
	ErrInvalidImage    = errors.New("image is not a valid PNG")
	ErrImageDimensions = errors.New("image dimensions are invalid")
	ErrTextTooLarge    = errors.New("text exceeds maximum size")
	ErrInvalidText     = errors.New("text is not valid UTF-8")
	ErrFileTooLarge    = errors.New("file exceeds maximum size")
	ErrInvalidFile     = errors.New("file must be a regular file")
)

type ItemKind string

const (
	ItemImage ItemKind = "image"
	ItemText  ItemKind = "text"
	ItemFile  ItemKind = "file"
)

// FileRef is accepted only through the local control plane. Path is never
// exposed by HTTP status or MCP responses.
type FileRef struct {
	Path    string
	Size    int64
	ModTime time.Time
	SHA256  string
}

// Item is the local input used to arm a snapshot. Data applies to images and
// text; File applies only to files.
type Item struct {
	ID       string
	Kind     ItemKind
	MIMEType string
	Name     string
	Data     []byte
	Width    int
	Height   int
	File     *FileRef
}

type ItemMetadata struct {
	ID       string   `json:"id"`
	Kind     ItemKind `json:"kind"`
	MIMEType string   `json:"mime_type"`
	Name     string   `json:"name,omitempty"`
	Size     int64    `json:"size"`
	SHA256   string   `json:"sha256"`
	Consumed bool     `json:"consumed"`
	Width    int      `json:"width,omitempty"`
	Height   int      `json:"height,omitempty"`
}

type Snapshot struct {
	ID                 string         `json:"id"`
	ArmedAt, ExpiresAt time.Time      `json:"-"`
	Items              []ItemMetadata `json:"items"`
}

type armedItem struct {
	meta ItemMetadata
	data []byte
	file *FileRef
}

// ArmedImage is kept for callers of the original image-only API.
type ArmedImage struct {
	PNG                []byte
	SHA256             string
	Width, Height      int
	MIME               string
	ArmedAt, ExpiresAt time.Time
	ID                 string
	Consumed           bool
}

type Session struct {
	ID                   string
	ImageID              string
	TokenHash            [32]byte
	CreatedAt, ExpiresAt time.Time
	Revoked              bool
	Persistent           bool
}

type Bridge struct {
	mu       sync.Mutex
	snapshot *Snapshot
	items    map[string]*armedItem
	sessions map[string]*Session
	ttl      time.Duration
	now      func() time.Time
}

func New(ttl time.Duration) *Bridge {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Bridge{items: make(map[string]*armedItem), sessions: make(map[string]*Session), ttl: ttl, now: time.Now}
}

// Arm preserves the image-only local API while creating a generic snapshot.
func (b *Bridge) Arm(imagePNG []byte, width, height int) (ArmedImage, error) {
	snapshot, err := b.ArmItems([]Item{{Kind: ItemImage, MIMEType: "image/png", Data: imagePNG, Width: width, Height: height}})
	if err != nil {
		return ArmedImage{}, err
	}
	item := snapshot.Items[0]
	return ArmedImage{PNG: append([]byte(nil), imagePNG...), SHA256: item.SHA256, Width: width, Height: height, MIME: item.MIMEType, ArmedAt: snapshot.ArmedAt, ExpiresAt: snapshot.ExpiresAt, ID: item.ID}, nil
}

// ArmItems replaces the current clipboard snapshot. File references remain
// local until their individual item endpoint is requested.
func (b *Bridge) ArmItems(items []Item) (Snapshot, error) {
	if len(items) == 0 {
		return Snapshot{}, errors.New("clipboard snapshot has no items")
	}
	fileCount := 0
	prepared := make(map[string]*armedItem, len(items))
	metadata := make([]ItemMetadata, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			item.ID = randomID()
		}
		if _, exists := prepared[item.ID]; exists {
			return Snapshot{}, errors.New("clipboard snapshot has duplicate item IDs")
		}
		armed, err := prepareItem(item)
		if err != nil {
			return Snapshot{}, err
		}
		if armed.meta.Kind == ItemFile {
			fileCount++
			if fileCount > MaxFiles {
				return Snapshot{}, fmt.Errorf("clipboard snapshot exceeds %d files", MaxFiles)
			}
		}
		prepared[item.ID] = armed
		metadata = append(metadata, armed.meta)
	}
	now := b.now()
	ttl := b.ttl
	if fileCount > 0 && ttl == DefaultTTL {
		ttl = FileTTL
	}
	snapshot := &Snapshot{ID: randomID(), ArmedAt: now, ExpiresAt: now.Add(ttl), Items: metadata}
	b.mu.Lock()
	b.snapshot = snapshot
	b.items = prepared
	for _, session := range b.sessions {
		if !session.Persistent {
			session.Revoked = true
		}
	}
	b.mu.Unlock()
	return copySnapshot(snapshot), nil
}

func prepareItem(item Item) (*armedItem, error) {
	meta := ItemMetadata{ID: item.ID, Kind: item.Kind, MIMEType: item.MIMEType, Name: filepath.Base(item.Name), Width: item.Width, Height: item.Height}
	switch item.Kind {
	case ItemImage:
		if len(item.Data) == 0 || len(item.Data) > MaxImageBytes {
			return nil, ErrImageTooLarge
		}
		config, err := png.DecodeConfig(bytes.NewReader(item.Data))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidImage, err)
		}
		if config.Width < 1 || config.Height < 1 || config.Width > MaxDimension || config.Height > MaxDimension {
			return nil, fmt.Errorf("%w: %dx%d", ErrImageDimensions, config.Width, config.Height)
		}
		if item.Width != config.Width || item.Height != config.Height {
			return nil, fmt.Errorf("%w: supplied %dx%d, PNG is %dx%d", ErrImageDimensions, item.Width, item.Height, config.Width, config.Height)
		}
		data := append([]byte(nil), item.Data...)
		sum := sha256.Sum256(data)
		meta.MIMEType = "image/png"
		meta.Name = "clipboard.png"
		meta.Size = int64(len(data))
		meta.SHA256 = hex.EncodeToString(sum[:])
		meta.Width, meta.Height = config.Width, config.Height
		return &armedItem{meta: meta, data: data}, nil
	case ItemText:
		if len(item.Data) == 0 || len(item.Data) > MaxTextBytes {
			return nil, ErrTextTooLarge
		}
		if !utf8.Valid(item.Data) {
			return nil, ErrInvalidText
		}
		data := append([]byte(nil), item.Data...)
		sum := sha256.Sum256(data)
		if meta.MIMEType == "" {
			meta.MIMEType = "text/plain; charset=utf-8"
		}
		if meta.Name == "." || meta.Name == "" {
			meta.Name = "clipboard.txt"
		}
		meta.Size = int64(len(data))
		meta.SHA256 = hex.EncodeToString(sum[:])
		return &armedItem{meta: meta, data: data}, nil
	case ItemFile:
		if item.File == nil {
			return nil, ErrInvalidFile
		}
		ref, err := validateFileRef(*item.File)
		if err != nil {
			return nil, err
		}
		if meta.Name == "." || meta.Name == "" {
			meta.Name = filepath.Base(ref.Path)
		}
		if meta.MIMEType == "" {
			meta.MIMEType = "application/octet-stream"
		}
		meta.Size, meta.SHA256 = ref.Size, ref.SHA256
		return &armedItem{meta: meta, file: &ref}, nil
	default:
		return nil, fmt.Errorf("unsupported clipboard item kind %q", item.Kind)
	}
}

// FileItem creates a verified local file item without exposing the path to a
// remote caller. MIME is metadata only and may be empty.
func FileItem(path, mime string) (Item, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Item{}, ErrInvalidFile
	}
	if info.Size() > MaxFileBytes {
		return Item{}, ErrFileTooLarge
	}
	sum, err := hashFile(path)
	if err != nil {
		return Item{}, ErrInvalidFile
	}
	return Item{Kind: ItemFile, MIMEType: mime, Name: filepath.Base(path), File: &FileRef{Path: path, Size: info.Size(), ModTime: info.ModTime(), SHA256: sum}}, nil
}

func validateFileRef(ref FileRef) (FileRef, error) {
	if ref.Path == "" || ref.Size < 0 || ref.Size > MaxFileBytes || ref.SHA256 == "" {
		return FileRef{}, ErrInvalidFile
	}
	info, err := os.Lstat(ref.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != ref.Size || !info.ModTime().Equal(ref.ModTime) {
		return FileRef{}, ErrInvalidFile
	}
	return ref, nil
}

func (b *Bridge) RegisterPersistentSession(id, token string) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(token) == "" {
		return errors.New("persistent session ID and token are required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.pruneSessionsLocked(now)
	hash := sha256.Sum256([]byte(token))
	b.sessions[id] = &Session{ID: id, TokenHash: hash, CreatedAt: now, Persistent: true}
	return nil
}

func (b *Bridge) CreateSession(ttl time.Duration) (Session, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.pruneSessionsLocked(now)
	if b.snapshot == nil || !now.Before(b.snapshot.ExpiresAt) {
		return Session{}, "", errors.New("no clipboard item armed")
	}
	if ttl <= 0 {
		ttl = b.ttl
	}
	token := randomID() + randomID()
	id := randomID()
	hash := sha256.Sum256([]byte(token))
	session := &Session{ID: id, ImageID: b.snapshot.ID, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	b.sessions[id] = session
	return *session, token, nil
}

func (b *Bridge) RevokeSession(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if session := b.sessions[id]; session != nil {
		session.Revoked = true
	}
}

func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", b.health)
	mux.HandleFunc("/v1/status", b.status)
	mux.HandleFunc("/v1/image", b.imageHandler)
	mux.HandleFunc("/v1/items/", b.itemHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (b *Bridge) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (b *Bridge) authenticate(r *http.Request) (*Session, string) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, "UNAUTHORIZED"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneSessionsLocked(b.now())
	for _, session := range b.sessions {
		hash := sha256.Sum256([]byte(parts[1]))
		if subtle.ConstantTimeCompare(hash[:], session.TokenHash[:]) == 1 {
			if session.Revoked || (!session.Persistent && b.now().After(session.ExpiresAt)) {
				return nil, "INVALID_SESSION"
			}
			return session, ""
		}
	}
	return nil, "UNAUTHORIZED"
}

func (b *Bridge) pruneSessionsLocked(now time.Time) {
	for id, session := range b.sessions {
		if session.Revoked || (!session.Persistent && !now.Before(session.ExpiresAt)) {
			delete(b.sessions, id)
		}
	}
}

func (b *Bridge) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		b.err(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	session, code := b.authenticate(r)
	if code != "" {
		b.err(w, http.StatusUnauthorized, code, "unauthorized")
		return
	}
	b.mu.Lock()
	if !b.snapshotAllowedLocked(session) {
		b.mu.Unlock()
		b.err(w, http.StatusNotFound, "NO_CLIPBOARD_ARMED", "no clipboard item armed")
		return
	}
	if !b.now().Before(b.snapshot.ExpiresAt) {
		b.mu.Unlock()
		b.err(w, http.StatusGone, "SNAPSHOT_EXPIRED", "clipboard snapshot expired")
		return
	}
	out := b.statusLocked()
	b.mu.Unlock()
	b.json(w, out)
}

func (b *Bridge) statusLocked() StatusResponse {
	out := StatusResponse{Armed: true, SnapshotID: b.snapshot.ID, ExpiresAt: b.snapshot.ExpiresAt, Items: make([]ItemMetadata, 0, len(b.items))}
	for _, original := range b.snapshot.Items {
		item := b.items[original.ID]
		meta := item.meta
		out.Items = append(out.Items, meta)
		if !meta.Consumed {
			out.Remaining++
		}
		if meta.Kind == ItemImage && out.Width == 0 {
			out.Consumed, out.Width, out.Height, out.Bytes = meta.Consumed, meta.Width, meta.Height, int(meta.Size)
		}
	}
	return out
}

func (b *Bridge) imageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		b.err(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var imageID string
	b.mu.Lock()
	for id, item := range b.items {
		if item.meta.Kind == ItemImage {
			imageID = id
			break
		}
	}
	b.mu.Unlock()
	if imageID == "" {
		b.err(w, http.StatusNotFound, "NO_IMAGE_ARMED", "no image armed")
		return
	}
	b.serveItem(w, r, imageID)
}

func (b *Bridge) itemHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		b.err(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/items/")
	if id == "" || strings.Contains(id, "/") {
		b.err(w, http.StatusNotFound, "ITEM_NOT_FOUND", "clipboard item not found")
		return
	}
	b.serveItem(w, r, id)
}

func (b *Bridge) serveItem(w http.ResponseWriter, r *http.Request, id string) {
	session, code := b.authenticate(r)
	if code != "" {
		b.err(w, http.StatusUnauthorized, code, "unauthorized")
		return
	}
	b.mu.Lock()
	if !b.snapshotAllowedLocked(session) {
		b.mu.Unlock()
		b.err(w, http.StatusNotFound, "NO_CLIPBOARD_ARMED", "no clipboard item armed")
		return
	}
	if !b.now().Before(b.snapshot.ExpiresAt) {
		b.mu.Unlock()
		b.err(w, http.StatusGone, "SNAPSHOT_EXPIRED", "clipboard snapshot expired")
		return
	}
	item := b.items[id]
	if item == nil {
		b.mu.Unlock()
		b.err(w, http.StatusNotFound, "ITEM_NOT_FOUND", "clipboard item not found")
		return
	}
	if item.meta.Consumed {
		b.mu.Unlock()
		b.err(w, http.StatusGone, "ITEM_CONSUMED", "clipboard item already consumed")
		return
	}
	meta := item.meta
	data := append([]byte(nil), item.data...)
	var ref *FileRef
	if item.file != nil {
		copyRef := *item.file
		ref = &copyRef
	}
	b.mu.Unlock()

	var file *os.File
	if ref != nil {
		var err error
		file, err = openVerifiedFile(*ref)
		if err != nil {
			b.err(w, http.StatusConflict, "FILE_CHANGED", "clipboard file changed or is unavailable")
			return
		}
		defer file.Close()
	}
	b.mu.Lock()
	current := b.items[id]
	if current == nil || current.meta.Consumed || !b.snapshotAllowedLocked(session) {
		b.mu.Unlock()
		b.err(w, http.StatusGone, "ITEM_CONSUMED", "clipboard item already consumed")
		return
	}
	current.meta.Consumed = true
	meta.Consumed = true
	b.mu.Unlock()
	w.Header().Set("Content-Type", meta.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("X-AgentClip-SHA256", meta.SHA256)
	w.WriteHeader(http.StatusOK)
	if file != nil {
		_, _ = io.Copy(w, file)
		return
	}
	_, _ = w.Write(data)
}

func (b *Bridge) snapshotAllowedLocked(session *Session) bool {
	return b.snapshot != nil && (session.Persistent || session.ImageID == b.snapshot.ID)
}

func openVerifiedFile(ref FileRef) (*os.File, error) {
	if _, err := validateFileRef(ref); err != nil {
		return nil, err
	}
	file, err := os.Open(ref.Path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != ref.Size || !info.ModTime().Equal(ref.ModTime) {
		file.Close()
		return nil, ErrInvalidFile
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		file.Close()
		return nil, ErrInvalidFile
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, MaxFileBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copySnapshot(snapshot *Snapshot) Snapshot {
	copy := *snapshot
	copy.Items = append([]ItemMetadata(nil), snapshot.Items...)
	return copy
}

type StatusResponse struct {
	SnapshotID string         `json:"snapshot_id,omitempty"`
	Armed      bool           `json:"armed"`
	Consumed   bool           `json:"consumed"`
	ExpiresAt  time.Time      `json:"expires_at,omitempty"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
	Bytes      int            `json:"bytes,omitempty"`
	Remaining  int            `json:"remaining"`
	Items      []ItemMetadata `json:"items,omitempty"`
}

type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (b *Bridge) err(w http.ResponseWriter, status int, code, msg string) {
	b.jsonStatus(w, status, ErrorResponse{Code: code, Message: msg})
}

func (b *Bridge) json(w http.ResponseWriter, value any) { b.jsonStatus(w, http.StatusOK, value) }

func (b *Bridge) jsonStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func randomID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value[:])
}
