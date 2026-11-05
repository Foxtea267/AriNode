package dsom

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGatewayExitChangesHistoryAndIPv6(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "provider-user", "provider-password")
	t.Setenv("DSOM_TEST_USER", "provider-user")
	t.Setenv("DSOM_TEST_PASSWORD", "provider-password")
	e := proxy.endpoint()
	e.UsernameEnv = "DSOM_TEST_USER"
	e.PasswordEnv = "DSOM_TEST_PASSWORD"
	c := testConfig("bd", e, sources.sources)
	c.Mode = "gateway"
	w := workerFixture(t, c)
	first := checkWorker(t, w)
	if !reflect.DeepEqual(first.ExitIPs, []string{"116.206.253.134"}) || first.Active != "gateway" || sources.requests.Load() != 2 || proxy.forwarded.Load() != 2 {
		t.Fatal("probe did not traverse the authenticated gateway and independent sources")
	}
	checkWorker(t, w)
	if len(w.status.History) != 1 {
		t.Fatal("unchanged exit duplicated history")
	}
	proxy.exit.Store("8.8.8.8")
	next := checkWorker(t, w)
	if len(next.History) != 2 || !next.LastSwitch.IsZero() {
		t.Fatal("IP change switched transport or was not recorded")
	}
	proxy.exit.Store("2606:4700:4700::1111")
	next = checkWorker(t, w)
	if next.ExitIPs[0] != "2606:4700:4700::1111" || len(next.History) != 3 {
		t.Fatal("IPv6 egress missing")
	}
	sources.mode.Store(3)
	next = checkWorker(t, w)
	if len(next.History) != 3 || next.ExitIPs[0] != "2606:4700:4700::1111" {
		t.Fatal("untrusted/private source response changed the exit")
	}
	info, err := os.Stat(w.historyPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("history permissions")
	}
	if len(loadHistory(w.historyPath)) != 3 {
		t.Fatal("history not persistent")
	}
	data, _ := json.Marshal(next)
	for _, secret := range []string{"provider-user", "provider-password", w.token} {
		if strings.Contains(string(data), secret) {
			t.Fatal("credentials leaked into status")
		}
	}
}

func TestExitDetectionNeverCreatesDirectTarget(t *testing.T) {
	sources := sourcesFixture(t)
	proxy := proxyFixture(t, sources.ports, "", "")
	w := workerFixture(t, testConfig("bd", proxy.endpoint(), sources.sources))
	for i := 0; i < 6; i++ {
		proxy.exit.Store(fmt.Sprintf("8.8.4.%d", i+1))
		status := checkWorker(t, w)
		if status.Active != "gateway" || status.DirectState != "DIRECT_UNAVAILABLE" || status.DirectReason != "direct_address_not_configured" || !status.LastSwitch.IsZero() {
			t.Fatal("an observed exit became a direct endpoint")
		}
	}
	if proxy.calls.Load() != 12 {
		t.Fatal("unexpected connections beyond configured sources")
	}
}

func TestTCPOnlyAuthFailureAndFailedForwardingRejected(t *testing.T) {
	for _, mode := range []int32{1, 3, 4} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			sources := sourcesFixture(t)
			gateway := proxyFixture(t, sources.ports, "", "")
			direct := proxyFixture(t, sources.ports, "user", "password")
			direct.mode.Store(mode)
			t.Setenv("DSOM_DIRECT_USER", "user")
			t.Setenv("DSOM_DIRECT_PASSWORD", "password")
			e := direct.endpoint()
			e.UsernameEnv = "DSOM_DIRECT_USER"
			e.PasswordEnv = "DSOM_DIRECT_PASSWORD"
			c := testConfig("bd", gateway.endpoint(), sources.sources)
			c.Direct.Endpoint = e
			w := workerFixture(t, c)
			for i := 0; i < 3; i++ {
				status := checkWorker(t, w)
				if status.Active != "gateway" || status.DirectState != "DIRECT_UNAVAILABLE" {
					t.Fatal("TCP or handshake alone was accepted as usable proxy")
				}
			}
			if direct.forwarded.Load() != 0 {
				t.Fatal("failed direct fixture forwarded requests")
			}
		})
	}
}

func TestAutoFailoverRecoveryCooldownAndDynamicAddress(t *testing.T) {
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	direct := proxyFixture(t, sources.ports, "", "")
	replacement := proxyFixture(t, sources.ports, "", "")
	direct.exit.Store("8.8.8.8")
	replacement.exit.Store("8.8.4.4")
	path := filepath.Join(t.TempDir(), "direct.json")
	write := func(e Endpoint) {
		data, _ := json.Marshal(e)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(direct.endpoint())
	c := testConfig("bd", gateway.endpoint(), sources.sources)
	c.Direct.EndpointFile = path
	w := workerFixture(t, c)
	if status := checkWorker(t, w); status.Active != "gateway" {
		t.Fatal("success threshold bypassed")
	}
	if status := checkWorker(t, w); status.Active != "direct" || status.ExitIPs[0] != "8.8.8.8" {
		t.Fatal("verified direct was not activated")
	}
	echo := echoFixture(t)
	held := relayDial(t, w, echo.Addr().String())
	defer held.Close()
	assertEcho(t, held, "before")
	direct.mode.Store(2)
	checkWorker(t, w)
	if w.status.Active != "direct" {
		t.Fatal("failure threshold bypassed")
	}
	if status := checkWorker(t, w); status.Active != "gateway" {
		t.Fatal("direct outage did not fall back")
	}
	assertEcho(t, held, "after") // Old connections survive an upstream switch.
	viaGateway := relayDial(t, w, echo.Addr().String())
	assertEcho(t, viaGateway, "gateway")
	viaGateway.Close()
	direct.mode.Store(0)
	checkWorker(t, w)
	checkWorker(t, w)
	if w.status.Active != "gateway" {
		t.Fatal("cooldown ignored")
	}
	w.mu.Lock()
	w.status.LastSwitch = time.Now().Add(-time.Hour)
	w.mu.Unlock()
	if status := checkWorker(t, w); status.Active != "direct" {
		t.Fatal("recovery did not switch back")
	}
	replacement.mode.Store(1)
	write(replacement.endpoint())
	checkWorker(t, w)
	if w.selected.Load().endpoint.Port != direct.endpoint().Port {
		t.Fatal("unverified address replaced a working direct")
	}
	replacement.mode.Store(0)
	checkWorker(t, w)
	w.mu.Lock()
	w.status.LastSwitch = time.Now().Add(-time.Hour)
	w.mu.Unlock()
	status := checkWorker(t, w)
	if status.ActiveAddress.Port != replacement.endpoint().Port || status.ExitIPs[0] != "8.8.4.4" {
		t.Fatal("updated authorized endpoint was not validated and activated")
	}
}

func TestSourceFailureAndDisagreementDoNotSwitch(t *testing.T) {
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	direct := proxyFixture(t, sources.ports, "", "")
	direct.exit.Store("8.8.8.8")
	c := testConfig("bd", gateway.endpoint(), sources.sources)
	c.Direct.Endpoint = direct.endpoint()
	w := workerFixture(t, c)
	checkWorker(t, w)
	checkWorker(t, w)
	switched := w.status.LastSwitch
	for _, mode := range []int32{1, 2} {
		sources.mode.Store(mode)
		for i := 0; i < 4; i++ {
			status := checkWorker(t, w)
			if status.Active != "direct" || status.ExitIPs[0] != "8.8.8.8" || !status.LastSwitch.Equal(switched) {
				t.Fatal("a source outage/disagreement caused false failover")
			}
		}
	}
}

func TestDirectModeFailsClosedAndGatewayOutageDoesNotCrash(t *testing.T) {
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	gateway.mode.Store(2)
	w := workerFixture(t, testConfig("bd", gateway.endpoint(), sources.sources))
	for i := 0; i < 3; i++ {
		status := checkWorker(t, w)
		if status.Active != "gateway" || status.LastFailure == "" {
			t.Fatal("gateway failure lost its recoverable state")
		}
	}
	c := testConfig("strict", gateway.endpoint(), sources.sources)
	c.Mode = "direct"
	strict := workerFixture(t, c)
	if status := checkWorker(t, strict); status.Active != "unavailable" || strict.selected.Load() != nil {
		t.Fatal("strict direct mode silently used a gateway")
	}
}

func TestReconcilePreservesRelayAndRuntimeAcrossPanelLikeResync(t *testing.T) {
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	c := testConfig("bd", gateway.endpoint(), sources.sources)
	m := New(t.TempDir())
	if err := m.Reconcile([]Config{c}); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	outbounds := m.Outbounds()
	worker := m.workers["bd"]
	for i := 0; i < 20; i++ {
		if err := m.Reconcile([]Config{c}); err != nil {
			t.Fatal(err)
		}
	}
	if worker != m.workers["bd"] || !reflect.DeepEqual(m.Outbounds(), outbounds) {
		t.Fatal("configuration sync reset runtime relay or state")
	}
	c.Mode = "gateway"
	if err := m.Reconcile([]Config{c}); err != nil {
		t.Fatal(err)
	}
	if worker != m.workers["bd"] || !reflect.DeepEqual(m.Outbounds(), outbounds) {
		t.Fatal("hot update rebuilt the relay")
	}
	bad := c
	bad.Mode = "invalid"
	if err := m.Reconcile([]Config{bad}); err == nil {
		t.Fatal("accepted invalid update")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Diagnose(ctx, "127.0.0.1", gateway.endpoint().Port, 2, time.Second, 0); err == nil {
		t.Fatal("diagnostic ignored cancellation")
	}
}

func TestFallbackGatewayAndRecoveryUseThresholds(t *testing.T) {
	sources := sourcesFixture(t)
	primary := proxyFixture(t, sources.ports, "", "")
	backup := proxyFixture(t, sources.ports, "", "")
	backup.exit.Store("1.1.1.1")
	primary.mode.Store(2)
	c := testConfig("bd", primary.endpoint(), sources.sources)
	c.Mode = "gateway"
	c.FallbackGateways = []Endpoint{backup.endpoint()}
	w := workerFixture(t, c)
	if state := checkWorker(t, w); state.Active != "gateway" {
		t.Fatal("backup bypassed configured success threshold")
	}
	if state := checkWorker(t, w); state.Active != "fallback-1" || state.ExitIPs[0] != "1.1.1.1" {
		t.Fatal("verified fallback gateway was not activated")
	}
	primary.mode.Store(0)
	checkWorker(t, w)
	checkWorker(t, w)
	if w.status.Active != "fallback-1" {
		t.Fatal("gateway recovery bypassed cooldown")
	}
	w.mu.Lock()
	w.status.LastSwitch = time.Now().Add(-time.Hour)
	w.mu.Unlock()
	if state := checkWorker(t, w); state.Active != "gateway" {
		t.Fatal("primary gateway recovery did not restore preferred gateway")
	}
}

func TestExistingDirectCannotRemainLabeledDirectWhenItBecomesGateway(t *testing.T) {
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	direct := proxyFixture(t, sources.ports, "", "")
	path := filepath.Join(t.TempDir(), "direct.json")
	data, _ := json.Marshal(direct.endpoint())
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c := testConfig("bd", gateway.endpoint(), sources.sources)
	c.Direct.EndpointFile = path
	w := workerFixture(t, c)
	checkWorker(t, w)
	checkWorker(t, w)
	if w.status.Active != "direct" {
		t.Fatal("fixture did not activate direct")
	}
	value := direct.endpoint()
	c.Gateway = &value
	normalized, _ := normalize(c)
	w.update(normalized)
	state := checkWorker(t, w)
	if state.Active != "gateway" || state.DirectState != "DIRECT_UNAVAILABLE" {
		t.Fatal("gateway alias was falsely reported as bypassed")
	}
}

func TestConcurrentConfigStatusAndDataForwarding(t *testing.T) {
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	c := testConfig("bd", gateway.endpoint(), sources.sources)
	m := New(t.TempDir())
	if err := m.Reconcile([]Config{c}); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	w := m.workers["bd"]
	echo := echoFixture(t)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for i := 0; i < 30; i++ {
			next := c
			if i%2 == 0 {
				next.Mode = "gateway"
			}
			if err := m.Reconcile([]Config{next}); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer group.Done()
		for i := 0; i < 100; i++ {
			m.Snapshot()
			m.Outbounds()
		}
	}()
	for i := 0; i < 10; i++ {
		connection := relayDial(t, w, echo.Addr().String())
		assertEcho(t, connection, "concurrent")
		connection.Close()
	}
	group.Wait()
}

func TestAuthenticatedButBrokenDirectForwardingFallsBack(t *testing.T) {
	sources := sourcesFixture(t)
	gateway := proxyFixture(t, sources.ports, "", "")
	direct := proxyFixture(t, sources.ports, "", "")
	direct.exit.Store("8.8.8.8")
	c := testConfig("bd", gateway.endpoint(), sources.sources)
	c.Direct.Endpoint = direct.endpoint()
	w := workerFixture(t, c)
	checkWorker(t, w)
	checkWorker(t, w)
	if w.status.Active != "direct" {
		t.Fatal("fixture not active")
	}
	direct.mode.Store(5) // SOCKS handshake and CONNECT succeed, HTTP forwarding fails.
	checkWorker(t, w)
	state := checkWorker(t, w)
	if state.Active != "gateway" {
		t.Fatal("a previously valid direct proxy kept blackholing traffic after forwarding failed")
	}
}
