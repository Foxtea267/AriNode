package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Foxtea267/AriNode/dsom"
	"github.com/spf13/cobra"
)

func init() {
	parent := &cobra.Command{Use: "dsom", Short: "Inspect dynamic SOCKS5 outbounds and diagnose an explicit gateway"}
	var address string
	status := &cobra.Command{Use: "status", Short: "Read DSOM state from the existing AriNode status API", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			u, err := url.Parse(address)
			if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("invalid status URL")
			}
			ip := net.ParseIP(u.Hostname())
			loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
			if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
				return fmt.Errorf("remote status URLs require HTTPS")
			}
			request, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, address, nil)
			if err != nil {
				return fmt.Errorf("invalid status request")
			}
			if token := os.Getenv("ARINODE_STATUS_TOKEN"); token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return fmt.Errorf("cannot connect to AriNode status API")
			}
			defer response.Body.Close()
			if response.StatusCode != 200 {
				return fmt.Errorf("status API HTTP %d", response.StatusCode)
			}
			var state struct {
				Outbounds []dsom.Status `json:"dynamic_outbounds"`
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
			if err != nil || len(data) > 4*1024*1024 || json.Unmarshal(data, &state) != nil {
				return fmt.Errorf("invalid status API response")
			}
			if state.Outbounds == nil {
				return fmt.Errorf("running AriNode does not expose DSOM status; upgrade the running service")
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(state.Outbounds)
		}}
	status.Flags().StringVar(&address, "url", "http://127.0.0.1:18086/v1/status", "existing status API URL")
	var host string
	var icmp bool
	var port, attempts, timeout, interval int
	diagnose := &cobra.Command{Use: "diagnose", Short: "Measure TCP latency/failure rate to one explicitly authorized host:port", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := dsom.Diagnose(cmd.Context(), host, port, attempts, time.Duration(timeout)*time.Second, time.Duration(interval)*time.Millisecond)
			if err != nil {
				return err
			}
			if icmp {
				value := dsom.ICMP(cmd.Context(), host, attempts, timeout)
				result.ICMP = &value
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}}
	diagnose.Flags().StringVar(&host, "host", "", "authorized gateway hostname")
	diagnose.Flags().BoolVar(&icmp, "icmp", false, "also request ICMP packet loss via existing Linux iputils ping (no privilege escalation)")
	diagnose.Flags().IntVar(&port, "port", 0, "explicit gateway port")
	diagnose.Flags().IntVar(&attempts, "attempts", 10, "TCP connect attempts (1-50)")
	diagnose.Flags().IntVar(&timeout, "timeout", 5, "timeout per attempt, seconds (1-30)")
	diagnose.Flags().IntVar(&interval, "interval", 1000, "interval between attempts, milliseconds")
	parent.AddCommand(status, diagnose)
	command.AddCommand(parent)
}
