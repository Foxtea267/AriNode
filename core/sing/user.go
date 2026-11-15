package sing

import (
	"errors"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/Foxtea267/AriNode/core"
	"github.com/sagernet/sing-box/option"
)

func (b *Sing) AddUsers(p *core.AddUsersParams) (added int, err error) {
	in, found := b.box.Inbound().Get(p.Tag)
	if !found {
		return 0, errors.New("the inbound not found")
	}
	if upgraded, ok := in.(*httpUpgradeInbound); ok {
		in = upgraded.Inbound
	}
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	switch p.NodeInfo.Type {
	case "mieru":
		err = in.(*mieruInbound).AddUsers(p.Users)
	case "vless":
		us := make([]option.VLESSUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.VLESSUser{
				Name: p.Users[i].Uuid,
				Flow: p.VAllss.Flow,
				UUID: p.Users[i].Uuid,
			}
		}
		err = in.(interface {
			AddUsers([]option.VLESSUser) error
		}).AddUsers(us)
	case "vmess":
		us := make([]option.VMessUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.VMessUser{
				Name: p.Users[i].Uuid,
				UUID: p.Users[i].Uuid,
			}
		}
		err = in.(interface {
			AddUsers([]option.VMessUser) error
		}).AddUsers(us)
	case "shadowsocks":
		us := make([]option.ShadowsocksUser, len(p.Users))
		passwords := make(map[string]bool, len(p.Users))
		for i := range p.Users {
			password, err := format.ShadowsocksUserPassword(p.Shadowsocks.Cipher, p.Users[i].Uuid)
			if err != nil {
				return 0, err
			}
			if passwords[password] {
				return 0, errors.New("duplicate effective Shadowsocks user key")
			}
			passwords[password] = true
			us[i] = option.ShadowsocksUser{
				Name:     p.Users[i].Uuid,
				Password: password,
			}
		}
		updater, ok := in.(interface {
			AddUsers([]option.ShadowsocksUser) error
		})
		if !ok {
			return 0, errors.New("Shadowsocks inbound does not support panel users")
		}
		err = updater.AddUsers(us)
	case "trojan":
		us := make([]option.TrojanUser, len(p.Users))
		for i := range p.Users {
			us[i] = option.TrojanUser{
				Name:     p.Users[i].Uuid,
				Password: p.Users[i].Uuid,
			}
		}
		err = in.(*stableTrojanInbound).AddUsers(us)
	case "tuic":
		err = addQUICUsers(in, p.Users)
	case "hysteria":
		err = addQUICUsers(in, p.Users)
	case "hysteria2":
		err = addQUICUsers(in, p.Users)
	case "anytls":
		err = in.(*anyTLSInbound).AddUsers(p.Users)
	default:
		return 0, errors.New("unsupported user protocol")
	}
	if err != nil {
		return 0, err
	}
	if b.users.uidMap[p.Tag] == nil {
		b.users.uidMap[p.Tag] = make(map[string]int)
	}
	for _, user := range p.Users {
		b.users.uidMap[p.Tag][user.Uuid] = user.Id
	}
	return len(p.Users), err
}

func (b *Sing) GetUserTraffic(tag, uuid string, reset bool) (up int64, down int64) {
	if v, ok := b.hookServer.counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		return c.GetTraffic(uuid, reset)
	}
	return 0, 0
}

func (b *Sing) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	trafficSlice := make([]panel.UserTraffic, 0)
	byUID := make(map[int]int)
	appendTraffic := func(traffic panel.UserTraffic) {
		if index, ok := byUID[traffic.UID]; ok {
			trafficSlice[index].Upload += traffic.Upload
			trafficSlice[index].Download += traffic.Download
		} else {
			byUID[traffic.UID] = len(trafficSlice)
			trafficSlice = append(trafficSlice, traffic)
		}
	}
	hook := b.hookServer
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	if spool := b.users.retiredTraffic[tag]; spool != nil {
		for _, traffic := range spool.Snapshot(reset) {
			appendTraffic(panel.UserTraffic(traffic))
		}
	}
	if v, ok := hook.counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			uuid := key.(string)
			traffic := value.(*counter.TrafficStorage)
			up, down := traffic.Snapshot(false)
			if up+down > b.nodeReportMinTrafficBytes[tag] {
				if reset {
					up, down = traffic.Snapshot(true)
					if up+down == 0 {
						return true
					}
				}
				if b.users.uidMap[tag][uuid] == 0 {
					c.Delete(uuid)
					return true
				}
				appendTraffic(panel.UserTraffic{
					UID:      b.users.uidMap[tag][uuid],
					Upload:   up,
					Download: down,
				})
			}
			return true
		})
		if len(trafficSlice) == 0 {
			return nil, nil
		}
		return trafficSlice, nil
	}
	return trafficSlice, nil
}

type UserDeleter interface {
	DelUsers(uuid []string) error
}

func (b *Sing) DelUsers(users []panel.UserInfo, tag string, info *panel.NodeInfo) error {
	var del UserDeleter
	if i, found := b.box.Inbound().Get(tag); found {
		if upgraded, ok := i.(*httpUpgradeInbound); ok {
			i = upgraded.Inbound
		}
		switch info.Type {
		case "mieru":
			del = i.(*mieruInbound)
		case "vmess":
			del = i.(UserDeleter)
		case "vless":
			del = i.(UserDeleter)
		case "shadowsocks":
			var ok bool
			del, ok = i.(UserDeleter)
			if !ok {
				return errors.New("Shadowsocks inbound does not support panel users")
			}
		case "trojan":
			del = i.(*stableTrojanInbound)
		case "tuic":
			del = i.(UserDeleter)
		case "hysteria":
			del = i.(UserDeleter)
		case "hysteria2":
			del = i.(UserDeleter)
		case "anytls":
			del = i.(*anyTLSInbound)
		default:
			return errors.New("unsupported user protocol")
		}
	} else {
		return errors.New("the inbound not found")
	}
	uuids := make([]string, len(users))
	b.users.mapLock.Lock()
	defer b.users.mapLock.Unlock()
	for i := range users {
		uuids[i] = users[i].Uuid
	}
	if err := del.DelUsers(uuids); err != nil {
		return err
	}
	_, stableSS := del.(*stableShadowsocksInbound)
	for i := range users {
		if v, ok := b.hookServer.counter.Load(tag); ok {
			c := v.(*counter.TrafficCounter)
			if stored, ok := c.Counters.Load(users[i].Uuid); ok && (info.Type == "vless" || info.Type == "trojan" || info.Type == "vmess" || stableSS) {
				uid := b.users.uidMap[tag][users[i].Uuid]
				if uid != 0 {
					if b.users.retiredTraffic == nil {
						b.users.retiredTraffic = make(map[string]*counter.RetiredUserTraffic)
					}
					if b.users.retiredTraffic[tag] == nil {
						b.users.retiredTraffic[tag] = &counter.RetiredUserTraffic{}
					}
					destination := b.users.retiredTraffic[tag]
					stored.(*counter.TrafficStorage).RetireToFunc(func(up, down int64) { destination.Add(uid, up, down) })
				}
			}
			c.Delete(users[i].Uuid)
		}
		delete(b.users.uidMap[tag], users[i].Uuid)
	}
	return nil
}
