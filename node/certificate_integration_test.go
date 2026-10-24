//go:build sing && with_quic && with_grpc

package node

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/cert"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core/sing"
	"github.com/Foxtea267/AriNode/limiter"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
)

// Four certificates, matching the reported leaf + intermediate-chain case.
func trustedPanelChain(t *testing.T) (certificate, key, root string) {
	t.Helper()
	var parent *x509.Certificate
	var parentKey *ecdsa.PrivateKey
	var chain [][]byte
	for i := 0; i < 4; i++ {
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 1)), Subject: pkix.Name{CommonName: fmt.Sprintf("AriNode test issuer %d", i)},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), BasicConstraintsValid: true,
			IsCA: i < 3, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
		if i == 3 {
			template.DNSNames = []string{"node.example.com"}
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			template.KeyUsage = x509.KeyUsageDigitalSignature
		}
		if parent == nil {
			parent, parentKey = template, private
		}
		der, err := x509.CreateCertificate(rand.Reader, template, parent, &private.PublicKey, parentKey)
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, der)
		parent, err = x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		parentKey = private
		if i == 3 {
			encoded, err := x509.MarshalPKCS8PrivateKey(private)
			if err != nil {
				t.Fatal(err)
			}
			key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		certificate += string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: chain[i]}))
	}
	root = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: chain[0]}))
	return
}

func verifiedProtocolClient(t *testing.T, protocol string, port int, root string) *box.Box {
	t.Helper()
	const uuid = "11111111-1111-4111-8111-111111111111"
	tls := map[string]any{"enabled": true, "server_name": "node.example.com", "insecure": false, "certificate": []string{root}}
	out := map[string]any{"type": protocol, "server": "127.0.0.1", "server_port": port, "password": uuid, "tls": tls}
	if protocol == "tuic" {
		out["uuid"] = uuid
		out["congestion_control"] = "cubic"
		tls["alpn"] = []string{"h3"}
	}
	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry())
	data, _ := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{out}})
	options, err := sjson.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	client, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		client.Close()
		t.Fatal(err)
	}
	return client
}

func TestPanelCertificatesVerifiedForwardingAndRotation(t *testing.T) {
	for _, protocol := range []string{"anytls", "hysteria2", "tuic"} {
		t.Run(protocol, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			certificate, key, root := trustedPanelChain(t)
			var config atomic.Value
			var calls atomic.Int64
			set := func(certificate, key string) {
				data, _ := json.Marshal(map[string]any{"server_port": port, "tls_settings": map[string]any{"server_name": "node.example.com", "allow_insecure": false},
					"cert_config": map[string]any{"cert_mode": "content", "cert_content": certificate, "key_content": key},
					"base_config": map[string]int{"push_interval": 3600, "pull_interval": 3600}, "congestion_control": "cubic"})
				config.Store(string(data))
			}
			set(certificate, key)
			panelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/server/config":
					fmt.Fprintf(w, "{\"nonce\":%d,%s}", calls.Add(1), strings.TrimPrefix(strings.TrimSuffix(config.Load().(string), "}"), "{"))
				case "/api/v2/server/user":
					fmt.Fprint(w, `{"users":[{"id":1,"uuid":"11111111-1111-4111-8111-111111111111"}]}`)
				case "/api/v2/server/alivelist":
					fmt.Fprint(w, `{"alive":{}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer panelServer.Close()
			sc := conf.NewSingConfig()
			sc.LogConfig.Disabled = true
			kernel, err := sing.New(&conf.CoreConfig{SingConfig: sc})
			if err != nil {
				t.Fatal(err)
			}
			if err := kernel.Start(); err != nil {
				kernel.Close()
				t.Fatal(err)
			}
			defer kernel.Close()
			api, _ := panel.New(&conf.ApiConfig{APIHost: panelServer.URL, NodeID: 244, NodeType: protocol, MachineID: 39, Key: "test"})
			limiter.Init()
			controller := NewController(kernel, api, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig(), CertStorePath: t.TempDir()})
			if err := controller.Start(); err != nil {
				controller.Close()
				t.Fatal(err)
			}
			defer controller.Close()
			first := controller.runtimeOptions.Load()
			for i := 0; i < 20; i++ {
				controller.nodeInfoMonitor()
			}
			if controller.runtimeOptions.Load() != first {
				t.Fatal("stable configuration recreated TLS inbound")
			}
			for generation := 0; generation < 2; generation++ {
				if generation == 1 {
					certificate, key, root = trustedPanelChain(t)
					set(certificate, key)
					controller.nodeInfoMonitor()
					if controller.configError.Load() {
						t.Fatal("certificate rotation failed")
					}
				}
				client := verifiedProtocolClient(t, protocol, port, root)
				echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "verified panel certificate") }))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				conn, err := client.Outbound().Default().DialContext(ctx, "tcp", M.ParseSocksaddr(echo.Listener.Addr().String()))
				if err != nil {
					cancel()
					echo.Close()
					client.Close()
					t.Fatal(err)
				}
				conn.SetDeadline(time.Now().Add(8 * time.Second))
				fmt.Fprintf(conn, "GET / HTTP/1.0\r\nHost: echo.example\r\n\r\n")
				response, err := http.ReadResponse(bufio.NewReader(conn), nil)
				var responseBody []byte
				if err == nil {
					responseBody, err = io.ReadAll(response.Body)
					response.Body.Close()
				}
				conn.Close()
				cancel()
				echo.Close()
				client.Close()
				if err != nil || response.StatusCode != http.StatusOK || string(responseBody) != "verified panel certificate" {
					t.Fatalf("verified forwarding failed: %v", err)
				}
				if protocol == "anytls" {
					roots := x509.NewCertPool()
					roots.AppendCertsFromPEM([]byte(root))
					conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{RootCAs: roots, ServerName: "node.example.com", MinVersion: tls.VersionTLS12})
					if err != nil {
						t.Fatal(err)
					}
					if len(conn.ConnectionState().PeerCertificates) != 4 {
						t.Fatal("server did not present the complete four-certificate chain")
					}
					conn.Close()
				}
				active := controller.runtimeOptions.Load()
				material, err := cert.Load(active.CertConfig.CertFile, active.CertConfig.KeyFile)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("protocol=%s generation=%d verified=true certificate_sha256=%s", protocol, generation, material.Fingerprint)
				for i := 0; i < 20; i++ {
					controller.nodeInfoMonitor()
				}
				if controller.runtimeOptions.Load() != active {
					t.Fatal("certificate generation was applied more than once")
				}
			}
		})
	}
}
