package core

import (
	"errors"
	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
)

// Optional interface: a routing-only edit keeps the inbound and users intact.
type NodePolicyUpdater interface {
	UpdateNodePolicy(tag string, info *panel.NodeInfo, config *conf.Options) error
}

var ErrPolicyReloadUnsupported = errors.New("core requires a per-node reload")

type AddUsersParams struct {
	Tag   string
	Users []panel.UserInfo
	*panel.NodeInfo
}

type Core interface {
	Start() error
	Close() error
	AddNode(tag string, info *panel.NodeInfo, config *conf.Options) error
	DelNode(tag string) error
	AddUsers(p *AddUsersParams) (added int, err error)
	GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error)
	DelUsers(users []panel.UserInfo, tag string, info *panel.NodeInfo) error
	Protocols() []string
	Type() string
}
