package core_test

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
	_ "github.com/Foxtea267/AriNode/core/sing"
	_ "github.com/Foxtea267/AriNode/core/xray"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/sagernet/sing-vmess/vless"
	M "github.com/sagernet/sing/common/metadata"
)

// SOCKS test exits observe real CONNECT requests. A chain relay actually opens
// the requested TCP connection; marker exits consume payload and reply from SG.
type policyExit struct {
	listener     net.Listener
	mu           sync.Mutex
	destinations []string
}

func startPolicyExit(t *testing.T, marker string, relay bool) *policyExit {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := &policyExit{listener: listener}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				var h [2]byte
				if _, err := io.ReadFull(conn, h[:]); err != nil {
					return
				}
				methods := make([]byte, int(h[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				conn.Write([]byte{5, 0})
				var header [4]byte
				if _, err := io.ReadFull(conn, header[:]); err != nil || header[1] != 1 {
					return
				}
				var host string
				switch header[3] {
				case 1:
					b := make([]byte, 4)
					if _, err := io.ReadFull(conn, b); err != nil {
						return
					}
					host = net.IP(b).String()
				case 4:
					b := make([]byte, 16)
					if _, err := io.ReadFull(conn, b); err != nil {
						return
					}
					host = net.IP(b).String()
				case 3:
					var size [1]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil {
						return
					}
					b := make([]byte, int(size[0]))
					if _, err := io.ReadFull(conn, b); err != nil {
						return
					}
					host = string(b)
				default:
					return
				}
				var port [2]byte
				if _, err := io.ReadFull(conn, port[:]); err != nil {
					return
				}
				destination := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:]))))
				e.mu.Lock()
				e.destinations = append(e.destinations, destination)
				e.mu.Unlock()
				conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				if relay {
					target, err := net.DialTimeout("tcp", destination, 5*time.Second)
					if err != nil {
						return
					}
					defer target.Close()
					go io.Copy(target, conn)
					io.Copy(conn, target)
				} else {
					var payload [4]byte
					if _, err := io.ReadFull(conn, payload[:]); err != nil {
						return
					}
					conn.Write([]byte(marker))
					io.Copy(io.Discard, conn)
				}
			}()
		}
	}()
	return e
}
func (e *policyExit) outbound(tag string) panel.OutboundConfig {
	host, portText, _ := net.SplitHostPort(e.listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	return panel.OutboundConfig{Tag: tag, Protocol: "socks", Settings: map[string]any{"servers": []any{map[string]any{"address": host, "port": port}}}}
}
func (e *policyExit) seen(destination string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range e.destinations {
		if d == destination {
			return true
		}
	}
	return false
}
func policyPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

const policyUUID = "11111111-1111-4111-8111-111111111111"

func policyDial(t *testing.T, port int, destination, want string) net.Conn {
	t.Helper()
	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetDeadline(time.Now().Add(8 * time.Second))
	client, _ := vless.NewClient(policyUUID, "", nil)
	conn, err := client.DialConn(raw, M.ParseSocksaddr(destination))
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	var response [4]byte
	if _, err := io.ReadFull(conn, response[:]); err != nil {
		conn.Close()
		t.Fatalf("%s via port %d: %v", destination, port, err)
	}
	if string(response[:]) != want {
		conn.Close()
		t.Fatalf("%s returned %q want %q", destination, response, want)
	}
	return conn
}
func TestPanelRoutingLiveIsolationAndHotUpdate(t *testing.T) {
	for _, kernel := range []string{"xray", "sing"} {
		t.Run(kernel, func(t *testing.T) {
			exitA := startPolicyExit(t, "SG-A", false)
			exitB := startPolicyExit(t, "SG-B", false)
			chain := startPolicyExit(t, "", true)
			hk, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer hk.Close()
			go func() {
				for {
					c, err := hk.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						c.SetDeadline(time.Now().Add(8 * time.Second))
						var b [4]byte
						if _, err := io.ReadFull(c, b[:]); err == nil {
							c.Write([]byte("HK!!"))
							io.Copy(io.Discard, c)
						}
					}()
				}
			}()
			var config conf.CoreConfig
			config.Type = kernel
			writeConfig := func(name string, value any) string {
				t.Helper()
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				return path
			}
			if kernel == "xray" {
				config.XrayConfig = conf.NewXrayConfig()
				config.XrayConfig.OutboundConfigPath = writeConfig("outbounds.json", []any{map[string]any{"tag": "sg01", "protocol": "socks", "settings": exitB.outbound("sg01").Settings}})
				config.XrayConfig.RouteConfigPath = writeConfig("routes.json", map[string]any{"rules": []any{map[string]any{"type": "field", "domain": []string{"domain:local.example"}, "outboundTag": "sg01"}}})
				config.XrayConfig.DnsConfigPath = writeConfig("dns.json", map[string]any{"hosts": map[string]any{"localhost": "127.0.0.1"}})
			} else {
				config.SingConfig = conf.NewSingConfig()
				host, portText, _ := net.SplitHostPort(exitB.listener.Addr().String())
				port, _ := strconv.Atoi(portText)
				config.SingConfig.OriginalPath = writeConfig("original.json", map[string]any{"dns": map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "static", "predefined": map[string]any{"localhost": "127.0.0.1"}}}, "final": "static"}, "outbounds": []any{map[string]any{"tag": "sg01", "type": "socks", "server": host, "server_port": port}, map[string]any{"tag": "global-direct", "type": "direct"}}, "route": map[string]any{"rules": []any{map[string]any{"domain_suffix": []string{"local.example"}, "outbound": "sg01"}}, "final": "global-direct"}})
			}
			server, err := core.NewCore([]conf.CoreConfig{config})
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			options := &conf.Options{Core: kernel, ListenIP: "127.0.0.1", ReportMinTraffic: 0}
			if err := options.UseCore(kernel); err != nil {
				t.Fatal(err)
			}
			users := []panel.UserInfo{{Id: 7, Uuid: policyUUID}}
			limiter.Init()
			node := func(id, port int, exit *policyExit) *panel.NodeInfo {
				return &panel.NodeInfo{Id: id, Type: "vless", Common: &panel.CommonNode{ServerPort: port, CustomOutbounds: []panel.OutboundConfig{exit.outbound("sg01")}, Routes: []panel.Route{{Id: 21, Match: []string{"claude.com", "*.claude.com", "anthropic.com"}, Action: "proxy", ActionValue: "sg01"}}}, VAllss: &panel.VAllssNode{Network: "tcp"}}
			}
			a := node(1, policyPort(t), exitA)
			b := node(2, policyPort(t), exitB)
			add := func(tag string, n *panel.NodeInfo) {
				l := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, map[int]int{})
				if err := l.UpdateRule(n.LimiterRules()); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { limiter.DeleteLimiter(tag) })
				if err := server.AddNode(tag, n, options); err != nil {
					t.Fatal(err)
				}
				if _, err := server.AddUsers(&core.AddUsersParams{Tag: tag, NodeInfo: n, Users: users}); err != nil {
					t.Fatal(err)
				}
			}
			add("HK-A", a)
			add("HK-B", b)
			policyDial(t, a.Common.ServerPort, "local.example:443", "SG-B").Close()
			held := policyDial(t, a.Common.ServerPort, "claude.com:443", "SG-A")
			defer held.Close()
			policyDial(t, b.Common.ServerPort, "child.claude.com:443", "SG-B").Close()
			policyDial(t, a.Common.ServerPort, hk.Addr().String(), "HK!!").Close()
			if !exitA.seen("claude.com:443") || !exitB.seen("child.claude.com:443") {
				t.Fatal("CONNECT did not reach selected exit")
			}
			updater := server.(core.NodePolicyUpdater)
			next := *a
			common := *a.Common
			next.Common = &common
			next.Common.CustomOutbounds = []panel.OutboundConfig{exitB.outbound("sg01")}
			if err := updater.UpdateNodePolicy("HK-A", &next, options); err != nil {
				t.Fatal(err)
			}
			policyDial(t, a.Common.ServerPort, "claude.com:443", "SG-B").Close()
			policyDial(t, b.Common.ServerPort, "claude.com:443", "SG-B").Close()
			held.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var one [1]byte
			if _, err := held.Read(one[:]); err == nil {
				t.Fatal("unexpected data")
			} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Fatalf("hot update closed old stream: %v", err)
			}
			held.Close()
			// Raw is lower priority than structured, and ordinary proxy is last.
			structured := *next.Common
			next.Common = &structured
			next.Common.CustomRouteRules = []panel.CustomRouteRule{{Name: "ai-direct", Match: panel.RouteMatch{DomainSuffixes: []string{"claude.com"}}, Action: panel.RouteAction{Type: "route", Target: "sg01"}}}
			raw := map[string]any{"outboundTag": "block", "domain": []string{"domain:claude.com"}}
			if kernel == "sing" {
				raw = map[string]any{"outbound": "block", "domain_suffix": []string{"claude.com"}}
			}
			next.Common.CustomRoutes = []map[string]any{raw}
			localhostTarget := net.JoinHostPort("localhost", strconv.Itoa(hk.Addr().(*net.TCPAddr).Port))
			next.Common.CustomRouteRules = append(next.Common.CustomRouteRules, panel.CustomRouteRule{Name: "source-and-ip", Match: panel.RouteMatch{Domains: []string{"localhost"}, IPCIDRs: []string{"127.0.0.0/8"}, Ports: []string{strconv.Itoa(hk.Addr().(*net.TCPAddr).Port)}, Networks: []string{"tcp"}, SourceCIDRs: []string{"127.0.0.0/8"}, SourcePorts: []string{"1-65535"}}, Action: panel.RouteAction{Type: "route", Target: "sg01"}})
			if err := updater.UpdateNodePolicy("HK-A", &next, options); err != nil {
				t.Fatal(err)
			}
			policyDial(t, a.Common.ServerPort, "claude.com:443", "SG-B").Close()
			policyDial(t, a.Common.ServerPort, localhostTarget, "SG-B").Close()
			next.Common.CustomRouteRules[1].Match.SourceCIDRs = []string{"192.0.2.0/24"}
			if err := updater.UpdateNodePolicy("HK-A", &next, options); err != nil {
				t.Fatal(err)
			}
			policyDial(t, a.Common.ServerPort, localhostTarget, "HK!!").Close()
			invalid := *next.Common
			bad := next
			bad.Common = &invalid
			bad.Common.CustomRouteRules = []panel.CustomRouteRule{{Name: "bad", Action: panel.RouteAction{Type: "route", Target: "missing"}}}
			if err := updater.UpdateNodePolicy("HK-A", &bad, options); err == nil {
				t.Fatal("invalid update silently fell back to direct")
			}
			policyDial(t, a.Common.ServerPort, "claude.com:443", "SG-B").Close()
			// A real two-hop chain must connect to the second SOCKS exit through relay.
			chained := *next.Common
			next.Common = &chained
			next.Common.CustomRoutes = nil
			next.Common.CustomRouteRules = nil
			chainOutbound := exitB.outbound("sg01")
			chainOutbound.ProxyTag = "relay"
			next.Common.CustomOutbounds = []panel.OutboundConfig{chainOutbound, chain.outbound("relay")}
			if err := updater.UpdateNodePolicy("HK-A", &next, options); err != nil {
				t.Fatal(err)
			}
			policyDial(t, a.Common.ServerPort, "claude.com:443", "SG-B").Close()
			if !chain.seen(exitB.listener.Addr().String()) {
				t.Fatal("proxy_tag did not establish outbound chain")
			}
			// User synchronization and counters remain on the existing inbound.
			traffic, err := server.GetUserTrafficSlice("HK-A", false)
			if err != nil || len(traffic) == 0 {
				t.Fatalf("lost traffic accounting: %v %v", traffic, err)
			}
			if err := server.DelUsers(users, "HK-A", a); err != nil {
				t.Fatal(err)
			}
			if _, err := server.AddUsers(&core.AddUsersParams{Tag: "HK-A", NodeInfo: a, Users: users}); err != nil {
				t.Fatal(err)
			}
			removedStream := policyDial(t, a.Common.ServerPort, "claude.com:443", "SG-B")
			defer removedStream.Close()
			otherStream := policyDial(t, b.Common.ServerPort, "claude.com:443", "SG-B")
			defer otherStream.Close()
			if err := server.DelNode("HK-A"); err != nil {
				t.Fatal(err)
			}
			removedStream.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, err := removedStream.Read(one[:]); err == nil {
				t.Fatal("deleted node stream remains usable")
			} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("deleted node stream was not closed")
			}
			otherStream.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if _, err := otherStream.Read(one[:]); err == nil {
				t.Fatal("unexpected data")
			} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Fatalf("node deletion closed another node's stream: %v", err)
			}
			policyDial(t, b.Common.ServerPort, "claude.com:443", "SG-B").Close()
			if err := server.DelNode("HK-B"); err != nil {
				t.Fatal(err)
			}
			t.Log(fmt.Sprintf("%s: SG exit, HK default, chain, hot update, retained stream, users, stats and node deletion verified", kernel))
		})
	}
}
