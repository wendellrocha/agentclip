package integration

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendellrocha/agentclip/internal/bridge"
	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/daemon"
	"github.com/wendellrocha/agentclip/internal/mcpserver"
)

func TestArmedImageFlowsFromBridgeToMCP(t *testing.T) {
	pngData := fixturePNG(t)
	d, err := daemon.Start(&daemon.Image{PNG: pngData, Width: 2, Height: 2}, "control-token")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	_, token, err := d.Bridge.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(d.State.Address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := mcpserver.NewHTTPProvider(port, token)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	server := mcpserver.New(provider)
	client := mcp.NewClient(&mcp.Implementation{Name: "agentclip-integration-test", Version: "0.1.0"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_clipboard_image", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("CallTool() = %#v, %v", result, err)
	}
	if len(result.Content) != 2 {
		t.Fatalf("content length = %d, want 2", len(result.Content))
	}
	imageResult, ok := result.Content[1].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.ImageContent", result.Content[1])
	}
	if imageResult.MIMEType != "image/png" || !bytes.Equal(imageResult.Data, pngData) {
		t.Fatal("MCP result did not preserve the armed PNG")
	}

	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_clipboard_image", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !second.IsError {
		t.Fatal("second image call should report consumed image")
	}
}

func TestCSVMaterializesFromBridgeToPrivateRemotePath(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "vendas.csv")
	contents := []byte("month,total\n2026-08,42\n")
	if err := os.WriteFile(source, contents, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := bridge.FileItem(source, "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(nil, "control-token")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	snapshot, err := d.Bridge.ArmItems([]bridge.Item{file})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := d.Bridge.CreateSession(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(d.State.Address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := mcpserver.NewHTTPProvider(port, token)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.MaterializeFiles(context.Background(), []string{snapshot.Items[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].ItemID != snapshot.Items[0].ID {
		t.Fatalf("materialization = %#v", result)
	}
	data, err := os.ReadFile(result.Files[0].Path)
	if err != nil || !bytes.Equal(data, contents) {
		t.Fatalf("remote file = %q, %v", data, err)
	}
	info, err := os.Stat(result.Files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("remote file permissions = %o", info.Mode().Perm())
	}
	if _, err := provider.MaterializeFiles(context.Background(), []string{snapshot.Items[0].ID}); err == nil {
		t.Fatal("second materialization unexpectedly succeeded")
	}
}

func TestRemoteFileRequiresLocalApprovalAndArrivesAtHost(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	source := filepath.Join(t.TempDir(), "relatorio.csv")
	contents := []byte("month,total\n2026-09,84\n")
	if err := os.WriteFile(source, contents, 0600); err != nil {
		t.Fatal(err)
	}
	d, err := daemon.Start(nil, "control-token")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Bridge.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(d.State.Address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := mcpserver.NewHTTPProvider(port, "read-token", "upload-token")
	if err != nil {
		t.Fatal(err)
	}
	unauthorized, err := http.NewRequest(http.MethodPost, "http://"+d.State.Address+"/v1/inbound/offers", strings.NewReader(`{"name":"relatorio.csv","size":1,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Header.Set("Authorization", "Bearer read-token")
	response, err := http.DefaultClient.Do(unauthorized)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("read token upload status = %d, want 401", response.StatusCode)
	}
	offer, err := provider.OfferFileToHost(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if offer.State != "pending" || offer.Name != "relatorio.csv" {
		t.Fatalf("offer = %#v", offer)
	}
	if _, err := provider.DeliverFileToHost(context.Background(), offer.ID, source); err == nil {
		t.Fatal("delivery succeeded without local approval")
	}
	if _, err := d.Bridge.AcceptInboundOffer(offer.ID); err != nil {
		t.Fatal(err)
	}
	delivered, err := provider.DeliverFileToHost(context.Background(), offer.ID, source)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.State != "delivered" {
		t.Fatalf("delivery = %#v", delivered)
	}
	status := d.Bridge.InboundLocalStatus()
	if len(status.Received) != 1 || status.Received[0].Path == "" {
		t.Fatalf("local inbox = %#v", status)
	}
	data, err := os.ReadFile(status.Received[0].Path)
	if err != nil || !bytes.Equal(data, contents) {
		t.Fatalf("host file = %q, %v", data, err)
	}
	if status.Received[0].SHA256 != delivered.SHA256 {
		t.Fatalf("hash = %q, want %q", status.Received[0].SHA256, delivered.SHA256)
	}
}

func TestCompanionApprovalUnblocksRemoteFileOffer(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	d, err := daemon.Start(nil, "control-token")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Bridge.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(d.State.Address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := mcpserver.NewHTTPProvider(port, "read-token", "upload-token")
	if err != nil {
		t.Fatal(err)
	}
	offer, err := d.Bridge.CreateInboundOffer("companion:dev", "report.csv", 3, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	control, err := companion.StartControl("dev", func() any { return d.Bridge.InboundLocalStatus() }, func() {}, func(action, offerID string) error {
		if action != "accept" {
			return fmt.Errorf("unexpected action %q", action)
		}
		_, err := d.Bridge.AcceptInboundOffer(offerID)
		return err
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()

	type waitResult struct {
		offer mcpserver.HostFileOffer
		err   error
	}
	result := make(chan waitResult, 1)
	go func() {
		approved, waitErr := provider.WaitHostFileOffer(context.Background(), offer.ID)
		result <- waitResult{offer: approved, err: waitErr}
	}()
	time.Sleep(20 * time.Millisecond)
	request, err := http.NewRequest(http.MethodPost, companion.ViewURL(control.State)+"api/inbound/"+offer.ID+"/accept", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("approval status = %d", response.StatusCode)
	}
	select {
	case got := <-result:
		if got.err != nil || got.offer.State != "accepted" {
			t.Fatalf("wait result = %#v, %v", got.offer, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remote offer was not unblocked by Companion approval")
	}
	if status := d.Bridge.InboundLocalStatus(); len(status.Offers) != 0 {
		t.Fatalf("approved offer remains actionable: %#v", status.Offers)
	}
}

func fixturePNG(t *testing.T) []byte {
	t.Helper()
	image := image.NewRGBA(image.Rect(0, 0, 2, 2))
	image.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
