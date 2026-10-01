package limiter

import (
	"sync"
	"testing"

	"github.com/Foxtea267/AriNode/api/panel"
)

func TestNativePanelBlockRules(t *testing.T) {
	l := &Limiter{}
	if err := l.UpdateRule(&panel.Rules{
		Match:  []string{"*.blocked.example", "plain.example", "full:exact.example", "keyword:tracker", "192.0.2.0/24", "2001:db8::/32", "198.51.100.7"},
		Regexp: []string{`^regex[0-9]+\.example$`}, Protocol: []string{"bittorrent"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"blocked.example", "sub.blocked.example", "SUB.BLOCKED.EXAMPLE.", "plain.example", "sub.plain.example", "exact.example", "mytracker.example", "192.0.2.42", "2001:db8::1", "198.51.100.7", "regex12.example"} {
		if !l.CheckDomainRule(host) {
			t.Errorf("expected %q to be blocked", host)
		}
	}
	for _, host := range []string{"notblocked.example", "blocked.example.net", "sub.exact.example", "192.0.3.42", "2001:db9::1", "198.51.100.8", "regex.example", "allowed.example"} {
		if l.CheckDomainRule(host) {
			t.Errorf("unexpected block of %q", host)
		}
	}
	if !l.CheckProtocolRule("bittorrent") || l.CheckProtocolRule("tls") {
		t.Fatal("protocol matching failed")
	}
}

func TestInvalidRuleUpdatePreservesActiveRules(t *testing.T) {
	l := &Limiter{}
	if err := l.UpdateRule(&panel.Rules{Match: []string{"*.blocked.example", "192.0.2.0/24"}, Protocol: []string{"bittorrent"}}); err != nil {
		t.Fatal(err)
	}
	for _, rules := range []*panel.Rules{{Regexp: []string{"valid", "*"}}, {Match: []string{"new.example", "192.0.2.0/99"}}} {
		if err := l.UpdateRule(rules); err == nil {
			t.Fatal("expected invalid rule error")
		}
		if !l.CheckDomainRule("sub.blocked.example") || !l.CheckDomainRule("192.0.2.1") || !l.CheckProtocolRule("bittorrent") || l.CheckDomainRule("new.example") {
			t.Fatal("invalid update changed active rules")
		}
	}
	if err := l.UpdateRule(&panel.Rules{}); err != nil {
		t.Fatal(err)
	}
	if l.CheckDomainRule("sub.blocked.example") || l.CheckDomainRule("192.0.2.1") || l.CheckProtocolRule("bittorrent") {
		t.Fatal("empty update failed to clear rules")
	}
}

func TestConcurrentRuleUpdates(t *testing.T) {
	l := &Limiter{}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 1000 {
			l.CheckDomainRule("blocked.example")
			l.CheckProtocolRule("bittorrent")
		}
	}()
	for range 100 {
		if err := l.UpdateRule(&panel.Rules{Match: []string{"*.blocked.example"}, Protocol: []string{"bittorrent"}}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
