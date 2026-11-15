package panel

import (
	"fmt"
	"io"
	"strings"

	"encoding/json/v2"

	"github.com/vmihailenco/msgpack/v5"
)

type OnlineUser struct {
	UID int
	IP  string
}

type UserInfo struct {
	Id          int    `json:"id" msgpack:"id"`
	Uuid        string `json:"uuid" msgpack:"uuid"`
	SpeedLimit  int    `json:"speed_limit" msgpack:"speed_limit"`
	DeviceLimit int    `json:"device_limit" msgpack:"device_limit"`
}

type UserListBody struct {
	Users []UserInfo `json:"users" msgpack:"users"`
}

type AliveMap struct {
	Alive map[int]int      `json:"alive"`
	IPs   map[int][]string `json:"ips,omitempty"`
}

// Retry the full snapshot if applying a fetched user list to the core fails.
// Otherwise the next 304 could leave revoked credentials installed indefinitely.
func (c *Client) InvalidateUserCache() { c.userEtag = "" }

// GetUserList will pull user from v2board
func (c *Client) GetUserList() ([]UserInfo, error) {
	path := c.endpoint("user")
	r, err := c.client.R().
		SetHeader("If-None-Match", c.userEtag).
		SetHeader("X-Response-Format", "msgpack").
		SetDoNotParseResponse(true).
		Get(path)
	if err != nil {
		return nil, panelRequestError(path, err)
	}
	if r == nil || r.RawResponse == nil {
		return nil, fmt.Errorf("panel request %s returned no response body", safeEndpoint(path))
	}
	defer r.RawResponse.Body.Close()

	if r.StatusCode() == 304 {
		return nil, nil
	}

	if err = c.checkResponse(r, path, err); err != nil {
		return nil, err
	}
	userlist := &UserListBody{}
	if strings.Contains(r.Header().Get("Content-Type"), "application/x-msgpack") {
		decoder := msgpack.NewDecoder(r.RawResponse.Body)
		if err := decoder.Decode(userlist); err != nil {
			return nil, fmt.Errorf("decode user list error: invalid msgpack response")
		}
		if _, err := decoder.PeekCode(); err != io.EOF {
			return nil, fmt.Errorf("decode user list error: trailing msgpack data")
		}
	} else {
		if err := json.UnmarshalRead(r.RawResponse.Body, userlist); err != nil {
			return nil, fmt.Errorf("decode user list error: invalid JSON response")
		}
	}
	// Only an explicit empty list revokes everyone. An error envelope or a
	// truncated body must never be applied as an authoritative snapshot.
	if userlist.Users == nil {
		return nil, fmt.Errorf("decode user list error: missing users array")
	}
	seen := make(map[string]struct{}, len(userlist.Users))
	for _, user := range userlist.Users {
		if user.Id <= 0 || strings.TrimSpace(user.Uuid) == "" || user.SpeedLimit < 0 || user.DeviceLimit < 0 {
			return nil, fmt.Errorf("decode user list error: invalid user record")
		}
		if _, duplicate := seen[user.Uuid]; duplicate {
			return nil, fmt.Errorf("decode user list error: duplicate user credential")
		}
		seen[user.Uuid] = struct{}{}
	}
	c.userEtag = r.Header().Get("ETag")
	return userlist.Users, nil
}

// GetUserAlive will fetch the alive_ip count for users
func (c *Client) GetUserAlive() (map[int]int, error) {
	// Publish only a complete successful snapshot. A transport/TLS error must
	// not clear the last valid device-limit state while other APIs are down.
	alive := &AliveMap{}
	path := c.endpoint("alivelist")
	if c.Cluster != nil {
		path = clusterBase + "alivelist"
	}
	r, err := c.client.R().
		ForceContentType("application/json").
		Get(path)
	if err != nil {
		if c.Cluster != nil {
			return nil, fmt.Errorf("cluster alive list is unavailable")
		}
		return nil, panelRequestError(path, err)
	}
	if r == nil || r.StatusCode() >= 399 {
		if c.Cluster != nil {
			return nil, fmt.Errorf("cluster alive list is unavailable")
		}
		if r == nil {
			return nil, fmt.Errorf("panel request %s failed: empty response", safeEndpoint(path))
		}
		// Some legacy panels have no native device-count endpoint. Preserve
		// their cold-start behavior only for an explicitly missing API; never
		// replace a previously published snapshot or hide TLS/auth failures.
		if (r.StatusCode() == 404 || r.StatusCode() == 405) && (c.AliveMap == nil || c.AliveMap.Alive == nil) {
			return map[int]int{}, nil
		}
		return nil, fmt.Errorf("panel request %s failed: HTTP %d", safeEndpoint(path), r.StatusCode())
	}
	if err := json.Unmarshal(r.Body(), alive); err != nil {
		return nil, fmt.Errorf("panel request %s failed: invalid alive list", safeEndpoint(path))
	}
	if c.Cluster != nil && (alive.Alive == nil || alive.IPs == nil) {
		return nil, fmt.Errorf("cluster alive list is missing required fields")
	}

	// Native XBoard returns only a count. The optional plugin endpoint adds
	// identities so a known client can move to another replica at its limit.
	if c.Cluster == nil && alive.IPs == nil && len(alive.Alive) > 0 {
		if rich, err := c.client.R().Get("/api/v1/arinode/devices/alivelist"); err == nil && rich != nil && rich.StatusCode() == 200 {
			var devices AliveMap
			if json.Unmarshal(rich.Body(), &devices) == nil && devices.Alive != nil && devices.IPs != nil {
				alive = &devices
			}
		}
	}
	c.AliveMap = alive
	c.AliveIPs = alive.IPs
	return c.AliveMap.Alive, nil
}

type UserTraffic struct {
	UID      int
	Upload   int64
	Download int64
}

// ReportUserTraffic reports the user traffic
func (c *Client) ReportUserTraffic(userTraffic []UserTraffic) error {
	data := make(map[int][]int64, len(userTraffic))
	for i := range userTraffic {
		row := userTraffic[i]
		if t := data[row.UID]; t != nil {
			t[0] += row.Upload
			t[1] += row.Download
		} else {
			data[row.UID] = []int64{row.Upload, row.Download}
		}
	}
	path := c.endpoint("push")
	r, err := c.client.R().
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	err = c.checkResponse(r, path, err)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) ReportNodeOnlineUsers(data *map[int][]string) error {
	path := c.endpoint("alive")
	r, err := c.client.R().
		SetBody(data).
		ForceContentType("application/json").
		Post(path)
	err = c.checkResponse(r, path, err)

	return err
}
