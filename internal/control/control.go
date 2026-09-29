// Package control is the client of the bridge's local control plane: the
// authenticated loopback API the CLI and the Companion use to arm clipboard
// snapshots, manage sessions and answer inbound file offers.
package control

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wendellrocha/agentclip/internal/bridge"
	"github.com/wendellrocha/agentclip/internal/companion"
	"github.com/wendellrocha/agentclip/internal/daemon"
	"github.com/wendellrocha/agentclip/internal/release"
)

type ArmResponse struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SessionResponse struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

func Arm(state daemon.State, image daemon.Image) (ArmResponse, error) {
	payload, err := json.Marshal(struct {
		PNG    string `json:"png"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}{base64.StdEncoding.EncodeToString(image.PNG), image.Width, image.Height})
	if err != nil {
		return ArmResponse{}, err
	}
	var result ArmResponse
	return result, Post(state, "/v1/control/arm", payload, &result)
}

func ArmSnapshot(state daemon.State, items []bridge.Item) error {
	type snapshotItem struct {
		ID       string          `json:"id"`
		Kind     bridge.ItemKind `json:"kind"`
		MIMEType string          `json:"mime_type"`
		Name     string          `json:"name"`
		Data     string          `json:"data,omitempty"`
		Width    int             `json:"width,omitempty"`
		Height   int             `json:"height,omitempty"`
		File     *bridge.FileRef `json:"file,omitempty"`
	}
	payload := struct {
		Items []snapshotItem `json:"items"`
	}{Items: make([]snapshotItem, 0, len(items))}
	for _, item := range items {
		entry := snapshotItem{ID: item.ID, Kind: item.Kind, MIMEType: item.MIMEType, Name: item.Name, Width: item.Width, Height: item.Height, File: item.File}
		if len(item.Data) > 0 {
			entry.Data = base64.StdEncoding.EncodeToString(item.Data)
		}
		payload.Items = append(payload.Items, entry)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return Post(state, "/v1/control/snapshot", data, nil)
}

func Session(state daemon.State) (SessionResponse, error) {
	var result SessionResponse
	return result, Post(state, "/v1/control/sessions", nil, &result)
}

func PublishReleaseStatus(state daemon.State, status release.Status) error {
	payload, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return Post(state, "/v1/control/release", payload, nil)
}

func InboundStatus(state daemon.State) bridge.InboundLocalStatus {
	var result bridge.InboundLocalStatus
	if err := Get(state, "/v1/control/inbound", &result); err != nil {
		return bridge.InboundLocalStatus{}
	}
	return result
}

func InboundAction(state daemon.State, offerID, action string) error {
	if action != "accept" && action != "reject" {
		return errors.New("invalid inbound action")
	}
	return Post(state, "/v1/control/inbound/"+offerID+"/"+action, nil, nil)
}

func InboundText(state daemon.State, offerID string) (companion.InboundFileContent, error) {
	if !ValidLoopbackAddress(state.Address) || strings.TrimSpace(offerID) == "" {
		return companion.InboundFileContent{}, errors.New("received file is unavailable")
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+state.Address+"/v1/control/inbound/"+offerID+"/file", nil)
	if err != nil {
		return companion.InboundFileContent{}, err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	response, err := (&http.Client{Timeout: bridge.InboundTransferTimeout}).Do(request)
	if err != nil {
		return companion.InboundFileContent{}, err
	}
	if response.StatusCode != http.StatusOK || response.ContentLength < 0 {
		response.Body.Close()
		return companion.InboundFileContent{}, errors.New("received file is unavailable")
	}
	_, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	if err != nil || strings.TrimSpace(parameters["filename"]) == "" {
		response.Body.Close()
		return companion.InboundFileContent{}, errors.New("received file is unavailable")
	}
	return companion.InboundFileContent{Name: parameters["filename"], Size: response.ContentLength, Previewable: response.Header.Get("X-AgentClip-Previewable") == "true", Reader: response.Body}, nil
}

func Post(state daemon.State, path string, payload []byte, output any) error {
	if !ValidLoopbackAddress(state.Address) {
		return errors.New("bridge state has an invalid loopback address")
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+state.Address+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return fmt.Errorf("bridge control returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if output == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func Get(state daemon.State, path string, output any) error {
	if !ValidLoopbackAddress(state.Address) {
		return errors.New("bridge state has an invalid loopback address")
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+state.Address+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.ControlToken)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return fmt.Errorf("bridge control returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func Healthy(state daemon.State) bool {
	if !ValidLoopbackAddress(state.Address) {
		return false
	}
	response, err := (&http.Client{Timeout: time.Second}).Get("http://" + state.Address + "/healthz")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func Port(state daemon.State) int {
	_, port, err := net.SplitHostPort(state.Address)
	if err != nil {
		return 0
	}
	value, _ := strconv.Atoi(port)
	return value
}

func ValidLoopbackAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return false
	}
	value, err := strconv.Atoi(port)
	return err == nil && value > 0 && value <= 65535
}

// Shutdown asks the bridge to stop.
func Shutdown(state daemon.State) error {
	return Post(state, "/v1/control/shutdown", nil, nil)
}
