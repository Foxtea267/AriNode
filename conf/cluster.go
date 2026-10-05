package conf

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Members share a panel/node ID; DNS chooses the receiver, and the plugin merges reports.
type ClusterConfig struct {
	Domain   string `json:"Domain"`
	MemberID string `json:"MemberID"`
}

var clusterMemberPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var clusterDomainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (c *ClusterConfig) Validate() error {
	if c == nil {
		return nil
	}
	if !clusterMemberPattern.MatchString(c.MemberID) {
		return fmt.Errorf("Cluster.MemberID must be 1-64 letters, digits, dots, underscores or hyphens")
	}
	domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.Domain), "."))
	if len(domain) > 253 || !strings.Contains(domain, ".") || net.ParseIP(domain) != nil {
		return fmt.Errorf("Cluster.Domain must be a DNS hostname")
	}
	for _, label := range strings.Split(domain, ".") {
		if !clusterDomainLabel.MatchString(label) {
			return fmt.Errorf("invalid Cluster.Domain label")
		}
	}
	c.Domain = domain
	return nil
}
