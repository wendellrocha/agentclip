package mcpserver

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeProvider struct{}

func (fakeProvider) Status(context.Context) (Status, error) {
	return Status{Armed: true, Bytes: 3, Remaining: 1}, nil
}
func (fakeProvider) Image(context.Context) (Image, error) {
	return Image{Data: []byte{1, 2, 3}, MIMEType: "image/png"}, nil
}
func (fakeProvider) Text(context.Context, string) (Text, error) {
	return Text{Data: []byte("hello"), MIMEType: "text/plain"}, nil
}
func (fakeProvider) MaterializeFiles(context.Context, []string) (Materialization, error) {
	return Materialization{Directory: "/tmp/agentclip", Files: []MaterializedFile{{ItemID: "file", Path: "/tmp/agentclip/file.csv"}}}, nil
}

func TestToolErrorsAreMCPVisible(t *testing.T) {
	result, _, err := toolError(errors.New("no image armed"))
	if err != nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("unexpected error result: %#v, %v", result, err)
	}
}

func TestToolsThroughInMemoryMCP(t *testing.T) {
	ctx := context.Background()
	server := New(fakeProvider{})
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
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
		t.Fatalf("call failed: %v %#v", err, result)
	}
	if len(result.Content) != 2 {
		t.Fatalf("got %d content blocks", len(result.Content))
	}
	text, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_clipboard_text", Arguments: map[string]any{"item_id": "text"}})
	if err != nil || text.IsError {
		t.Fatalf("text failed: %v %#v", err, text)
	}
	files, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "materialize_clipboard_files", Arguments: map[string]any{"item_ids": []string{"file"}}})
	if err != nil || files.IsError {
		t.Fatalf("files failed: %v %#v", err, files)
	}
	if _, ok := result.Content[1].(*mcp.ImageContent); !ok {
		t.Fatalf("got %T, want ImageContent", result.Content[1])
	}

	status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "clipboard_status", Arguments: map[string]any{}})
	if err != nil || status.IsError {
		t.Fatalf("status failed: %v %#v", err, status)
	}
}
