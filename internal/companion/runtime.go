package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// RuntimeState locates the local Companion control server. ControlToken and
// ViewToken are local capabilities and are never returned by the dashboard.
type RuntimeState struct {
	Profile      string    `json:"profile"`
	Address      string    `json:"address"`
	ControlToken string    `json:"control_token"`
	ViewToken    string    `json:"view_token"`
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"started_at"`
}

func LoadRuntime(name string) (RuntimeState, error) {
	path, err := runtimePath(name)
	if err != nil {
		return RuntimeState{}, err
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return RuntimeState{}, fmt.Errorf("read Companion runtime %q: %w", name, err)
	}
	var state RuntimeState
	if err := json.Unmarshal(payload, &state); err != nil {
		return RuntimeState{}, fmt.Errorf("decode Companion runtime %q: %w", name, err)
	}
	if state.Profile != name || !validLoopbackAddress(state.Address) || state.ControlToken == "" || state.ViewToken == "" {
		return RuntimeState{}, fmt.Errorf("invalid Companion runtime %q", name)
	}
	return state, nil
}

func SaveRuntime(state RuntimeState) error {
	if !validProfileName(state.Profile) || !validLoopbackAddress(state.Address) || state.ControlToken == "" || state.ViewToken == "" {
		return errors.New("invalid Companion runtime state")
	}
	path, err := runtimePath(state.Profile)
	if err != nil {
		return err
	}
	return writePrivateJSON(path, state.Profile+".tmp-", state)
}

func RemoveRuntime(state RuntimeState) {
	current, err := LoadRuntime(state.Profile)
	if err == nil && current.PID == state.PID && current.ControlToken == state.ControlToken {
		path, pathErr := runtimePath(state.Profile)
		if pathErr == nil {
			_ = os.Remove(path)
		}
	}
}

func RuntimeHealthy(state RuntimeState) bool {
	request, err := http.NewRequest(http.MethodGet, "http://"+state.Address+"/v1/healthz", nil)
	if err != nil {
		return false
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func FetchRuntimeStatus(state RuntimeState) (json.RawMessage, error) {
	request, err := http.NewRequest(http.MethodGet, "http://"+state.Address+"/v1/status", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Companion status returned HTTP %d", response.StatusCode)
	}
	return io.ReadAll(io.LimitReader(response.Body, 64*1024))
}

func StopRuntime(state RuntimeState) error {
	request, err := http.NewRequest(http.MethodPost, "http://"+state.Address+"/v1/control/stop", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("stop Companion returned HTTP %d", response.StatusCode)
	}
	return nil
}

// InboundAction asks the local Companion dashboard service to approve or
// reject a remote file offer. The control token never leaves the host.
func InboundAction(state RuntimeState, offerID, action string) error {
	if offerID == "" || (action != "accept" && action != "reject") {
		return errors.New("invalid inbound action")
	}
	response, err := postJSON("http://"+state.Address+"/v1/control/inbound/"+offerID+"/"+action, state.ControlToken, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("inbound action returned HTTP %d", response.StatusCode)
	}
	return nil
}

func ViewURL(state RuntimeState) string {
	return "http://" + state.Address + "/view/" + state.ViewToken + "/"
}

func runtimePath(name string) (string, error) {
	if !validProfileName(name) {
		return "", errors.New("companion profile name must be a simple filename")
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find AgentClip cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "agentclip", "companions", name+".json"), nil
}

func writePrivateJSON(path, temporaryPrefix string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), temporaryPrefix)
	if err != nil {
		return err
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
		return err
	}
	if err := os.Chmod(temporaryPath, 0600); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func validLoopbackAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	value, err := net.LookupPort("tcp", port)
	return err == nil && value > 0 && value <= 65535
}

func postJSON(url, token string, body []byte) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return (&http.Client{Timeout: 3 * time.Second}).Do(request)
}
