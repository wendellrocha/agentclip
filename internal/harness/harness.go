// Package harness registers the AgentClip MCP server with agent harnesses
// that are configured through local files (Antigravity, OpenCode and Pi).
package harness

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type harnessMCPEntry struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type openCodeMCPEntry struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	Environment map[string]string `json:"environment"`
	Enabled     bool              `json:"enabled"`
}

// Install registers AgentClip with a supported harness under the given home.
func Install(home, harness, name string, port int, token string, uploadTokens ...string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("harness entry name is required")
	}
	uploadToken := ""
	if len(uploadTokens) > 0 {
		uploadToken = uploadTokens[0]
	}
	environment := map[string]string{
		"AGENTCLIP_BRIDGE_PORT":   strconv.Itoa(port),
		"AGENTCLIP_SESSION_TOKEN": token,
	}
	if strings.TrimSpace(uploadToken) != "" {
		environment["AGENTCLIP_UPLOAD_TOKEN"] = uploadToken
	}
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "agy", "antigravity":
		path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
		return updateJSONMCPEntry(path, "mcpServers", name, harnessMCPEntry{Command: "agentclip", Args: []string{"mcp"}, Env: environment})
	case "opencode":
		path := filepath.Join(home, ".config", "opencode", "opencode.json")
		return updateJSONMCPEntry(path, "mcp", name, openCodeMCPEntry{Type: "local", Command: []string{"agentclip", "mcp"}, Environment: environment, Enabled: true})
	case "pi":
		return writePiExtension(home, name, port, token, uploadToken)
	default:
		return fmt.Errorf("unsupported harness %q; supported harnesses: agy, opencode, pi", harness)
	}
}

// Remove deletes the AgentClip registration from a supported harness.
func Remove(home, harness, name string) error {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "agy", "antigravity":
		return removeJSONMCPEntry(filepath.Join(home, ".gemini", "config", "mcp_config.json"), "mcpServers", name)
	case "opencode":
		return removeJSONMCPEntry(filepath.Join(home, ".config", "opencode", "opencode.json"), "mcp", name)
	case "pi":
		path := filepath.Join(home, ".pi", "agent", "extensions", name+".ts")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove Pi extension: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported harness %q; supported harnesses: agy, opencode, pi", harness)
	}
}

func updateJSONMCPEntry(path, section, name string, entry any) error {
	root, err := readJSONObject(path)
	if err != nil {
		return err
	}
	entries := map[string]json.RawMessage{}
	if raw, found := root[section]; found {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("read %s from %s: %w", section, path, err)
		}
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode AgentClip MCP entry: %w", err)
	}
	entries[name] = raw
	root[section], err = json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode %s: %w", section, err)
	}
	return writeJSONObject(path, root)
}

func removeJSONMCPEntry(path, section, name string) error {
	root, err := readJSONObject(path)
	if err != nil {
		return err
	}
	raw, found := root[section]
	if !found {
		return nil
	}
	entries := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("read %s from %s: %w", section, path, err)
	}
	if _, found := entries[name]; !found {
		return nil
	}
	delete(entries, name)
	if len(entries) == 0 {
		delete(root, section)
	} else {
		root[section], err = json.Marshal(entries)
		if err != nil {
			return fmt.Errorf("encode %s: %w", section, err)
		}
	}
	return writeJSONObject(path, root)
}

func readJSONObject(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	root := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return root, nil
}

func writeJSONObject(path string, root map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".agentclip-")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace configuration: %w", err)
	}
	return os.Chmod(path, 0600)
}

func writePiExtension(home, name string, port int, token, uploadToken string) error {
	directory := filepath.Join(home, ".pi", "agent", "extensions")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create Pi extension directory: %w", err)
	}
	path := filepath.Join(directory, name+".ts")
	if err := os.WriteFile(path, []byte(piExtensionSource(port, token, uploadToken)), 0600); err != nil {
		return fmt.Errorf("write Pi extension: %w", err)
	}
	return os.Chmod(path, 0600)
}

//go:embed pi_extension.ts.tmpl
var piExtensionTemplate string

func piExtensionSource(port int, token, uploadToken string) string {
	return strings.NewReplacer(
		"__PORT__", strconv.Itoa(port),
		"__TOKEN__", strconv.Quote(token),
		"__UPLOAD_TOKEN__", strconv.Quote(uploadToken),
	).Replace(piExtensionTemplate)
}
