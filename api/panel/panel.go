package panel

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/Foxtea267/AriNode/common/nodepolicy"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/go-resty/resty/v2"
)

// Panel is the interface for different panel's api.

type Client struct {
	Cluster          *conf.ClusterConfig
	AliveIPs         map[int][]string
	client           *resty.Client
	APIHost          string
	APISendIP        string
	Token            string
	NodeType         string
	NodeId           int
	MachineID        int
	nodeEtag         string
	userEtag         string
	responseBodyHash string
	pluginRetryAt    time.Time
	managedConfig    bool
	nodeBody         []byte
	lastLocalPresent bool
	LocalOutbounds   func() []nodepolicy.OutboundConfig
	LocalRoutes      []nodepolicy.CustomRouteRule
	UserList         *UserListBody
	AliveMap         *AliveMap
}

type panelRequestTraceKey struct{}

type panelRequestTrace struct {
	mu          sync.Mutex
	remoteAddr  string
	dialAddr    string
	resolvedIPs []string
}

func (p *panelRequestTrace) setRemoteAddr(address string) {
	p.mu.Lock()
	p.remoteAddr = address
	p.mu.Unlock()
}

func (p *panelRequestTrace) getRemoteAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remoteAddr
}

func (p *panelRequestTrace) setDialAddr(address string) {
	p.mu.Lock()
	p.dialAddr = address
	p.mu.Unlock()
}

func (p *panelRequestTrace) getDialAddr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dialAddr
}

func (p *panelRequestTrace) setResolvedIPs(addresses []string) {
	p.mu.Lock()
	p.resolvedIPs = append(p.resolvedIPs[:0], addresses...)
	p.mu.Unlock()
}

func (p *panelRequestTrace) getResolvedIPs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.resolvedIPs...)
}

func New(c *conf.ApiConfig) (*Client, error) {
	if c == nil {
		return nil, fmt.Errorf("panel configuration is required")
	}
	if err := c.Cluster.Validate(); err != nil {
		return nil, err
	}
	var client *resty.Client
	if c.APISendIP != "" {
		client = resty.NewWithLocalAddr(&net.TCPAddr{
			IP: net.ParseIP(c.APISendIP),
		})
	} else {
		client = resty.New()
	}
	client.SetRetryCount(3)
	if c.Timeout > 0 {
		client.SetTimeout(time.Duration(c.Timeout) * time.Second)
	} else {
		client.SetTimeout(5 * time.Second)
	}
	client.SetLogger(quietRestyLogger{})
	client.OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
		trace := &panelRequestTrace{}
		httpTrace := &httptrace.ClientTrace{
			DNSDone: func(info httptrace.DNSDoneInfo) {
				addresses := make([]string, 0, len(info.Addrs))
				for _, address := range info.Addrs {
					addresses = append(addresses, address.String())
				}
				trace.setResolvedIPs(addresses)
			},
			// A failed TLS handshake has no GotConn event. Under Happy Eyeballs
			// the last attempted dial is not necessarily the winning connection.
			ConnectStart: func(_, address string) { trace.setDialAddr(address) },
			GotConn: func(info httptrace.GotConnInfo) {
				if info.Conn != nil && info.Conn.RemoteAddr() != nil {
					trace.setRemoteAddr(info.Conn.RemoteAddr().String())
				}
			},
		}
		req.SetContext(context.WithValue(httptrace.WithClientTrace(req.Context(), httpTrace), panelRequestTraceKey{}, trace))
		return nil
	})
	client.OnError(func(req *resty.Request, err error) {
		if err == nil {
			return
		}
		requestURL := &url.URL{}
		method, requestAddress, attempts := "", "", 0
		if req != nil {
			requestURL, _ = url.Parse(req.URL)
			method = req.Method
			attempts = req.Attempt
			requestAddress = req.URL
		}
		fields := map[string]any{
			"panel":      SafePanelAddress(c.APIHost),
			"endpoint":   safeEndpoint(requestAddress),
			"method":     method,
			"attempts":   attempts,
			"node_id":    c.NodeID,
			"machine_id": c.MachineID,
			"error_kind": panelErrorKind(err),
		}
		if requestURL != nil {
			fields["tls_server_name"] = requestURL.Hostname()
		}
		if req != nil {
			if trace, ok := req.Context().Value(panelRequestTraceKey{}).(*panelRequestTrace); ok {
				if remoteAddress := trace.getRemoteAddr(); remoteAddress != "" {
					fields["remote_addr"] = remoteAddress
				}
				if dialAddress := trace.getDialAddr(); dialAddress != "" {
					fields["last_dial_addr"] = dialAddress
				}
				if resolvedIPs := trace.getResolvedIPs(); len(resolvedIPs) > 0 {
					fields["resolved_ips"] = resolvedIPs
				}
			}
		}
		transport, _ := client.GetClient().Transport.(*http.Transport)
		if transport != nil {
			if transport.TLSClientConfig == nil {
				fields["tls_roots"] = "system_default"
			} else {
				fields["tls_min_version"] = transport.TLSClientConfig.MinVersion
				if transport.TLSClientConfig.RootCAs == nil {
					fields["tls_roots"] = "system_default"
				} else {
					fields["tls_roots"] = "explicit_pool"
				}
				if transport.TLSClientConfig.ServerName != "" {
					fields["tls_server_name"] = transport.TLSClientConfig.ServerName
				}
			}
		}
		var verifyErr *tls.CertificateVerificationError
		if errors.As(err, &verifyErr) {
			fields["go_version"] = runtime.Version()
			fields["go_os"] = runtime.GOOS
			fields["go_arch"] = runtime.GOARCH
			if build, ok := debug.ReadBuildInfo(); ok {
				for _, setting := range build.Settings {
					switch setting.Key {
					case "GOEXPERIMENT", "CGO_ENABLED", "GOAMD64":
						fields["build_"+strings.ToLower(setting.Key)] = setting.Value
					}
				}
			}
			if transport == nil || transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
				roots, rootsErr := x509.SystemCertPool()
				fields["system_cert_pool_readable"] = rootsErr == nil && roots != nil
			}
			chain := make([]map[string]string, 0, len(verifyErr.UnverifiedCertificates))
			for _, certificate := range verifyErr.UnverifiedCertificates {
				if certificate == nil {
					continue
				}
				fingerprint := sha256.Sum256(certificate.Raw)
				spkiFingerprint := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
				chain = append(chain, map[string]string{
					"signature":   certificate.SignatureAlgorithm.String(),
					"sha256":      hex.EncodeToString(fingerprint[:]),
					"spki_sha256": hex.EncodeToString(spkiFingerprint[:]),
					"not_before":  certificate.NotBefore.UTC().Format(time.RFC3339),
					"not_after":   certificate.NotAfter.UTC().Format(time.RFC3339),
				})
			}
			fields["peer_cert_sha256"] = chain
		}
		logrus.WithFields(fields).Warn("Panel API request failed")
	})
	client.SetBaseURL(c.APIHost)
	// Check node type
	c.NodeType = strings.ToLower(c.NodeType)
	switch c.NodeType {
	case "":
		if c.MachineID <= 0 || c.NodeID != 0 {
			return nil, fmt.Errorf("node type is required")
		}
	case "v2ray":
		c.NodeType = "vmess"
	case
		"vmess",
		"trojan",
		"shadowsocks",
		"hysteria",
		"hysteria2",
		"tuic",
		"anytls",
		"mieru",
		"vless":
	default:
		return nil, fmt.Errorf("unsupported Node type: %s", c.NodeType)
	}
	// set params
	params := map[string]string{"node_id": strconv.Itoa(c.NodeID), "token": c.Key}
	if c.MachineID > 0 {
		params["machine_id"] = strconv.Itoa(c.MachineID)
	} else {
		params["node_type"] = c.NodeType
	}
	if c.NodeID > 0 || c.MachineID <= 0 {
		client.SetQueryParams(params)
	} else {
		client.SetRedirectPolicy(resty.NoRedirectPolicy())
		client.SetRetryCount(0)
	}
	return &Client{
		Cluster:   c.Cluster,
		client:    client,
		Token:     c.Key,
		APIHost:   c.APIHost,
		APISendIP: c.APISendIP,
		NodeType:  c.NodeType,
		NodeId:    c.NodeID,
		MachineID: c.MachineID,
		UserList:  &UserListBody{},
		AliveMap:  &AliveMap{},
	}, nil
}

func (c *Client) endpoint(action string) string {
	if c.MachineID > 0 {
		return "/api/v2/server/" + action
	}
	return "/api/v1/server/UniProxy/" + action
}
