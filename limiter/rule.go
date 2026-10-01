package limiter

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/Foxtea267/AriNode/api/panel"
)

func (l *Limiter) CheckDomainRule(destination string) (reject bool) {
	l.ruleMu.RLock()
	defer l.ruleMu.RUnlock()
	if addr, err := netip.ParseAddr(destination); err == nil {
		for _, prefix := range l.IPRules {
			if prefix.Contains(addr.Unmap()) {
				return true
			}
		}
	}
	// have rule
	for i := range l.DomainRules {
		if l.DomainRules[i].MatchString(destination) {
			reject = true
			break
		}
	}
	return
}

func (l *Limiter) CheckProtocolRule(protocol string) (reject bool) {
	l.ruleMu.RLock()
	defer l.ruleMu.RUnlock()
	for i := range l.ProtocolRules {
		if l.ProtocolRules[i] == protocol {
			reject = true
			break
		}
	}
	return
}

func (l *Limiter) UpdateRule(rule *panel.Rules) error {
	// Compile a complete replacement before publishing it to active connections.
	domains := make([]*regexp.Regexp, 0, len(rule.Regexp)+len(rule.Match))
	var prefixes []netip.Prefix
	for i := range rule.Regexp {
		compiled, err := regexp.Compile(rule.Regexp[i])
		if err != nil {
			return fmt.Errorf("invalid domain rule %d: %w", i, err)
		}
		domains = append(domains, compiled)
	}
	for i, raw := range rule.Match {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			prefixes = append(prefixes, prefix.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(value); err == nil {
			addr = addr.Unmap()
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		var pattern string
		switch {
		case strings.HasPrefix(value, "full:"):
			pattern = "^" + regexp.QuoteMeta(strings.TrimPrefix(value, "full:")) + "\\.?$"
		case strings.HasPrefix(value, "keyword:"):
			pattern = regexp.QuoteMeta(strings.TrimPrefix(value, "keyword:"))
		default:
			value = strings.TrimPrefix(value, "domain:")
			value = strings.TrimPrefix(value, "*.")
			value = strings.TrimSuffix(value, ".")
			if value == "" || strings.ContainsAny(value, ":/") {
				return fmt.Errorf("invalid native domain/IP rule %d", i)
			}
			// Domain suffixes include the domain itself and require a label boundary.
			pattern = "(^|\\.)" + strings.ReplaceAll(regexp.QuoteMeta(value), `\*`, ".*") + "\\.?$"
		}
		compiled, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return fmt.Errorf("invalid native domain rule %d: %w", i, err)
		}
		domains = append(domains, compiled)
	}
	l.ruleMu.Lock()
	l.DomainRules = domains
	l.IPRules = prefixes
	l.ProtocolRules = append([]string(nil), rule.Protocol...)
	l.ruleMu.Unlock()
	return nil
}
