package limiter

import (
	log "github.com/sirupsen/logrus"
	"net/netip"
	"strings"
	"sync"
)

func canonicalDeviceIP(ip string) string {
	if addr, err := netip.ParseAddr(ip); err == nil {
		return addr.Unmap().WithZone("").String()
	}
	return ip
}

// Atomic local admission joins the panel's global IP set with this instance's
// recent and newly admitted IPs. A replica is not itself a user device.
func (l *Limiter) admitDeviceIP(user string, uid int, ip string, limit int) bool {
	l.aliveMu.Lock()
	defer l.aliveMu.Unlock()
	v, _ := l.UserOnlineIP.LoadOrStore(user, new(sync.Map))
	local := v.(*sync.Map)
	_, localKnown := local.Load(ip)
	known := localKnown || l.aliveIPs[uid][ip]
	if known || limit <= 0 {
		local.Store(ip, uid)
		return true
	}
	all := map[string]bool{}
	for address := range l.aliveIPs[uid] {
		all[address] = true
	}
	l.OldUserOnline.Range(func(key, value any) bool {
		if value.(int) == uid {
			all[key.(string)] = true
			if key.(string) == ip {
				known = true
			}
		}
		return true
	})
	local.Range(func(key, _ any) bool { all[key.(string)] = true; return true })
	count := len(all)
	if l.AliveList[uid] > count {
		count = l.AliveList[uid]
	}
	if limit > 0 && !known && count >= limit {
		node, _, _ := strings.Cut(user, "|")
		log.WithFields(log.Fields{"node": node, "device_limit": limit, "observed_devices": count}).Debug("New device rejected at IP limit; existing replica clients remain allowed")
		return false
	}
	local.Store(ip, uid)
	return true
}

func (l *Limiter) SetAliveList(counts map[int]int, ips map[int][]string) {
	l.aliveMu.Lock()
	defer l.aliveMu.Unlock()
	l.AliveList = make(map[int]int, len(counts))
	for uid, count := range counts {
		l.AliveList[uid] = count
	}
	l.aliveIPs = make(map[int]map[string]bool, len(ips))
	for uid, addresses := range ips {
		l.aliveIPs[uid] = make(map[string]bool, len(addresses))
		for _, ip := range addresses {
			l.aliveIPs[uid][canonicalDeviceIP(ip)] = true
		}
		// ips is the complete global set returned by the AriNode plugin.
		l.AliveList[uid] = len(l.aliveIPs[uid])
	}
}
func (l *Limiter) AllowedUserIDs() map[int]bool {
	allowed := make(map[int]bool)
	l.UserLimitInfo.Range(func(_, value any) bool { allowed[value.(*UserLimitInfo).UID] = true; return true })
	return allowed
}
