package limiter

import (
	"net/netip"
	"sync"
)

type deviceIdentity struct {
	UID int
	IP  string
}

type deviceSession struct {
	uid  int
	refs int
}

// TrackDeviceIP tracks one accepted connection (or half of an Xray link).
// The returned release is safe to call more than once. Counts are per UID + IP,
// not per connection, server replica, source port or textual IPv6 spelling.
func (l *Limiter) TrackDeviceIP(user, ip string) func() {
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return func() {}
	}
	ip = address.Unmap().WithZone("").String()
	l.aliveMu.Lock()
	info, ok := l.UserLimitInfo.Load(user)
	if !ok {
		l.aliveMu.Unlock()
		return func() {}
	}
	if l.activeDevices == nil {
		l.activeDevices = make(map[string]map[string]*deviceSession)
	}
	if l.activeDevices[user] == nil {
		l.activeDevices[user] = make(map[string]*deviceSession)
	}
	entry := l.activeDevices[user][ip]
	if entry == nil {
		entry = &deviceSession{uid: info.(*UserLimitInfo).UID}
		l.activeDevices[user][ip] = entry
	}
	entry.refs++
	l.aliveMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.aliveMu.Lock()
			defer l.aliveMu.Unlock()
			if l.activeDevices[user][ip] != entry {
				return // A revoked user's old connection cannot remove a new one.
			}
			entry.refs--
			if entry.refs == 0 {
				delete(l.activeDevices[user], ip)
				if len(l.activeDevices[user]) == 0 {
					delete(l.activeDevices, user)
				}
			}
		})
	}
}
