package panel

import (
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
)

// The plugin serves a fresh, authenticated file snapshot and bypasses stale
// conditional responses. Older panels retain their native API and ETag behavior.
func (c *Client) nodeConfigResponse() (*resty.Response, bool, error) {
	if c.MachineID > 0 && !time.Now().Before(c.pluginRetryAt) {
		response, err := c.client.R().SetHeader("Cache-Control", "no-cache").Get("/api/v1/arinode/node/config")
		if err != nil {
			return nil, false, fmt.Errorf("plugin configuration request failed")
		}
		if response == nil {
			return nil, false, fmt.Errorf("empty plugin configuration response")
		}
		if response.StatusCode() == 200 && response.Header().Get("X-AriNode-Config") == "1" {
			return response, true, nil
		}
		switch response.StatusCode() {
		case 404, 405:
			c.pluginRetryAt = time.Now().Add(5 * time.Minute)
		case 200:
			// A panel SPA may answer a missing route with HTML and HTTP 200.
			c.pluginRetryAt = time.Now().Add(5 * time.Minute)
		default:
			return nil, false, fmt.Errorf("plugin configuration HTTP %d", response.StatusCode())
		}
	}
	path := c.endpoint("config")
	response, err := c.client.R().SetHeader("If-None-Match", c.nodeEtag).
		ForceContentType("application/json").Get(path)
	if err = c.checkResponse(response, path, err); err != nil {
		return nil, false, err
	}
	return response, false, nil
}
