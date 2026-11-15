//go:build linux

package panel_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	_ "github.com/Foxtea267/AriNode/core/imports"
)

const dependenciesTLSHelper = "ARINODE_TEST_TLS_DEPENDENCIES"

// This loads core/imports under the supplied build tags and exercises the real
// panel client with Linux system roots in a fresh process. It does not start
// every core, run production services, or simulate an hours-long process.
func TestPanelTLSWithCoreDependenciesAndSystemRoots(t *testing.T) {
	if directory := os.Getenv(dependenciesTLSHelper); directory != "" {
		runDependenciesTLSChild(t, directory)
		return
	}
	directory := t.TempDir()
	root, goodCertificate, goodKey, recoveredCertificate, recoveredKey := dependenciesTLSChain(t)
	_, badCertificate, badKey, _, _ := dependenciesTLSChain(t)
	files := map[string][]byte{
		"roots.pem": root, "good.pem": goodCertificate, "good.key": goodKey,
		"bad.pem": badCertificate, "bad.key": badKey,
		"recovered.pem": recoveredCertificate, "recovered.key": recoveredKey,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	emptyDirectory := filepath.Join(directory, "empty-ca-directory")
	if err := os.Mkdir(emptyDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPanelTLSWithCoreDependenciesAndSystemRoots$", "-test.v")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "SSL_CERT_FILE=") && !strings.HasPrefix(variable, "SSL_CERT_DIR=") && !strings.HasPrefix(variable, dependenciesTLSHelper+"=") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "SSL_CERT_FILE="+filepath.Join(directory, "roots.pem"), "SSL_CERT_DIR="+emptyDirectory, dependenciesTLSHelper+"="+directory)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated core-dependency TLS test failed: %v\n%s", err, output)
	}
	t.Logf("isolated Linux system-root result:\n%s", output)
}

func runDependenciesTLSChild(t *testing.T, directory string) {
	t.Helper()
	loadPair := func(name string) tls.Certificate {
		pair, err := tls.LoadX509KeyPair(filepath.Join(directory, name+".pem"), filepath.Join(directory, name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		return pair
	}
	good, bad, recovered := loadPair("good"), loadPair("bad"), loadPair("recovered")
	var active atomic.Pointer[tls.Certificate]
	active.Store(&good)
	var requests atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/api/v2/server/machine/nodes" || request.Method != http.MethodPost {
			t.Errorf("unexpected local machine discovery endpoint")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[{"id":7,"type":"vless"}]}`))
	}))
	// GetConfigForClient also handles IP clients that send no SNI. StartTLS
	// requires an initial certificate, but each handshake uses the active one.
	server.TLS = &tls.Config{Certificates: []tls.Certificate{good}, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{Certificates: []tls.Certificate{*active.Load()}, MinVersion: tls.VersionTLS12}, nil
	}}
	server.StartTLS()
	defer server.Close()
	client, err := panel.New(&conf.ApiConfig{APIHost: server.URL, Key: "isolated-test-token", MachineID: 39, Timeout: 3})
	if err != nil {
		t.Fatal(err)
	}
	assertDiscovery := func(accepted bool) {
		t.Helper()
		nodes, err := client.GetMachineNodes()
		if accepted && (err != nil || len(nodes) != 1 || nodes[0].ID != 7) {
			t.Fatalf("strict system-root discovery failed: nodes=%+v err=%v", nodes, err)
		}
		if !accepted && (err == nil || nodes != nil) {
			t.Fatal("untrusted local chain was accepted")
		}
	}
	assertDiscovery(true)
	active.Store(&bad)
	server.CloseClientConnections() // previous request completed; force new TLS
	assertDiscovery(false)
	if requests.Load() != 1 {
		t.Fatal("untrusted TLS request entered the HTTP handler")
	}
	active.Store(&recovered)
	server.CloseClientConnections()
	assertDiscovery(true)
	if requests.Load() != 2 {
		t.Fatalf("unexpected successful request count: %d", requests.Load())
	}
}

func dependenciesTLSChain(t *testing.T) (rootPEM, certificate, key, recoveredCertificate, recoveredKey []byte) {
	t.Helper()
	now := time.Now()
	serial := int64(0)
	issue := func(name string, curve elliptic.Curve, ca bool, parent *x509.Certificate, signer *ecdsa.PrivateKey, key *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
		t.Helper()
		var err error
		if key == nil {
			key, err = ecdsa.GenerateKey(curve, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
		}
		serial++
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true, IsCA: ca, KeyUsage: x509.KeyUsageDigitalSignature}
		if ca {
			template.KeyUsage |= x509.KeyUsageCertSign
		} else {
			template.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
			template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		if parent == nil {
			parent, signer = template, key
		}
		der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, signer)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return parsed, key
	}
	root, rootKey := issue("isolated X2", elliptic.P384(), true, nil, nil, nil)
	_, yeKey := issue("isolated YE", elliptic.P256(), true, nil, nil, nil)
	cross, _ := issue("isolated YE", elliptic.P256(), true, root, rootKey, yeKey)
	intermediate, intermediateKey := issue("isolated YE2", elliptic.P384(), true, cross, yeKey, nil)
	leafPair := func() ([]byte, []byte) {
		leaf, private := issue("local panel", elliptic.P256(), false, intermediate, intermediateKey, nil)
		var chain []byte
		for _, item := range []*x509.Certificate{leaf, intermediate, cross} {
			chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: item.Raw})...)
		}
		encoded, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(fmt.Errorf("encode isolated test key: %w", err))
		}
		return chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	}
	certificate, key = leafPair()
	recoveredCertificate, recoveredKey = leafPair()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}), certificate, key, recoveredCertificate, recoveredKey
}
