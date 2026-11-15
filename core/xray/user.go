package xray

import (
	"context"
	"fmt"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/format"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy"
)

func (c *Xray) GetUserManager(tag string) (proxy.UserManager, error) {
	handler, err := c.ihm.GetHandler(context.Background(), tag)
	if err != nil {
		return nil, fmt.Errorf("no such inbound tag: %s", err)
	}
	inboundInstance, ok := handler.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.GetInbound", tag)
	}
	userManager, ok := inboundInstance.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.UserManager", tag)
	}
	return userManager, nil
}

func (c *Xray) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	c.access.Lock()
	defer c.access.Unlock()
	if c.dispatcher == nil {
		return fmt.Errorf("Xray is not running")
	}
	userManager, err := c.GetUserManager(tag)
	if err != nil {
		return fmt.Errorf("get user manager error: %s", err)
	}
	var user string
	c.users.mapLock.Lock()
	defer c.users.mapLock.Unlock()
	for i := range users {
		user = format.UserTag(tag, users[i].Uuid)
		if userManager.GetUser(context.Background(), user) != nil {
			err = userManager.RemoveUser(context.Background(), user)
		}
		if err != nil {
			return err
		}
		c.dispatcher.RevokeUser(user)
		uid := c.users.uidMap[user]
		delete(c.users.uidMap, user)
		if v, ok := c.dispatcher.Counter.Load(tag); ok {
			tc := v.(*counter.TrafficCounter)
			if storage, ok := tc.Counters.Load(user); ok && uid != 0 {
				c.retireTraffic(tag, uid, storage.(*counter.TrafficStorage))
			}
			tc.Delete(user)
		}
	}
	return nil
}

func (x *Xray) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	x.access.Lock()
	defer x.access.Unlock()
	if x.dispatcher == nil {
		return nil, fmt.Errorf("Xray is not running")
	}
	trafficSlice := make([]panel.UserTraffic, 0)
	byUID := make(map[int]int)
	merge := func(row panel.UserTraffic) {
		if i, ok := byUID[row.UID]; ok {
			trafficSlice[i].Upload += row.Upload
			trafficSlice[i].Download += row.Download
		} else {
			byUID[row.UID] = len(trafficSlice)
			trafficSlice = append(trafficSlice, row)
		}
	}
	x.users.mapLock.RLock()
	defer x.users.mapLock.RUnlock()
	if pending := x.users.retired[tag]; pending != nil {
		for _, row := range pending.Snapshot(reset) {
			merge(panel.UserTraffic(row))
		}
	}
	if v, ok := x.dispatcher.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			email := key.(string)
			traffic := value.(*counter.TrafficStorage)
			up, down := traffic.Snapshot(false)
			if up+down > x.nodeReportMinTrafficBytes[tag] {
				if reset {
					up, down = traffic.Snapshot(true)
					if up == 0 && down == 0 {
						return true
					}
				}
				if x.users.uidMap[email] == 0 {
					c.Delete(email)
					return true
				}
				merge(panel.UserTraffic{
					UID:      x.users.uidMap[email],
					Upload:   up,
					Download: down,
				})
			}
			return true
		})
	}
	if len(trafficSlice) == 0 {
		return nil, nil
	}
	return trafficSlice, nil
}

func (c *Xray) AddUsers(p *vCore.AddUsersParams) (added int, err error) {
	c.access.Lock()
	defer c.access.Unlock()
	if c.dispatcher == nil {
		return 0, fmt.Errorf("Xray is not running")
	}
	c.users.mapLock.Lock()
	defer c.users.mapLock.Unlock()
	var users []*protocol.User
	switch p.NodeInfo.Type {
	case "vmess":
		users = buildVmessUsers(p.Tag, p.Users)
	case "vless":
		users = buildVlessUsers(p.Tag, p.Users, p.VAllss.Flow)
	case "trojan":
		users = buildTrojanUsers(p.Tag, p.Users)
	case "shadowsocks":
		users, err = buildSSUsers(p.Tag,
			p.Users,
			p.Shadowsocks.Cipher,
			p.Shadowsocks.ServerKey)
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unsupported node type: %s", p.NodeInfo.Type)
	}
	man, err := c.GetUserManager(p.Tag)
	if err != nil {
		return 0, fmt.Errorf("get user manager error: %s", err)
	}
	memoryUsers := make([]*protocol.MemoryUser, len(users))
	for i, u := range users {
		memoryUsers[i], err = u.ToMemoryUser()
		if err != nil {
			return 0, err
		}
	}
	for i, user := range memoryUsers {
		if c.users.uidMap[user.Email] == p.Users[i].Id && man.GetUser(context.Background(), user.Email) != nil {
			added++
			continue
		}
		if err := c.dispatcher.RegisterUser(user); err != nil {
			return added, err
		}
		err = man.AddUser(context.Background(), user)
		if err != nil {
			c.dispatcher.RevokeUser(user.Email)
			return added, fmt.Errorf("Xray rejected user registration")
		}
		c.users.uidMap[user.Email] = p.Users[i].Id
		added++
	}
	return added, nil
}

func (c *Xray) retireTraffic(tag string, uid int, storage *counter.TrafficStorage) {
	if c.users.retired == nil {
		c.users.retired = make(map[string]*counter.RetiredUserTraffic)
	}
	pending := c.users.retired[tag]
	if pending == nil {
		pending = &counter.RetiredUserTraffic{}
		c.users.retired[tag] = pending
	}
	storage.RetireToFunc(func(up, down int64) { pending.Add(uid, up, down) })
}
