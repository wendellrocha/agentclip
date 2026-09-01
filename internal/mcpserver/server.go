// Package mcpserver exposes the AgentClip image provider as MCP tools.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendellrocha/agentclip/internal/buildinfo"
)

// ClipboardProvider is the bridge-facing interface used by the MCP server.
// Implementations must return a copy of image/text data or keep it immutable.
type ClipboardProvider interface {
	Status(context.Context) (Status, error)
	Image(context.Context) (Image, error)
	Text(context.Context, string) (Text, error)
	MaterializeFiles(context.Context, []string) (Materialization, error)
}

type Status struct {
	SnapshotID string         `json:"snapshot_id,omitempty"`
	Armed      bool           `json:"armed"`
	Consumed   bool           `json:"consumed"`
	Width      int            `json:"width,omitempty"`
	Height     int            `json:"height,omitempty"`
	Bytes      int            `json:"bytes,omitempty"`
	Remaining  int            `json:"remaining"`
	ExpiresAt  time.Time      `json:"expires_at,omitempty"`
	Items      []ItemMetadata `json:"items,omitempty"`
}

type ItemMetadata struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	MIMEType string `json:"mime_type"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Consumed bool   `json:"consumed"`
}

type Image struct {
	Data     []byte
	MIMEType string
}

type Text struct {
	Data     []byte
	MIMEType string
}

type MaterializedFile struct {
	ItemID string `json:"item_id"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Materialization struct {
	Directory string             `json:"directory"`
	Files     []MaterializedFile `json:"files"`
}

type itemArguments struct {
	ItemID string `json:"item_id"`
}

type fileArguments struct {
	ItemIDs []string `json:"item_ids"`
}

var ErrNoProvider = errors.New("image provider is nil")

// New constructs an MCP server with clipboard_status and get_clipboard_image.
func New(provider ClipboardProvider) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "agentclip", Version: buildinfo.Version}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name: "clipboard_status", Description: "Lists clipboard items available after the user's explicit request. It returns metadata only, never clipboard bytes or host paths.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		if provider == nil {
			return toolError(ErrNoProvider)
		}
		status, err := provider.Status(ctx)
		if err != nil {
			return toolError(err)
		}
		data, _ := json.Marshal(status)
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_clipboard_image", Description: "Returns the currently armed clipboard image after the user explicitly asks to inspect it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		if provider == nil {
			return toolError(ErrNoProvider)
		}
		image, err := provider.Image(ctx)
		if err != nil {
			return toolError(err)
		}
		if len(image.Data) == 0 {
			return toolError(errors.New("provider returned an empty image"))
		}
		if image.MIMEType == "" {
			image.MIMEType = "image/png"
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: "Clipboard image captured successfully."},
			&mcp.ImageContent{Data: image.Data, MIMEType: image.MIMEType},
		}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_clipboard_text", Description: "Returns an armed clipboard text item after the user explicitly asks to inspect clipboard text.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input itemArguments) (*mcp.CallToolResult, any, error) {
		if provider == nil {
			return toolError(ErrNoProvider)
		}
		text, err := provider.Text(ctx, input.ItemID)
		if err != nil {
			return toolError(err)
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text.Data)}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "materialize_clipboard_files", Description: "Transfers explicitly requested clipboard files to private temporary paths on this server. Call only after the user asks to inspect those files; returned paths expire automatically.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input fileArguments) (*mcp.CallToolResult, any, error) {
		if provider == nil {
			return toolError(ErrNoProvider)
		}
		if len(input.ItemIDs) == 0 {
			return toolError(errors.New("item_ids is required"))
		}
		result, err := provider.MaterializeFiles(ctx, input.ItemIDs)
		if err != nil {
			return toolError(err)
		}
		data, _ := json.Marshal(result)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
	})
	return s
}

func toolError(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{
		&mcp.TextContent{Text: err.Error()},
	}}, nil, nil
}
