package panel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Foxtea267/AriNode/common/sysstatus"
)

const clusterBase = "/api/v1/arinode/cluster/"

type ClusterReport struct {
	ReportID string            `json:"report_id"`
	MemberID string            `json:"member_id"`
	Domain   string            `json:"domain"`
	Traffic  map[int][2]int64  `json:"traffic"`
	Alive    map[int][]string  `json:"alive"`
	Status   *sysstatus.Status `json:"status,omitempty"`
}

func (c *Client) CheckCluster() error {
	if c.Cluster == nil {
		return nil
	}
	path := clusterBase + "info"
	r, err := c.client.R().SetQueryParam("domain", c.Cluster.Domain).Get(path)
	if err = c.checkResponse(r, path, err); err != nil {
		return fmt.Errorf("cluster requires enabled AriNode Xboard plugin: %w", err)
	}
	var info struct {
		Version int `json:"version"`
		NodeID  int `json:"node_id"`
	}
	if r.StatusCode() != 200 || json.Unmarshal(r.Body(), &info) != nil || info.Version != 1 || info.NodeID != c.NodeId {
		return fmt.Errorf("invalid cluster plugin handshake")
	}
	return nil
}
func (c *Client) NewClusterReport(traffic []UserTraffic, alive map[int][]string, status *sysstatus.Status) (*ClusterReport, error) {
	if c.Cluster == nil {
		return nil, fmt.Errorf("cluster is not configured")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	r := &ClusterReport{ReportID: hex.EncodeToString(id[:]), MemberID: c.Cluster.MemberID, Domain: c.Cluster.Domain, Traffic: make(map[int][2]int64), Alive: alive, Status: status}
	if r.Alive == nil {
		r.Alive = make(map[int][]string)
	}
	for _, u := range traffic {
		v := r.Traffic[u.UID]
		v[0] += u.Upload
		v[1] += u.Download
		r.Traffic[u.UID] = v
	}
	return r, nil
}
func (c *Client) ReportCluster(r *ClusterReport) error {
	path := clusterBase + "report"
	response, err := c.client.R().SetBody(r).Post(path)
	if err = c.checkResponse(response, path, err); err != nil {
		return err
	}
	var ack struct {
		Accepted bool   `json:"accepted"`
		ReportID string `json:"report_id"`
	}
	if response.StatusCode() != 200 || json.Unmarshal(response.Body(), &ack) != nil || !ack.Accepted || ack.ReportID != r.ReportID {
		return fmt.Errorf("cluster report was not acknowledged")
	}
	return nil
}
