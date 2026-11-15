package panel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/common/cert"
	"github.com/Foxtea267/AriNode/conf"
)

type tlsTestAuthority struct {
	rootYE          *x509.Certificate
	rootYEKey       *ecdsa.PrivateKey
	rootX2          *x509.Certificate
	rootX2Key       *ecdsa.PrivateKey
	intermediate    *x509.Certificate
	intermediateKey *ecdsa.PrivateKey
	crossSignedRoot []byte
}

func newTLSTestAuthority(t *testing.T) *tlsTestAuthority {
	t.Helper()
	now := time.Now()
	rootYEKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootX2Key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootYE := &x509.Certificate{
		SerialNumber:          tlsTestSerial(t),
		Subject:               pkix.Name{CommonName: "Root YE"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootYEDER, err := x509.CreateCertificate(rand.Reader, rootYE, rootYE, &rootYEKey.PublicKey, rootYEKey)
	if err != nil {
		t.Fatal(err)
	}
	rootYE, err = x509.ParseCertificate(rootYEDER)
	if err != nil {
		t.Fatal(err)
	}
	rootX2 := &x509.Certificate{
		SerialNumber:          tlsTestSerial(t),
		Subject:               pkix.Name{CommonName: "ISRG Root X2"},
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootX2DER, err := x509.CreateCertificate(rand.Reader, rootX2, rootX2, &rootX2Key.PublicKey, rootX2Key)
	if err != nil {
		t.Fatal(err)
	}
	rootX2, err = x509.ParseCertificate(rootX2DER)
	if err != nil {
		t.Fatal(err)
	}
	crossTemplate := &x509.Certificate{
		SerialNumber:          tlsTestSerial(t),
		Subject:               rootYE.Subject,
		Issuer:                rootX2.Subject,
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		SubjectKeyId:          rootYE.SubjectKeyId,
		AuthorityKeyId:        rootX2.SubjectKeyId,
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	crossDER, err := x509.CreateCertificate(rand.Reader, crossTemplate, rootX2, &rootYEKey.PublicKey, rootX2Key)
	if err != nil {
		t.Fatal(err)
	}
	crossRoot, err := x509.ParseCertificate(crossDER)
	if err != nil {
		t.Fatal(err)
	}
	intermediateKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	intermediateTemplate := &x509.Certificate{
		SerialNumber:          tlsTestSerial(t),
		Subject:               pkix.Name{CommonName: "YE2"},
		Issuer:                rootYE.Subject,
		NotBefore:             now.Add(-24 * time.Hour),
		NotAfter:              now.Add(48 * time.Hour),
		SubjectKeyId:          []byte{1, 2, 3, 4},
		AuthorityKeyId:        rootYE.SubjectKeyId,
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, intermediateTemplate, rootYE, &intermediateKey.PublicKey, rootYEKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediate, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		t.Fatal(err)
	}
	return &tlsTestAuthority{
		rootYE: rootYE, rootYEKey: rootYEKey,
		rootX2: rootX2, rootX2Key: rootX2Key,
		intermediate: intermediate, intermediateKey: intermediateKey,
		crossSignedRoot: crossRoot.Raw,
	}
}

func (a *tlsTestAuthority) issueLeaf(t *testing.T, serial int64, dnsNames []string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "backend.novonet.cc"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		DNSNames:              dnsNames,
		IPAddresses:           nil,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		AuthorityKeyId:        a.intermediate.SubjectKeyId,
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, a.intermediate, &leafKey.PublicKey, a.intermediateKey)
	if err != nil {
		t.Fatal(err)
	}
	chain := [][]byte{der, a.intermediate.Raw, a.crossSignedRoot}
	return tls.Certificate{Certificate: chain, PrivateKey: leafKey}
}

func tlsTestSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	return serial
}

func tlsTestRoots(root *x509.Certificate) *x509.CertPool {
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return roots
}

func startPanelTLSServer(t *testing.T, certificate func() *tls.Certificate, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{*certificate()}, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return certificate(), nil
	}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func newTLSPanelRestyClient(t *testing.T, serverURL string, roots *x509.CertPool) *Client {
	return newTLSPanelRestyClientWithSendIP(t, serverURL, roots, "")
}

func newTLSPanelRestyClientWithSendIP(t *testing.T, serverURL string, roots *x509.CertPool, sendIP string) *Client {
	t.Helper()
	client, err := New(&conf.ApiConfig{APIHost: serverURL, APISendIP: sendIP, Key: "test-token", NodeID: 1, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	client.client.SetTLSClientConfig(&tls.Config{RootCAs: roots, ServerName: "backend.novonet.cc", MinVersion: tls.VersionTLS12})
	return client
}

func panelTLSRequest(c *Client, expectedLeafSerial ...int64) error {
	response, err := c.client.R().Get(c.endpoint("config"))
	if err != nil {
		return err
	}
	if response.StatusCode() != http.StatusOK || string(response.Body()) != "ok" {
		return errors.New("panel TLS test endpoint returned an unexpected response")
	}
	if len(expectedLeafSerial) > 0 {
		state := response.RawResponse.TLS
		if state == nil || len(state.PeerCertificates) == 0 || state.PeerCertificates[0].SerialNumber.Int64() != expectedLeafSerial[0] {
			return errors.New("panel TLS request did not use the expected rotated leaf certificate")
		}
	}
	return nil
}

func TestPanelRestyTLSValidCrossSignedECDSAChain(t *testing.T) {
	authority := newTLSTestAuthority(t)
	now := time.Now()
	leaf := authority.issueLeaf(t, 10, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	var requests atomic.Int64
	server := startPanelTLSServer(t, func() *tls.Certificate { return &leaf }, &requests)
	client := newTLSPanelRestyClient(t, server.URL, tlsTestRoots(authority.rootX2))
	if err := panelTLSRequest(client); err != nil {
		t.Fatalf("valid Root YE cross-signed by ISRG Root X2 chain was rejected: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("server saw %d requests, want 1", requests.Load())
	}
}

func TestPanelRestyTLSRejectsInvalidChains(t *testing.T) {
	tests := []struct {
		name       string
		roots      func(*tlsTestAuthority) *x509.CertPool
		leaf       func(*testing.T, *tlsTestAuthority, time.Time) tls.Certificate
		checkError func(*testing.T, error)
	}{
		{
			name:  "untrusted root",
			roots: func(*tlsTestAuthority) *x509.CertPool { return x509.NewCertPool() },
			leaf: func(t *testing.T, a *tlsTestAuthority, now time.Time) tls.Certificate {
				return a.issueLeaf(t, 20, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
			},
			checkError: func(t *testing.T, err error) {
				var verifyErr *tls.CertificateVerificationError
				var unknown x509.UnknownAuthorityError
				if !errors.As(err, &verifyErr) || !errors.As(err, &unknown) {
					t.Fatalf("error %T %v does not report an untrusted certificate chain", err, err)
				}
			},
		},
		{
			name:  "invalid leaf signature",
			roots: func(a *tlsTestAuthority) *x509.CertPool { return tlsTestRoots(a.rootX2) },
			leaf: func(t *testing.T, a *tlsTestAuthority, now time.Time) tls.Certificate {
				certificate := a.issueLeaf(t, 30, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
				certificate.Certificate[0] = append([]byte(nil), certificate.Certificate[0]...)
				certificate.Certificate[0][len(certificate.Certificate[0])-1] ^= 1
				return certificate
			},
			checkError: func(t *testing.T, err error) {
				var verifyErr *tls.CertificateVerificationError
				if !errors.As(err, &verifyErr) {
					t.Fatalf("error %T %v does not report certificate verification failure", err, err)
				}
			},
		},
		{
			name:  "hostname mismatch",
			roots: func(a *tlsTestAuthority) *x509.CertPool { return tlsTestRoots(a.rootX2) },
			leaf: func(t *testing.T, a *tlsTestAuthority, now time.Time) tls.Certificate {
				return a.issueLeaf(t, 40, []string{"not-backend.invalid"}, now.Add(-time.Hour), now.Add(time.Hour))
			},
			checkError: func(t *testing.T, err error) {
				var verifyErr *tls.CertificateVerificationError
				var hostErr x509.HostnameError
				if !errors.As(err, &verifyErr) || !errors.As(err, &hostErr) {
					t.Fatalf("error %T %v does not report hostname verification failure", err, err)
				}
			},
		},
		{
			name:  "expired leaf",
			roots: func(a *tlsTestAuthority) *x509.CertPool { return tlsTestRoots(a.rootX2) },
			leaf: func(t *testing.T, a *tlsTestAuthority, now time.Time) tls.Certificate {
				return a.issueLeaf(t, 50, []string{"backend.novonet.cc"}, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
			},
			checkError: func(t *testing.T, err error) {
				var verifyErr *tls.CertificateVerificationError
				var invalid x509.CertificateInvalidError
				if !errors.As(err, &verifyErr) || !errors.As(err, &invalid) || invalid.Reason != x509.Expired {
					t.Fatalf("error %T %v does not report an expired certificate", err, err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			authority := newTLSTestAuthority(t)
			now := time.Now()
			leaf := tc.leaf(t, authority, now)
			var requests atomic.Int64
			server := startPanelTLSServer(t, func() *tls.Certificate { return &leaf }, &requests)
			client := newTLSPanelRestyClient(t, server.URL, tc.roots(authority))
			err := panelTLSRequest(client)
			if err == nil {
				t.Fatal("invalid certificate chain was accepted")
			}
			tc.checkError(t, err)
			if requests.Load() != 0 {
				t.Fatalf("server handler received %d requests despite a TLS failure", requests.Load())
			}
		})
	}
}

func TestPanelRestyTLSClientReuseAndConcurrentRequests(t *testing.T) {
	authority := newTLSTestAuthority(t)
	now := time.Now()
	leaf := authority.issueLeaf(t, 60, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	rotatedLeaf := authority.issueLeaf(t, 61, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	var active atomic.Pointer[tls.Certificate]
	active.Store(&leaf)
	var requests atomic.Int64
	server := startPanelTLSServer(t, func() *tls.Certificate { return active.Load() }, &requests)
	client := newTLSPanelRestyClient(t, server.URL, tlsTestRoots(authority.rootX2))
	for i := 0; i < 4; i++ {
		if err := panelTLSRequest(client); err != nil {
			t.Fatalf("reused client request %d failed: %v", i, err)
		}
	}
	// This is a bounded five-cycle reuse/refresh test, not an hours-long soak.
	// Keep the inbound material independent from the panel chain, as in normal
	// node certificate handling. Exercise the real normalization, parse, and
	// immutable certificate-store publication paths in the same process.
	inboundAuthority := newTLSTestAuthority(t)
	inboundFirst := inboundAuthority.issueLeaf(t, 600, []string{"inbound.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	inboundSecond := inboundAuthority.issueLeaf(t, 601, []string{"inbound.example.com"}, now.Add(-time.Hour), now.Add(time.Hour))
	inboundPairs := [2][2]string{
		tlsTestPEMPair(t, inboundFirst),
		tlsTestPEMPair(t, inboundSecond),
	}
	store := cert.Store{Root: t.TempDir()}
	const cycles, concurrent, inboundRotations = 5, 12, 4
	for cycle := 0; cycle < cycles; cycle++ {
		expectedSerial := int64(61)
		if cycle%2 == 0 {
			active.Store(&rotatedLeaf)
		} else {
			active.Store(&leaf)
			expectedSerial = 60
		}
		client.client.GetClient().CloseIdleConnections()
		start := make(chan struct{})
		errs := make(chan error, concurrent+1)
		var group sync.WaitGroup
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			seenGenerations := make(map[string]bool)
			for rotation := 0; rotation < inboundRotations; rotation++ {
				pair := inboundPairs[rotation%len(inboundPairs)]
				certificate, key, err := cert.CanonicalPair(pair[0], pair[1])
				if err != nil {
					errs <- fmt.Errorf("normalize inbound certificate: %w", err)
					return
				}
				parsed, err := cert.Parse([]byte(certificate), []byte(key))
				if err != nil {
					errs <- fmt.Errorf("parse inbound certificate: %w", err)
					return
				}
				material, err := store.Save("independent-inbound", certificate, key)
				if err != nil {
					errs <- fmt.Errorf("publish inbound certificate: %w", err)
					return
				}
				if !parsed.Equal(material.Leaf) || material.Leaf.VerifyHostname("inbound.example.com") != nil {
					errs <- errors.New("inbound publication changed the normalized leaf certificate")
					return
				}
				seenGenerations[material.Fingerprint] = true
				loaded, err := cert.Load(material.CertPath, material.KeyPath)
				if err != nil || loaded.Fingerprint != material.Fingerprint {
					errs <- fmt.Errorf("read back inbound certificate: fingerprint matches=%t err=%v", loaded != nil && loaded.Fingerprint == material.Fingerprint, err)
					return
				}
			}
			if len(seenGenerations) != len(inboundPairs) {
				errs <- errors.New("inbound certificate rotation did not publish both distinct leaf generations")
				return
			}
			errs <- nil
		}()
		for i := 0; i < concurrent; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				errs <- panelTLSRequest(client, expectedSerial)
			}()
		}
		close(start)
		group.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("reuse/refresh cycle %d failed: %v", cycle, err)
			}
		}
	}
	if got := requests.Load(); got != cycles*concurrent+4 {
		t.Fatalf("server saw %d requests, want %d", got, cycles*concurrent+4)
	}
}

func tlsTestPEMPair(t *testing.T, certificate tls.Certificate) [2]string {
	t.Helper()
	var chain strings.Builder
	for _, der := range certificate.Certificate {
		chain.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	return [2]string{chain.String(), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}
}

func TestPanelRestyTLSDefaultAndLocalAddressTransportsArePrivate(t *testing.T) {
	authority := newTLSTestAuthority(t)
	now := time.Now()
	leaf := authority.issueLeaf(t, 62, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	var requests atomic.Int64
	server := startPanelTLSServer(t, func() *tls.Certificate { return &leaf }, &requests)
	clients := make([]*Client, 0, 4)
	transports := make(map[*http.Transport]bool)
	for _, sendIP := range []string{"", "", "127.0.0.1", "127.0.0.1"} {
		client, err := New(&conf.ApiConfig{APIHost: server.URL, APISendIP: sendIP, Key: "test-token", NodeID: 1, NodeType: "vless"})
		if err != nil {
			t.Fatal(err)
		}
		transport, ok := client.client.GetClient().Transport.(*http.Transport)
		if !ok || transport == nil {
			t.Fatal("Resty did not create an HTTP transport")
		}
		if transports[transport] || transport == http.DefaultTransport {
			t.Fatal("panel bindings share an HTTP transport with each other or the global default")
		}
		if transport.TLSClientConfig != nil {
			t.Fatal("new panel transport unexpectedly has custom TLS trust settings")
		}
		for _, previous := range clients {
			if client.client.GetClient() == previous.client.GetClient() {
				t.Fatal("panel bindings share an HTTP client")
			}
		}
		transports[transport] = true
		clients = append(clients, client)
		client.client.SetTLSClientConfig(&tls.Config{RootCAs: tlsTestRoots(authority.rootX2), ServerName: "backend.novonet.cc", MinVersion: tls.VersionTLS12})
		if err := panelTLSRequest(client); err != nil {
			t.Fatalf("panel request with APISendIP=%q failed: %v", sendIP, err)
		}
	}
	if requests.Load() != int64(len(clients)) {
		t.Fatalf("server saw %d requests, want %d", requests.Load(), len(clients))
	}
	if host, _, err := net.SplitHostPort(server.Listener.Addr().String()); err != nil || host != "127.0.0.1" {
		t.Fatalf("transport test escaped the IPv4 loopback listener: host=%q err=%v", host, err)
	}
}

func TestPanelRestyTLSRootPoolsAreIsolated(t *testing.T) {
	firstAuthority := newTLSTestAuthority(t)
	secondAuthority := newTLSTestAuthority(t)
	now := time.Now()
	firstLeaf := firstAuthority.issueLeaf(t, 70, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	secondLeaf := secondAuthority.issueLeaf(t, 80, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	var firstRequests, secondRequests atomic.Int64
	firstServer := startPanelTLSServer(t, func() *tls.Certificate { return &firstLeaf }, &firstRequests)
	secondServer := startPanelTLSServer(t, func() *tls.Certificate { return &secondLeaf }, &secondRequests)
	firstClient := newTLSPanelRestyClient(t, firstServer.URL, tlsTestRoots(firstAuthority.rootX2))
	secondClient := newTLSPanelRestyClient(t, secondServer.URL, tlsTestRoots(secondAuthority.rootX2))
	if err := panelTLSRequest(firstClient); err != nil {
		t.Fatalf("first client rejected its panel CA: %v", err)
	}
	if err := panelTLSRequest(secondClient); err != nil {
		t.Fatalf("second client rejected its panel CA: %v", err)
	}
	if err := panelTLSRequest(firstClientWithURL(t, firstClient, secondServer.URL)); err == nil {
		t.Fatal("first client's trust pool accepted the second panel CA")
	}
	if err := panelTLSRequest(firstClientWithURL(t, secondClient, firstServer.URL)); err == nil {
		t.Fatal("second client's trust pool accepted the first panel CA")
	}
	if firstRequests.Load() != 1 || secondRequests.Load() != 1 {
		t.Fatalf("unexpected requests reached handlers: first=%d second=%d", firstRequests.Load(), secondRequests.Load())
	}
}

func firstClientWithURL(t *testing.T, original *Client, serverURL string) *Client {
	t.Helper()
	clone, err := New(&conf.ApiConfig{APIHost: serverURL, Key: "test-token", NodeID: 1, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	transport := original.client.GetClient().Transport.(*http.Transport)
	if transport.TLSClientConfig == nil {
		t.Fatal("test client did not retain its private TLS configuration")
	}
	clone.client.SetTLSClientConfig(transport.TLSClientConfig.Clone())
	return clone
}

func TestPanelRestyTLSCertificateRotationAfterIdleConnectionsClose(t *testing.T) {
	authority := newTLSTestAuthority(t)
	now := time.Now()
	firstLeaf := authority.issueLeaf(t, 90, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	secondLeaf := authority.issueLeaf(t, 91, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	var active atomic.Pointer[tls.Certificate]
	active.Store(&firstLeaf)
	var requests atomic.Int64
	server := startPanelTLSServer(t, func() *tls.Certificate { return active.Load() }, &requests)
	client := newTLSPanelRestyClient(t, server.URL, tlsTestRoots(authority.rootX2))
	if err := panelTLSRequest(client); err != nil {
		t.Fatalf("initial certificate was rejected: %v", err)
	}
	active.Store(&secondLeaf)
	client.client.GetClient().CloseIdleConnections()
	if err := panelTLSRequest(client); err != nil {
		t.Fatalf("rotated certificate under the same trusted chain was rejected: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("server saw %d requests, want 2", requests.Load())
	}
}

func TestPanelMachineDiscoveryTLSRefreshAfterCertificateRotation(t *testing.T) {
	authority := newTLSTestAuthority(t)
	now := time.Now()
	firstLeaf := authority.issueLeaf(t, 110, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	secondLeaf := authority.issueLeaf(t, 111, []string{"backend.novonet.cc"}, now.Add(-time.Hour), now.Add(time.Hour))
	var active atomic.Pointer[tls.Certificate]
	active.Store(&firstLeaf)
	var nodeID atomic.Int64
	nodeID.Store(1)
	var requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v2/server/machine/nodes" || r.Method != http.MethodPost {
			t.Errorf("unexpected machine discovery request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"nodes":[{"id":%d,"type":"vless"}]}`, nodeID.Load())
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{firstLeaf}, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return active.Load(), nil
	}}
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := New(&conf.ApiConfig{APIHost: server.URL, MachineID: 39, NodeType: "vless"})
	if err != nil {
		t.Fatal(err)
	}
	client.client.SetTLSClientConfig(&tls.Config{RootCAs: tlsTestRoots(authority.rootX2), ServerName: "backend.novonet.cc", MinVersion: tls.VersionTLS12})
	first, err := client.GetMachineNodes()
	if err != nil || len(first) != 1 || first[0].ID != 1 {
		t.Fatalf("initial machine discovery failed: nodes=%+v err=%v", first, err)
	}
	active.Store(&secondLeaf)
	nodeID.Store(2)
	client.client.GetClient().CloseIdleConnections()
	second, err := client.GetMachineNodes()
	if err != nil || len(second) != 1 || second[0].ID != 2 {
		t.Fatalf("machine discovery did not refresh after certificate rotation: nodes=%+v err=%v", second, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("server saw %d discovery requests, want 2", requests.Load())
	}
}
