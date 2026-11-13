package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Foxtea267/AriNode/conf"
)

type Enrollment struct {
	MachineID  int    `json:"machine_id"`
	Token      string `json:"token"`
	Core       string `json:"core"`
	MultiNode  *bool  `json:"multi_node"`
	MultiPanel *bool  `json:"multi_panel"`
}

func Enroll(ctx context.Context, host, code string) (Enrollment, error) {
	var enrollment Enrollment
	body, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return enrollment, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/api/v1/arinode/enroll", bytes.NewReader(body))
	if err != nil {
		return enrollment, fmt.Errorf("invalid enrollment URL")
	}
	if req.URL.Scheme != "https" && !strings.EqualFold(req.URL.Hostname(), "localhost") && !net.ParseIP(req.URL.Hostname()).IsLoopback() {
		return enrollment, fmt.Errorf("panel enrollment requires HTTPS")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return enrollment, fmt.Errorf("could not contact panel enrollment endpoint")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return enrollment, fmt.Errorf("panel enrollment returned HTTP %d; check the plugin and generate a fresh command", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(data) > 8192 || json.Unmarshal(data, &enrollment) != nil ||
		enrollment.MachineID <= 0 || enrollment.Token == "" || len(enrollment.Token) > 1024 ||
		(enrollment.Core != "sing" && enrollment.Core != "xray") ||
		enrollment.MultiNode == nil || enrollment.MultiPanel == nil {
		return Enrollment{}, fmt.Errorf("panel returned invalid enrollment data")
	}
	return enrollment, nil
}

func (e Enrollment) Binding(host string) conf.MachineBinding {
	return conf.MachineBinding{APIHost: host, APIKey: e.Token, MachineID: e.MachineID, Core: e.Core, SingleNode: !*e.MultiNode}
}
