package dsom

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func TestValidationAndUntrustedIPResults(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.1.2.3", "100.64.0.1", "192.0.2.3", "198.18.0.1", "255.255.255.255", "2001:db8::1", "::1", "::ffff:192.168.1.2", "8.8.8.8\n1.1.1.1", "<script>x</script>", "8.8.8.8:80"} {
		if _, ok := publicIP(value); ok {
			t.Fatalf("accepted untrusted result %q", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "116.206.253.134", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if _, ok := publicIP(value); !ok {
			t.Fatalf("rejected public address %q", value)
		}
	}
	base := Config{Tag: "bd", Enabled: true, Mode: "gateway", Gateway: &Endpoint{Server: "proxy.example", Port: 1080}}
	for _, change := range []func(*Config){
		func(c *Config) { c.Tag = "../escape" }, func(c *Config) { c.Tag = "direct" }, func(c *Config) { c.Mode = "scan" }, func(c *Config) { c.Gateway = nil },
		func(c *Config) { c.HealthCheck.Timeout = 100 }, func(c *Config) { c.HealthCheck.Interval = -1 }, func(c *Config) { c.HealthCheck.Retries = 100 },
		func(c *Config) {
			c.HealthCheck.Sources = []Source{{URL: "http://example.com/ip"}, {URL: "https://other.example/ip"}}
		},
		func(c *Config) {
			c.HealthCheck.Sources = []Source{{URL: "https://one.example/a"}, {URL: "https://one.example/b"}}
		},
		func(c *Config) { c.Metadata.URL = "https://user:password@example.com/{ip}" },
		func(c *Config) { c.Direct = Direct{Enabled: true, Endpoint: Endpoint{Server: "116.206.253.134"}} },
		func(c *Config) { c.Direct = Direct{Enabled: true, Endpoint: *c.Gateway} },
	} {
		c := base
		change(&c)
		if Validate([]Config{c}) == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	if Validate([]Config{base, base}) == nil {
		t.Fatal("duplicate tag accepted")
	}
	if err := Validate([]Config{base}); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataIsCachedAndExplicitlyProviderReported(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "", "")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"ip": r.URL.Path[1:], "success": true, "country_code": "BD", "connection": map[string]any{"asn": 12345, "org": "Fixture ISP", "type": "residential"}})
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	proxy.queryPorts[port] = true
	c := testConfig("bd", proxy.endpoint(), sources.sources)
	c.Metadata.URL = server.URL + "/{ip}"
	w := workerFixture(t, c)
	state := checkWorker(t, w)
	checkWorker(t, w)
	geo := state.Metadata["116.206.253.134"]
	if geo.Country != "BD" || geo.ASN != 12345 || geo.NetworkType != "residential" || geo.Source != "provider_reported" || calls.Load() != 1 {
		t.Fatal("metadata was not validated/cached or lacked provenance")
	}
}

func TestOptionalICMPParsingDoesNotConfuseTCPAndPacketLoss(t *testing.T) {
	result := parseICMP("10 packets transmitted, 8 received, 20% packet loss, time 9000ms\nrtt min/avg/max/mdev = 10.0/20.5/30.0/1.0 ms")
	if !result.Available || result.PacketLossPercent == nil || *result.PacketLossPercent != 20 || result.MeanRTTMS == nil || *result.MeanRTTMS != 20.5 {
		t.Fatal("valid ICMP measurement not parsed")
	}
	for _, output := range []string{"ping: permission denied", "0 connections failed", "999% packet loss"} {
		if result := parseICMP(output); result.Available || result.PacketLossPercent != nil {
			t.Fatal("unavailable ICMP invented a packet loss figure")
		}
	}
}

func TestCredentialFileRotationWithoutManagerRestart(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "secret-user", "new-secret")
	path := filepath.Join(t.TempDir(), "auth.json")
	write := func(password string) {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"username": "secret-user", "password": password})
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("wrong-secret")
	e := proxy.endpoint()
	e.CredentialsFile = path
	c := testConfig("bd", e, sources.sources)
	c.Mode = "gateway"
	c.HealthCheck.SuccessThreshold = 1
	w := workerFixture(t, c)
	state := checkWorker(t, w)
	if state.LastFailure != "socks5_authentication_failed" {
		t.Fatal("authentication failure was not distinguished")
	}
	write("new-secret")
	state = checkWorker(t, w)
	if len(state.ExitIPs) != 1 || state.ExitIPs[0] != "116.206.253.134" {
		t.Fatal("credential file change was not revalidated")
	}
	echo := echoFixture(t)
	connection := relayDial(t, w, echo.Addr().String())
	assertEcho(t, connection, "new-credentials")
	connection.Close()
}

func TestShutdownCancelsInFlightProbeRequests(t *testing.T) {
	var entered atomic.Int32
	ports := map[string]bool{}
	var sources []Source
	for i := 0; i < 2; i++ {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered.Add(1); <-r.Context().Done() }))
		t.Cleanup(server.Close)
		_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
		ports[port] = true
		sources = append(sources, Source{URL: server.URL})
	}
	proxy := proxyFixture(t, ports, "", "")
	config := testConfig("bd", proxy.endpoint(), sources)
	config.HealthCheck.Timeout = 30
	manager := New(t.TempDir())
	if err := manager.Reconcile([]Config{config}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if entered.Load() == 0 {
		manager.Close()
		t.Fatal("probe did not start")
	}
	done := make(chan struct{})
	go func() { manager.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("probe shutdown waited for HTTP timeout")
	}
}

func TestCredentialFilesAndAuthenticationCannotDowngrade(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "", "") // Always selects no-auth.
	file := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(file, []byte(`{"username":"secret-user","password":"secret-pass"}`), 0600); err != nil {
		t.Fatal(err)
	}
	e := proxy.endpoint()
	e.CredentialsFile = file
	resolved, err := resolve(e)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if connection, err := dialSOCKS(ctx, resolved, "example.com:80", &resolver{}, time.Minute); err == nil {
		connection.Close()
		t.Fatal("authenticated endpoint silently downgraded to no-auth")
	}
	if runtime.GOOS != "windows" {
		os.Chmod(file, 0644)
		if _, err := resolve(e); err == nil {
			t.Fatal("world-readable credential file accepted")
		}
		os.Chmod(file, 0600)
	}
	if err := os.WriteFile(file, []byte(`{"username":"x","password":"y"} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve(e); err == nil {
		t.Fatal("trailing credential data accepted")
	}
}

func TestSourceOriginsAndStrictModeReconfiguration(t *testing.T) {
	c := Config{Tag: "bd", Mode: "gateway", Enabled: true, Gateway: &Endpoint{Server: "gateway.example", Port: 1080}, HealthCheck: HealthCheck{Sources: []Source{{URL: "https://same.example/ip"}, {URL: "https://same.example:443/other"}}}}
	if Validate([]Config{c}) == nil {
		t.Fatal("the same origin counted twice toward consensus")
	}
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	c = testConfig("bd", gateway.endpoint(), sources.sources)
	w := workerFixture(t, c)
	checkWorker(t, w)
	c.Mode = "direct"
	c.Direct.Enabled = true
	normalized, _ := normalize(c)
	w.update(normalized)
	if w.selected.Load() != nil || len(w.status.ExitIPs) != 0 || w.status.ExitFresh {
		t.Fatal("strict mode continued using gateway or reported the old gateway exit as current")
	}
	value, _ := resolve(gateway.endpoint())
	w.selected.Store(&selection{name: "direct", endpoint: value, timeout: 1, dnsTTL: 60})
	c.Direct.Enabled = false
	normalized, _ = normalize(c)
	w.update(normalized)
	if w.selected.Load() != nil {
		t.Fatal("disabling direct in strict mode silently enabled gateway")
	}
}

func TestDNSCacheExpiryAndCancellation(t *testing.T) {
	count := 0
	dns := resolver{lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
		count++
		if count == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}}
	for i := 0; i < 5; i++ {
		ips, err := dns.addresses(context.Background(), "gateway.example", time.Minute)
		if err != nil || ips[0].String() != "8.8.8.8" {
			t.Fatal("DNS cache not reused")
		}
	}
	if count != 1 {
		t.Fatal("DNS resolved on each request")
	}
	dns.entries["gateway.example"] = dnsEntry{expires: time.Now().Add(-time.Second)}
	ips, err := dns.addresses(context.Background(), "gateway.example", time.Minute)
	if err != nil || ips[0].String() != "1.1.1.1" || count != 2 {
		t.Fatal("gateway DNS change not detected after TTL")
	}
}

func TestMetadataFailureNeverChangesTransport(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "", "")
	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ip":"1.1.1.1","success":true,"country_code":"NL","connection":{"asn":123,"type":"hosting"}}`)
	}))
	defer metadata.Close()
	_, port, _ := net.SplitHostPort(metadata.Listener.Addr().String())
	proxy.queryPorts[port] = true
	c := testConfig("bd", proxy.endpoint(), sources.sources)
	c.Metadata.URL = metadata.URL + "/{ip}"
	w := workerFixture(t, c)
	state := checkWorker(t, w)
	geo := state.Metadata["116.206.253.134"]
	if state.Active != "gateway" || geo.NetworkType != "unknown" || geo.Country != "" {
		t.Fatal("inconsistent metadata changed routing or was trusted")
	}
}

func TestRelayRequiresAuthenticationAndRejectsUDP(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "", "")
	w := workerFixture(t, testConfig("bd", proxy.endpoint(), sources.sources))
	connection, err := net.Dial("tcp", w.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	connection.SetDeadline(time.Now().Add(time.Second))
	connection.Write([]byte{5, 1, 0})
	var reply [2]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil || reply[1] != 255 {
		t.Fatal("relay exposed an unauthenticated proxy")
	}
	connection.Close()
	connection, err = net.Dial("tcp", w.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(time.Second))
	connection.Write([]byte{5, 1, 2})
	io.ReadFull(connection, reply[:])
	auth := append([]byte{1, 4}, []byte("dsom")...)
	auth = append(auth, byte(len(w.token)))
	auth = append(auth, []byte(w.token)...)
	connection.Write(auth)
	io.ReadFull(connection, reply[:])
	connection.Write([]byte{5, 3, 0, 1, 127, 0, 0, 1, 0, 53})
	if _, err := io.ReadFull(connection, reply[:]); err != nil || reply[1] != 7 {
		t.Fatal("UDP was not explicitly rejected")
	}
}

func TestBoundedHistoryAndGracefulClose(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "", "")
	w := workerFixture(t, testConfig("bd", proxy.endpoint(), sources.sources))
	for i := 0; i < 100; i++ {
		w.status.History = append(w.status.History, Change{At: time.Now(), Via: "gateway", IPs: []string{"8.8.8.8"}})
	}
	for i := 0; i < 8; i++ {
		proxy.exit.Store("116.206.253.134")
		checkWorker(t, w)
		proxy.exit.Store("8.8.4.4")
		checkWorker(t, w)
	}
	if len(w.status.History) != 100 || !w.status.LastSwitch.IsZero() {
		t.Fatal("frequent exit changes grew history or switched transport")
	}
	echo := echoFixture(t)
	connection := relayDial(t, w, echo.Addr().String())
	defer connection.Close()
	assertEcho(t, connection, "held")
	done := make(chan struct{})
	go func() { w.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DSOM did not stop active tunnels gracefully")
	}
	data, _ := json.Marshal(w.status)
	if string(data) == "" {
		t.Fatal("status unavailable after shutdown")
	}
}
