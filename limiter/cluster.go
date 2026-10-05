package limiter

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
			l.aliveIPs[uid][ip] = true
		}
	}
}
func (l *Limiter) AllowedUserIDs() map[int]bool {
	allowed := make(map[int]bool)
	l.UserLimitInfo.Range(func(_, value any) bool { allowed[value.(*UserLimitInfo).UID] = true; return true })
	return allowed
}
