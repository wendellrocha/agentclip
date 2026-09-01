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
	Name        string    `json:"name"`
	Destination string    `json:"destination"`
	RemotePort  int       `json:"remote_port"`
	Token       string    `json:"token"`
	CreatedAt   time.Time `json:"created_at"`
}

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
	if err := temporary.Chmod(0600); err == nil {
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

func profilePath(name string) (string, error) {
	if !validProfileName(name) || filepath.Base(name) != name {
		return "", errors.New("companion profile name must be a simple filename")
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find AgentClip config directory: %w", err)
	}
	return filepath.Join(configDir, "agentclip", "profiles", name+".json"), nil
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
