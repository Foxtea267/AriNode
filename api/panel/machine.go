package panel

import (
	"encoding/json"
	"fmt"
	"strings"
)

type MachineNode struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
}

func (c *Client) GetMachineNodes() ([]MachineNode, error) {
	if c.MachineID <= 0 {
		return nil, fmt.Errorf("machine ID is required")
	}
	r, err := c.client.R().SetBody(map[string]any{"token": c.Token, "machine_id": c.MachineID}).Post("/api/v2/server/machine/nodes")
	if err != nil {
		return nil, fmt.Errorf("machine discovery request failed")
	}
	if r.StatusCode() != 200 {
		return nil, fmt.Errorf("machine discovery HTTP %d", r.StatusCode())
	}
	var data struct {
		Nodes *[]MachineNode `json:"nodes"`
	}
	if err := json.Unmarshal(r.Body(), &data); err != nil || data.Nodes == nil {
		return nil, fmt.Errorf("invalid machine discovery response")
	}
	seen := make(map[int]bool)
	for i := range *data.Nodes {
		n := &(*data.Nodes)[i]
		n.Type = strings.ToLower(n.Type)
		if n.ID <= 0 || n.Type == "" || seen[n.ID] {
			return nil, fmt.Errorf("invalid or duplicate machine node")
		}
		seen[n.ID] = true
	}
	return *data.Nodes, nil
}
