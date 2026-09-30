package panel

import "github.com/Foxtea267/ariNode/common/sysstatus"

func (c *Client) ReportStatus(status sysstatus.Status) error {
	path := c.endpoint("status")
	r, err := c.client.R().
		SetBody(status).
		SetHeader("Content-Type", "application/json").
		Post(path)
	return c.checkResponse(r, path, err)
}

func (c *Client) ReportMachineStatus(status sysstatus.Status) error {
	if c.MachineID <= 0 {
		return nil
	}
	const path = "/api/v2/server/machine/status"
	r, err := c.client.R().
		SetBody(status).
		SetHeader("Content-Type", "application/json").
		Post(path)
	return c.checkResponse(r, path, err)
}
