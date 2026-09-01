package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	inboxRetention          = 30 * time.Minute
	fileTransferTTL         = 2 * time.Minute
	hostApprovalWaitTimeout = 10 * time.Minute
	hostApprovalPollEvery   = 250 * time.Millisecond
)

// HTTPProvider obtains bridge state through the loopback port exposed by the
// SSH reverse forward. Its endpoint must be an HTTP URL on 127.0.0.1.
type HTTPProvider struct {
	baseURL     string
	token       string
	uploadToken string
	client      *http.Client
}

// NewHTTPProvider constructs a bridge client. The port is intentionally the
// only endpoint input accepted by the remote command, preventing requests to
// arbitrary hosts.
func NewHTTPProvider(port int, token string, uploadTokens ...string) (*HTTPProvider, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("bridge port must be between 1 and 65535")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("bridge session token is required")
	}
	uploadToken := ""
	if len(uploadTokens) > 0 {
		uploadToken = uploadTokens[0]
	}
	provider := &HTTPProvider{
		baseURL:     "http://127.0.0.1:" + strconv.Itoa(port),
		token:       token,
		uploadToken: uploadToken,
		client:      &http.Client{Timeout: 5 * time.Second},
	}
	_ = cleanupExpiredInbox()
	return provider, nil
}

// OfferFileToHost submits only verified metadata. The content stays on the
// remote machine until the local user accepts the offer in the Companion.
func (p *HTTPProvider) OfferFileToHost(ctx context.Context, path string) (HostFileOffer, error) {
	metadata, err := remoteFileMetadata(path)
	if err != nil {
		return HostFileOffer{}, err
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return HostFileOffer{}, err
	}
	response, err := p.uploadRequest(ctx, http.MethodPost, "/v1/inbound/offers", strings.NewReader(string(payload)), int64(len(payload)), p.client)
	if err != nil {
		return HostFileOffer{}, err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return HostFileOffer{}, err
	}
	var offer HostFileOffer
	if err := json.NewDecoder(response.Body).Decode(&offer); err != nil {
		return HostFileOffer{}, fmt.Errorf("decode inbound offer: %w", err)
	}
	return offer, nil
}

func (p *HTTPProvider) HostFileOfferStatus(ctx context.Context, offerID string) (HostFileOffer, error) {
	if strings.TrimSpace(offerID) == "" {
		return HostFileOffer{}, fmt.Errorf("offer_id is required")
	}
	response, err := p.uploadRequest(ctx, http.MethodGet, "/v1/inbound/offers/"+offerID, nil, 0, p.client)
	if err != nil {
		return HostFileOffer{}, err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return HostFileOffer{}, err
	}
	var offer HostFileOffer
	if err := json.NewDecoder(response.Body).Decode(&offer); err != nil {
		return HostFileOffer{}, fmt.Errorf("decode inbound offer: %w", err)
	}
	return offer, nil
}

// WaitHostFileOffer waits for an explicit local approval, rejection, or
// expiry. It polls only the authenticated loopback bridge exposed by the
// existing reverse tunnel; the host never connects directly to the remote MCP
// process.
func (p *HTTPProvider) WaitHostFileOffer(ctx context.Context, offerID string) (HostFileOffer, error) {
	if strings.TrimSpace(offerID) == "" {
		return HostFileOffer{}, fmt.Errorf("offer_id is required")
	}
	waitCtx, cancel := context.WithTimeout(ctx, hostApprovalWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(hostApprovalPollEvery)
	defer ticker.Stop()
	for {
		offer, err := p.HostFileOfferStatus(waitCtx, offerID)
		if err != nil {
			return HostFileOffer{}, err
		}
		if offer.State != "pending" {
			return offer, nil
		}
		select {
		case <-waitCtx.Done():
			return HostFileOffer{}, fmt.Errorf("waiting for host approval: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (p *HTTPProvider) DeliverFileToHost(ctx context.Context, offerID, path string) (HostFileOffer, error) {
	if strings.TrimSpace(offerID) == "" {
		return HostFileOffer{}, fmt.Errorf("offer_id is required")
	}
	metadata, err := remoteFileMetadata(path)
	if err != nil {
		return HostFileOffer{}, err
	}
	offer, err := p.HostFileOfferStatus(ctx, offerID)
	if err != nil {
		return HostFileOffer{}, err
	}
	if offer.State != "accepted" {
		return HostFileOffer{}, fmt.Errorf("host file offer is %s", offer.State)
	}
	if offer.Name != metadata.Name || offer.Size != metadata.Size || !strings.EqualFold(offer.SHA256, metadata.SHA256) {
		return HostFileOffer{}, fmt.Errorf("remote file changed after it was offered")
	}
	file, err := os.Open(path)
	if err != nil {
		return HostFileOffer{}, fmt.Errorf("open remote file: %w", err)
	}
	defer file.Close()
	transferCtx, cancel := context.WithTimeout(ctx, fileTransferTTL)
	defer cancel()
	response, err := p.uploadRequest(transferCtx, http.MethodPut, "/v1/inbound/offers/"+offerID+"/content", file, metadata.Size, &http.Client{Timeout: fileTransferTTL})
	if err != nil {
		return HostFileOffer{}, err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return HostFileOffer{}, err
	}
	if err := json.NewDecoder(response.Body).Decode(&offer); err != nil {
		return HostFileOffer{}, fmt.Errorf("decode delivered inbound offer: %w", err)
	}
	return offer, nil
}

func (p *HTTPProvider) Status(ctx context.Context) (Status, error) {
	response, err := p.request(ctx, "/v1/status")
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return Status{}, err
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("decode bridge status: %w", err)
	}
	return status, nil
}

func (p *HTTPProvider) Image(ctx context.Context) (Image, error) {
	response, err := p.request(ctx, "/v1/image")
	if err != nil {
		return Image{}, err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return Image{}, err
	}
	if response.Header.Get("Content-Type") != "image/png" {
		return Image{}, fmt.Errorf("bridge returned unexpected content type %q", response.Header.Get("Content-Type"))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 12*1024*1024+1))
	if err != nil {
		return Image{}, fmt.Errorf("read bridge image: %w", err)
	}
	if len(data) == 0 {
		return Image{}, fmt.Errorf("bridge returned an empty image")
	}
	if len(data) > 12*1024*1024 {
		return Image{}, fmt.Errorf("bridge image exceeds maximum size")
	}
	return Image{Data: data, MIMEType: "image/png"}, nil
}

func (p *HTTPProvider) Text(ctx context.Context, itemID string) (Text, error) {
	if strings.TrimSpace(itemID) == "" {
		return Text{}, fmt.Errorf("item_id is required")
	}
	response, err := p.request(ctx, "/v1/items/"+itemID)
	if err != nil {
		return Text{}, err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return Text{}, err
	}
	mime := response.Header.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(mime), "text/") {
		return Text{}, fmt.Errorf("clipboard item is not text")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		return Text{}, fmt.Errorf("read clipboard text: %w", err)
	}
	if len(data) == 0 || len(data) > 1<<20 {
		return Text{}, fmt.Errorf("clipboard text exceeds maximum size")
	}
	return Text{Data: data, MIMEType: mime}, nil
}

func (p *HTTPProvider) MaterializeFiles(ctx context.Context, itemIDs []string) (Materialization, error) {
	if len(itemIDs) == 0 || len(itemIDs) > 5 {
		return Materialization{}, fmt.Errorf("materialize between 1 and 5 file items")
	}
	status, err := p.Status(ctx)
	if err != nil {
		return Materialization{}, err
	}
	items := make(map[string]ItemMetadata, len(status.Items))
	for _, item := range status.Items {
		items[item.ID] = item
	}
	selected := make([]ItemMetadata, 0, len(itemIDs))
	var total int64
	for _, id := range itemIDs {
		item, found := items[id]
		if !found || item.Kind != "file" || item.Consumed {
			return Materialization{}, fmt.Errorf("clipboard item %q is not an available file", id)
		}
		if item.Size < 0 || item.Size > 50<<20 || total+item.Size > 100<<20 {
			return Materialization{}, fmt.Errorf("clipboard files exceed transfer limits")
		}
		total += item.Size
		selected = append(selected, item)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Materialization{}, fmt.Errorf("find AgentClip cache directory: %w", err)
	}
	root := filepath.Join(cache, "agentclip", "inbox")
	if err := os.MkdirAll(root, 0700); err != nil {
		return Materialization{}, fmt.Errorf("create AgentClip inbox: %w", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		return Materialization{}, fmt.Errorf("secure AgentClip inbox: %w", err)
	}
	if err := cleanupInbox(root, time.Now().Add(-inboxRetention)); err != nil {
		return Materialization{}, fmt.Errorf("clean AgentClip inbox: %w", err)
	}
	directory, err := os.MkdirTemp(root, "snapshot-")
	if err != nil {
		return Materialization{}, fmt.Errorf("create materialization directory: %w", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return Materialization{}, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(directory)
		}
	}()
	result := Materialization{Directory: directory, Files: make([]MaterializedFile, 0, len(selected))}
	for _, item := range selected {
		file, err := p.downloadFile(ctx, directory, item)
		if err != nil {
			return Materialization{}, err
		}
		result.Files = append(result.Files, file)
	}
	success = true
	return result, nil
}

func cleanupExpiredInbox() error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	return cleanupInbox(filepath.Join(cache, "agentclip", "inbox"), time.Now().Add(-inboxRetention))
}

func cleanupInbox(root string, cutoff time.Time) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *HTTPProvider) downloadFile(ctx context.Context, directory string, item ItemMetadata) (MaterializedFile, error) {
	transferCtx, cancel := context.WithTimeout(ctx, fileTransferTTL)
	defer cancel()
	response, err := p.requestWithClient(transferCtx, "/v1/items/"+item.ID, &http.Client{Timeout: fileTransferTTL})
	if err != nil {
		return MaterializedFile{}, err
	}
	defer response.Body.Close()
	if err := responseError(response); err != nil {
		return MaterializedFile{}, err
	}
	if response.Header.Get("Content-Length") != strconv.FormatInt(item.Size, 10) || response.Header.Get("X-AgentClip-SHA256") != item.SHA256 {
		return MaterializedFile{}, fmt.Errorf("clipboard file metadata changed during transfer")
	}
	name := filepath.Base(item.Name)
	if name == "." || name == "" || name == string(filepath.Separator) {
		name = item.ID
	}
	path := filepath.Join(directory, name)
	if _, err := os.Lstat(path); err == nil {
		path = filepath.Join(directory, item.ID+"-"+name)
	}
	temporary, err := os.CreateTemp(directory, ".agentclip-file-")
	if err != nil {
		return MaterializedFile{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return MaterializedFile{}, err
	}
	hash := sha256.New()
	bytesWritten, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, 50<<20+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return MaterializedFile{}, fmt.Errorf("download clipboard file: %w", copyErr)
	}
	if closeErr != nil {
		return MaterializedFile{}, closeErr
	}
	if bytesWritten != item.Size || hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return MaterializedFile{}, fmt.Errorf("clipboard file hash verification failed")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return MaterializedFile{}, err
	}
	return MaterializedFile{ItemID: item.ID, Path: path, Size: item.Size, SHA256: item.SHA256}, nil
}

func (p *HTTPProvider) request(ctx context.Context, path string) (*http.Response, error) {
	return p.requestWithClient(ctx, path, p.client)
}

func (p *HTTPProvider) requestWithClient(ctx context.Context, path string, client *http.Client) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build bridge request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request bridge: %w", err)
	}
	return response, nil
}

func (p *HTTPProvider) uploadRequest(ctx context.Context, method, path string, body io.Reader, length int64, client *http.Client) (*http.Response, error) {
	if strings.TrimSpace(p.uploadToken) == "" {
		return nil, fmt.Errorf("host uploads are unavailable for this profile; run agentclip setup again")
	}
	request, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build inbound request: %w", err)
	}
	if body != nil {
		request.ContentLength = length
	}
	request.Header.Set("Authorization", "Bearer "+p.uploadToken)
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request host inbox: %w", err)
	}
	return response, nil
}

type remoteFileOffer struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func remoteFileMetadata(path string) (remoteFileOffer, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return remoteFileOffer{}, fmt.Errorf("remote file must be a regular file")
	}
	if info.Size() < 0 || info.Size() > 50<<20 {
		return remoteFileOffer{}, fmt.Errorf("remote file exceeds transfer limits")
	}
	file, err := os.Open(path)
	if err != nil {
		return remoteFileOffer{}, fmt.Errorf("open remote file: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, 50<<20+1)); err != nil {
		return remoteFileOffer{}, fmt.Errorf("hash remote file: %w", err)
	}
	return remoteFileOffer{Name: filepath.Base(path), Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func responseError(response *http.Response) error {
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	defer io.Copy(io.Discard, io.LimitReader(response.Body, 8*1024))
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err == nil && body.Code != "" {
		if body.Message != "" {
			return fmt.Errorf("bridge %s: %s", body.Code, body.Message)
		}
		return fmt.Errorf("bridge %s", body.Code)
	}
	return fmt.Errorf("bridge returned HTTP %d", response.StatusCode)
}
