package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Foxtea267/AriNode/conf"
)

var ErrUpdateJobUnavailable = errors.New("backend update job is unavailable")

var UpdateJobID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type BackendUpdateRequest struct {
	Action  string `json:"action"`
	Agent   string `json:"agent"`
	Version string `json:"version"`
	Job     string `json:"job,omitempty"`
	Result  string `json:"result,omitempty"`
}

func BackendUpdate(ctx context.Context, binding conf.MachineBinding, update BackendUpdateRequest) (string, error) {
	client := &http.Client{
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return backendUpdate(ctx, client, binding, update)
}

func backendUpdate(ctx context.Context, client *http.Client, binding conf.MachineBinding, update BackendUpdateRequest) (string, error) {
	host, err := conf.PanelURL(binding.APIHost)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(host, "https://") {
		return "", fmt.Errorf("backend updates require an HTTPS panel")
	}
	payload := struct {
		BackendUpdateRequest
		MachineID int    `json:"machine_id"`
		NodeID    int    `json:"node_id"`
		Token     string `json:"token"`
	}{update, binding.MachineID, 0, binding.APIKey}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/v1/arinode/backend-update", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("invalid backend update endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("backend update request failed")
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusConflict:
		return "", ErrUpdateJobUnavailable
	case http.StatusOK:
	default:
		return "", fmt.Errorf("backend update HTTP %d", res.StatusCode)
	}
	var reply struct {
		Data *struct {
			Job      string `json:"job"`
			Accepted bool   `json:"accepted"`
		} `json:"data"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(data) > 8192 || json.Unmarshal(data, &reply) != nil || reply.Data == nil {
		return "", fmt.Errorf("invalid backend update response")
	}
	if update.Action == "poll" {
		if reply.Data.Job != "" && !UpdateJobID.MatchString(reply.Data.Job) {
			return "", fmt.Errorf("invalid backend update job ID")
		}
		return reply.Data.Job, nil
	}
	if !reply.Data.Accepted {
		return "", ErrUpdateJobUnavailable
	}
	return "", nil
}
