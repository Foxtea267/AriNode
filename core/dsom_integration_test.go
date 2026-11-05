//go:build sing && xray

package core_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/dsom"
	"github.com/Foxtea267/AriNode/limiter"
)

func TestDSOMNativeKernelsProxyChainAndHotGatewayChange(t *testing.T) {
	for _, kernel := range []string{"xray", "sing"} {
		t.Run(kernel, func(t *testing.T) {
			gateway := startPolicyExit(t, "", true)
			replacement := startPolicyExit(t, "", true)
			chain := startPolicyExit(t, "", true)
			var sources []dsom.Source
			for i := 0; i < 2; i++ {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "116.206.253.134") }))
				defer server.Close()
				sources = append(sources, dsom.Source{URL: server.URL})
			}
			endpoint := func(exit *policyExit) dsom.Endpoint {
				host, port, _ := net.SplitHostPort(exit.listener.Addr().String())
				number, _ := strconv.Atoi(port)
				return dsom.Endpoint{Server: host, Port: number}
			}
			first := endpoint(gateway)
			config := dsom.Config{Tag: "bd", Enabled: true, Mode: "gateway", Gateway: &first, HealthCheck: dsom.HealthCheck{Timeout: 1, SuccessThreshold: 1, Sources: sources}}
			manager := dsom.New(t.TempDir())
			if err := manager.Reconcile([]dsom.Config{config}); err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			nativeConfig := conf.CoreConfig{Type: kernel}
			if kernel == "xray" {
				nativeConfig.XrayConfig = conf.NewXrayConfig()
			} else {
				nativeConfig.SingConfig = conf.NewSingConfig()
				nativeConfig.SingConfig.LogConfig.Disabled = true
			}
			server, err := core.NewCore([]conf.CoreConfig{nativeConfig})
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			options := &conf.Options{Core: kernel, ListenIP: "127.0.0.1"}
			if err := options.UseCore(kernel); err != nil {
				t.Fatal(err)
			}
			child := chain.outbound("child")
			child.ProxyTag = "bd"
			outbounds := manager.Outbounds()
			outbounds = append(outbounds, child)
			info := &panel.NodeInfo{Id: 1, Type: "vless", Common: &panel.CommonNode{ServerPort: policyPort(t), CustomOutbounds: outbounds,
				CustomRouteRules: []panel.CustomRouteRule{{Name: "dynamic", Match: panel.RouteMatch{Networks: []string{"tcp"}}, Action: panel.RouteAction{Type: "route", Target: "child"}}}}, VAllss: &panel.VAllssNode{Network: "tcp"}}
			users := []panel.UserInfo{{Id: 1, Uuid: policyUUID}}
			limiter.Init()
			limiter.AddLimiter("HK-DSOM", &conf.LimitConfig{}, users, map[int]int{})
			defer limiter.DeleteLimiter("HK-DSOM")
			if err := server.AddNode("HK-DSOM", info, options); err != nil {
				t.Fatal(err)
			}
			defer server.DelNode("HK-DSOM")
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: "HK-DSOM", NodeInfo: info, Users: users}); err != nil {
				t.Fatal(err)
			}
			echo, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer echo.Close()
			go func() {
				for {
					connection, err := echo.Accept()
					if err != nil {
						return
					}
					go func() { defer connection.Close(); io.Copy(connection, connection) }()
				}
			}()
			held := policyDial(t, info.Common.ServerPort, echo.Addr().String(), "ping")
			defer held.Close()
			second := endpoint(replacement)
			config.Gateway = &second
			if err := manager.Reconcile([]dsom.Config{config}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) {
				status := manager.Snapshot()
				if len(status) > 0 && status[0].ActiveAddress.Port == second.Port {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if manager.Snapshot()[0].ActiveAddress.Port != second.Port {
				t.Fatal("hot gateway replacement did not validate and activate")
			}
			if !reflect.DeepEqual(manager.Outbounds(), outbounds[:len(outbounds)-1]) {
				t.Fatal("gateway switch changed native outbound configuration")
			}
			if _, err := held.Write([]byte("held")); err != nil {
				t.Fatal(err)
			}
			var response [4]byte
			if _, err := io.ReadFull(held, response[:]); err != nil || string(response[:]) != "held" {
				t.Fatal("existing native connection was interrupted")
			}
			policyDial(t, info.Common.ServerPort, echo.Addr().String(), "ping").Close()
			if !gateway.seen(chain.listener.Addr().String()) || !replacement.seen(chain.listener.Addr().String()) {
				t.Fatal("native proxy chain did not traverse both configured gateways")
			}
		})
	}
}
