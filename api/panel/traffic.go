package panel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const trafficBase = "/api/v1/arinode/traffic/"

// TrafficReport is a sealed accounting batch. Keep its ID and traffic
// unchanged across retries; new bytes always go into a separate batch.
type TrafficReport struct {
	ReportID string           `json:"report_id"`
	Traffic  map[int][2]int64 `json:"traffic"`
}

func NewTrafficReport(rows []UserTraffic) (*TrafficReport, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	r := &TrafficReport{ReportID: fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(id[:])), Traffic: make(map[int][2]int64, len(rows))}
	for _, u := range rows {
		v := r.Traffic[u.UID]
		v[0] += u.Upload
		v[1] += u.Download
		r.Traffic[u.UID] = v
	}
	return r, nil
}

// TrafficReceipts distinguishes a panel without the plugin endpoint from a
// failing/auth-rejecting endpoint. Only a missing route allows native fallback.
func (c *Client) TrafficReceipts() (bool, error) {
	path := trafficBase + "info"
	r, err := c.client.R().SetHeader("Accept", "application/json").Get(path)
	if err == nil && r != nil && (r.StatusCode() == http.StatusNotFound || r.StatusCode() == http.StatusMethodNotAllowed) {
		return false, nil
	}
	if err := c.checkResponse(r, path, err); err != nil {
		return false, err
	}
	var info struct {
		Version int `json:"version"`
		NodeID  int `json:"node_id"`
	}
	if r.StatusCode() != http.StatusOK || json.Unmarshal(r.Body(), &info) != nil || info.Version != 1 || info.NodeID != c.NodeId {
		return false, fmt.Errorf("invalid traffic receipt handshake")
	}
	return true, nil
}

func (c *Client) ReportTrafficWithReceipt(report *TrafficReport) error {
	path := trafficBase + "report"
	r, err := c.client.R().SetHeader("Accept", "application/json").SetBody(report).Post(path)
	if err := c.checkResponse(r, path, err); err != nil {
		return err
	}
	var ack struct {
		Accepted bool   `json:"accepted"`
		ReportID string `json:"report_id"`
	}
	if r.StatusCode() != http.StatusOK || json.Unmarshal(r.Body(), &ack) != nil || !ack.Accepted || ack.ReportID != report.ReportID {
		return fmt.Errorf("traffic report was not acknowledged")
	}
	return nil
}
