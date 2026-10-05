package conf

import "testing"

func TestClusterIdentityValidation(t *testing.T) {
	for _, c := range []ClusterConfig{{Domain: "192.0.2.1", MemberID: "hk"}, {Domain: "pool.example.com", MemberID: ""}, {Domain: "https://pool.example.com", MemberID: "hk"}, {Domain: "pool.example.com", MemberID: "../../hk"}} {
		if c.Validate() == nil {
			t.Fatalf("invalid group accepted: %+v", c)
		}
	}
	c := ClusterConfig{Domain: "Pool.Example.COM.", MemberID: "hk-01"}
	if c.Validate() != nil || c.Domain != "pool.example.com" {
		t.Fatal("DNS hostname normalization failed")
	}
}
