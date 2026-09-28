// Package companion owns a paired, persistent connection from the host to one
// server profile.
package companion

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Profile contains the local half of a server pairing. Token is a capability
// and must never be printed or put into logs.
type Profile struct {
	Name            string    `json:"name"`
	Destination     string    `json:"destination"`
	RemotePort      int       `json:"remote_port"`
	Token           string    `json:"token"`
	UploadToken     string    `json:"upload_token,omitempty"`
	SSHIdentityFile string    `json:"ssh_identity_file,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

func (p Profile) HasUploadToken() bool { return strings.TrimSpace(p.UploadToken) != "" }

func (p Profile) Validate() error {
	if !validProfileName(p.Name) {
		return errors.New("companion profile name must contain only letters, numbers, hyphens, or underscores")
	}
	if strings.TrimSpace(p.Destination) == "" {
		return errors.New("companion SSH destination is required")
	}
	if p.RemotePort < 1 || p.RemotePort > 65535 {
		return errors.New("companion remote port must be between 1 and 65535")
	}
	if strings.TrimSpace(p.Token) == "" {
		return errors.New("companion pairing token is required")
	}
	if p.SSHIdentityFile != "" && !filepath.IsAbs(p.SSHIdentityFile) {
		return errors.New("companion SSH identity file must be an absolute path")
	}
	return nil
}

func SaveProfile(profile Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	path, err := profilePath(profile.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create companion profile directory: %w", err)
	}
	payload, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode companion profile: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), profile.Name+".tmp-")
	if err != nil {
		return fmt.Errorf("create companion profile file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	err = temporary.Chmod(0600)
	if err == nil {
		_, err = temporary.Write(payload)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write companion profile: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0600); err != nil {
		return fmt.Errorf("secure companion profile: %w", err)
	}
	return os.Rename(temporaryPath, path)
}

func LoadProfile(name string) (Profile, error) {
	path, err := profilePath(name)
	if err != nil {
		return Profile{}, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fmt.Errorf("read companion profile %q: %w", name, err)
	}
	var profile Profile
	if err := json.Unmarshal(payload, &profile); err != nil {
		return Profile{}, fmt.Errorf("decode companion profile %q: %w", name, err)
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, fmt.Errorf("invalid companion profile %q: %w", name, err)
	}
	return profile, nil
}

// ListProfiles returns every valid persisted pairing. Invalid files are
// reported instead of silently skipped: an upgrade must not claim that every
// configured host was considered when one profile cannot be read.
func ListProfiles() ([]Profile, error) {
	configDir, err := agentClipConfigDir()
	if err != nil {
		return nil, fmt.Errorf("find AgentClip config directory: %w", err)
	}
	directory := filepath.Join(configDir, "agentclip", "profiles")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []Profile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read companion profiles: %w", err)
	}
	profiles := make([]Profile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		profile, err := LoadProfile(name)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, nil
}

func profilePath(name string) (string, error) {
	if !validProfileName(name) || filepath.Base(name) != name {
		return "", errors.New("companion profile name must be a simple filename")
	}
	configDir, err := agentClipConfigDir()
	if err != nil {
		return "", fmt.Errorf("find AgentClip config directory: %w", err)
	}
	return filepath.Join(configDir, "agentclip", "profiles", name+".json"), nil
}

// SSHIdentityPath returns the private key path reserved for a setup-created
// SSH identity. The caller is responsible for creating the key material.
func SSHIdentityPath(name string) (string, error) {
	if !validProfileName(name) || filepath.Base(name) != name {
		return "", errors.New("companion profile name must be a simple filename")
	}
	configDir, err := agentClipConfigDir()
	if err != nil {
		return "", fmt.Errorf("find AgentClip config directory: %w", err)
	}
	return filepath.Join(configDir, "agentclip", "keys", name), nil
}

func agentClipConfigDir() (string, error) {
	if directory := strings.TrimSpace(os.Getenv("AGENTCLIP_CONFIG_DIR")); directory != "" {
		if !filepath.IsAbs(directory) {
			return "", errors.New("AGENTCLIP_CONFIG_DIR must be an absolute path")
		}
		return directory, nil
	}
	return os.UserConfigDir()
}

func validProfileName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}
