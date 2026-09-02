package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	InboundOfferTTL        = 10 * time.Minute
	InboundRetention       = 30 * time.Minute
	InboundTransferTimeout = 2 * time.Minute
)

type InboundState string

const (
	InboundPending   InboundState = "pending"
	InboundAccepted  InboundState = "accepted"
	InboundReceiving InboundState = "receiving"
	InboundDelivered InboundState = "delivered"
	InboundRejected  InboundState = "rejected"
	InboundExpired   InboundState = "expired"
	InboundFailed    InboundState = "failed"
)

// InboundOffer is safe to return to the remote MCP client: it deliberately
// excludes the destination path on the host.
type InboundOffer struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Size      int64        `json:"size"`
	SHA256    string       `json:"sha256"`
	State     InboundState `json:"state"`
	CreatedAt time.Time    `json:"created_at"`
	ExpiresAt time.Time    `json:"expires_at"`
}

// InboundLocalStatus is returned only through the local Companion control
// plane, so it may include the path of already validated received files.
type InboundLocalStatus struct {
	Offers   []InboundOffer    `json:"offers"`
	Received []InboundReceived `json:"received"`
}

type InboundReceived struct {
	InboundOffer
	Path        string    `json:"path"`
	DeliveredAt time.Time `json:"delivered_at"`
	Previewable bool      `json:"previewable"`
}

type inboundOffer struct {
	InboundOffer
	profile     string
	path        string
	deliveredAt time.Time
}

type inboundOfferRequest struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (b *Bridge) inboundOffersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		b.err(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	session, code := b.authenticateUpload(r)
	if code != "" {
		b.err(w, http.StatusUnauthorized, code, "unauthorized")
		return
	}
	var request inboundOfferRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&request); err != nil {
		b.err(w, http.StatusBadRequest, "INVALID_OFFER", "invalid file offer")
		return
	}
	offer, err := b.CreateInboundOffer(session.ID, request.Name, request.Size, request.SHA256)
	if err != nil {
		b.err(w, http.StatusBadRequest, "INVALID_OFFER", err.Error())
		return
	}
	b.json(w, offer)
}

func (b *Bridge) inboundOfferHandler(w http.ResponseWriter, r *http.Request) {
	session, code := b.authenticateUpload(r)
	if code != "" {
		b.err(w, http.StatusUnauthorized, code, "unauthorized")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/inbound/offers/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "content") {
		b.err(w, http.StatusNotFound, "OFFER_NOT_FOUND", "offer not found")
		return
	}
	offerID := parts[0]
	switch {
	case r.Method == http.MethodGet && len(parts) == 1:
		offer, err := b.InboundOffer(session.ID, offerID)
		if err != nil {
			b.err(w, http.StatusNotFound, "OFFER_NOT_FOUND", "offer not found")
			return
		}
		b.json(w, offer)
	case r.Method == http.MethodPut && len(parts) == 2:
		if r.ContentLength < 0 {
			b.err(w, http.StatusLengthRequired, "CONTENT_LENGTH_REQUIRED", "content length is required")
			return
		}
		offer, err := b.DeliverInboundOffer(session.ID, offerID, r.Body, r.ContentLength)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errOfferNotFound) {
				status = http.StatusNotFound
			} else if errors.Is(err, errApprovalRequired) {
				status = http.StatusConflict
			}
			b.err(w, status, "INBOUND_DELIVERY_REJECTED", err.Error())
			return
		}
		b.json(w, offer)
	default:
		b.err(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

var (
	errOfferNotFound    = errors.New("inbound offer not found")
	errApprovalRequired = errors.New("local approval is required before upload")
)

func (b *Bridge) CreateInboundOffer(sessionID, name string, size int64, checksum string) (InboundOffer, error) {
	if !validInboundName(name) || size < 0 || size > MaxFileBytes || !validChecksum(checksum) {
		return InboundOffer{}, errors.New("inbound offer metadata is invalid")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneInboundLocked(b.now())
	if session := b.sessions[sessionID]; session == nil || !session.HasUploadToken || !session.Persistent || session.Revoked {
		return InboundOffer{}, errors.New("inbound upload session is unavailable")
	}
	now := b.now()
	offer := &inboundOffer{InboundOffer: InboundOffer{ID: randomID(), Name: filepath.Base(name), Size: size, SHA256: strings.ToLower(checksum), State: InboundPending, CreatedAt: now, ExpiresAt: now.Add(InboundOfferTTL)}, profile: inboundProfile(sessionID)}
	b.inbound[offer.ID] = offer
	return offer.InboundOffer, nil
}

func (b *Bridge) InboundOffer(sessionID, offerID string) (InboundOffer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneInboundLocked(b.now())
	offer := b.inbound[offerID]
	if offer == nil || offer.profile != inboundProfile(sessionID) {
		return InboundOffer{}, errOfferNotFound
	}
	return offer.InboundOffer, nil
}

func (b *Bridge) AcceptInboundOffer(offerID string) (InboundOffer, error) {
	return b.setInboundOfferState(offerID, InboundAccepted)
}

func (b *Bridge) RejectInboundOffer(offerID string) (InboundOffer, error) {
	return b.setInboundOfferState(offerID, InboundRejected)
}

func (b *Bridge) setInboundOfferState(offerID string, target InboundState) (InboundOffer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneInboundLocked(b.now())
	offer := b.inbound[offerID]
	if offer == nil {
		return InboundOffer{}, errOfferNotFound
	}
	if offer.State != InboundPending {
		return InboundOffer{}, fmt.Errorf("inbound offer is %s", offer.State)
	}
	offer.State = target
	return offer.InboundOffer, nil
}

func (b *Bridge) InboundLocalStatus() InboundLocalStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneInboundLocked(b.now())
	status := InboundLocalStatus{Offers: make([]InboundOffer, 0), Received: make([]InboundReceived, 0)}
	for _, offer := range b.inbound {
		switch offer.State {
		case InboundDelivered:
			status.Received = append(status.Received, InboundReceived{InboundOffer: offer.InboundOffer, Path: offer.path, DeliveredAt: offer.deliveredAt, Previewable: inboundTextName(offer.Name)})
		case InboundPending:
			status.Offers = append(status.Offers, offer.InboundOffer)
		}
	}
	// Maps deliberately back the inbound store, but their iteration order is
	// undefined. Keep the Companion inbox predictable by surfacing the newest
	// offers and deliveries first.
	sort.Slice(status.Offers, func(i, j int) bool {
		if status.Offers[i].CreatedAt.Equal(status.Offers[j].CreatedAt) {
			return status.Offers[i].ID > status.Offers[j].ID
		}
		return status.Offers[i].CreatedAt.After(status.Offers[j].CreatedAt)
	})
	sort.Slice(status.Received, func(i, j int) bool {
		if status.Received[i].DeliveredAt.Equal(status.Received[j].DeliveredAt) {
			return status.Received[i].ID > status.Received[j].ID
		}
		return status.Received[i].DeliveredAt.After(status.Received[j].DeliveredAt)
	})
	return status
}

// OpenInboundFile returns an already delivered file without exposing its host
// path to callers. It is intended for the local Companion control plane.
func (b *Bridge) OpenInboundFile(offerID string) (*os.File, InboundOffer, error) {
	return b.openInboundFile(offerID, false)
}

// OpenInboundTextFile returns an already delivered textual file for safe
// inline viewing. Binary formats must use OpenInboundFile for download.
func (b *Bridge) OpenInboundTextFile(offerID string) (*os.File, InboundOffer, error) {
	return b.openInboundFile(offerID, true)
}

func (b *Bridge) openInboundFile(offerID string, textOnly bool) (*os.File, InboundOffer, error) {
	b.mu.Lock()
	b.pruneInboundLocked(b.now())
	offer := b.inbound[offerID]
	if offer == nil || offer.State != InboundDelivered || (textOnly && !inboundTextName(offer.Name)) {
		b.mu.Unlock()
		return nil, InboundOffer{}, errors.New("received text file is unavailable")
	}
	path, metadata := offer.path, offer.InboundOffer
	b.mu.Unlock()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != metadata.Size {
		return nil, InboundOffer{}, errors.New("received text file is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, InboundOffer{}, errors.New("received text file is unavailable")
	}
	return file, metadata, nil
}

func inboundTextName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	if base == "dockerfile" || base == "makefile" || base == ".env" {
		return true
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".bash", ".c", ".cc", ".cfg", ".conf", ".cpp", ".cs", ".css", ".csv", ".env", ".go", ".gql", ".graphql", ".h", ".hpp", ".html", ".ini", ".java", ".js", ".json", ".jsonl", ".jsx", ".log", ".lua", ".md", ".mjs", ".ndjson", ".php", ".properties", ".py", ".r", ".rb", ".rs", ".sh", ".sql", ".svg", ".toml", ".ts", ".tsx", ".tsv", ".txt", ".xml", ".yaml", ".yml", ".zsh":
		return true
	default:
		return false
	}
}

// InboundTextPreviewable reports whether a received filename can be safely
// displayed as plain text in the local Companion.
func InboundTextPreviewable(name string) bool { return inboundTextName(name) }

func (b *Bridge) DeliverInboundOffer(sessionID, offerID string, body io.Reader, contentLength int64) (InboundOffer, error) {
	b.mu.Lock()
	b.pruneInboundLocked(b.now())
	offer := b.inbound[offerID]
	if offer == nil || offer.profile != inboundProfile(sessionID) {
		b.mu.Unlock()
		return InboundOffer{}, errOfferNotFound
	}
	if offer.State != InboundAccepted {
		state := offer.State
		b.mu.Unlock()
		if state == InboundPending {
			return InboundOffer{}, errApprovalRequired
		}
		return InboundOffer{}, fmt.Errorf("inbound offer is %s", state)
	}
	if contentLength != offer.Size {
		b.mu.Unlock()
		return InboundOffer{}, errors.New("inbound upload length does not match offer")
	}
	offer.State = InboundReceiving
	profile, name, size, checksum := offer.profile, offer.Name, offer.Size, offer.SHA256
	b.mu.Unlock()

	path, err := writeInboundFile(profile, offerID, name, size, checksum, body)
	b.mu.Lock()
	defer b.mu.Unlock()
	current := b.inbound[offerID]
	if current == nil {
		return InboundOffer{}, errOfferNotFound
	}
	if err != nil {
		current.State = InboundFailed
		return InboundOffer{}, err
	}
	current.State, current.path, current.deliveredAt = InboundDelivered, path, b.now()
	return current.InboundOffer, nil
}

func (b *Bridge) pruneInboundLocked(now time.Time) {
	for id, offer := range b.inbound {
		if (offer.State == InboundPending || offer.State == InboundAccepted) && !now.Before(offer.ExpiresAt) {
			offer.State = InboundExpired
		}
		if offer.State == InboundDelivered && !now.Before(offer.deliveredAt.Add(InboundRetention)) {
			_ = os.RemoveAll(filepath.Dir(offer.path))
			delete(b.inbound, id)
		}
	}
}

func writeInboundFile(profile, offerID, name string, size int64, checksum string, body io.Reader) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find local AgentClip inbox: %w", err)
	}
	directory := filepath.Join(cache, "agentclip", "received", profile, offerID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, ".agentclip-inbound-")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(body, MaxFileBytes+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written != size || hex.EncodeToString(hash.Sum(nil)) != checksum {
		return "", errors.New("inbound upload hash verification failed")
	}
	path := filepath.Join(directory, name)
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func validInboundName(name string) bool {
	return name != "" && filepath.Base(name) == name && name != "." && name != string(filepath.Separator)
}

func validChecksum(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func inboundProfile(sessionID string) string {
	name := strings.TrimPrefix(sessionID, "companion:")
	if name == "" || filepath.Base(name) != name {
		return "default"
	}
	return name
}
