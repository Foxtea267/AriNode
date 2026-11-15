package xray

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/auth"
	B "github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/app/proxyman/inbound"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	X "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/singbridge"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/shadowsocks_2022"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type ss2022HandlerConfig struct {
	tag      string
	receiver *proxyman.ReceiverConfig
	proxy    *shadowsocks_2022.MultiUserServerConfig
}

func init() {
	common.Must(common.RegisterConfig((*ss2022HandlerConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		c := config.(*ss2022HandlerConfig)
		return inbound.NewAlwaysOnInboundHandler(ctx, c.tag, c.receiver, &ss2022ProxyConfig{settings: c.proxy})
	}))
	common.Must(common.RegisterConfig((*ss2022ProxyConfig)(nil), func(_ context.Context, config interface{}) (interface{}, error) {
		return newSS2022Proxy(config.(*ss2022ProxyConfig).settings)
	}))
}

type ss2022ProxyConfig struct {
	settings *shadowsocks_2022.MultiUserServerConfig
}

func ss2022HandlerSettings(config *core.InboundHandlerConfig) (any, error) {
	if config.ProxySettings.GetType() != serial.GetMessageType(&shadowsocks_2022.MultiUserServerConfig{}) {
		return config, nil
	}
	raw, err := config.ProxySettings.GetInstance()
	if err != nil {
		return nil, err
	}
	receiver, err := config.ReceiverSettings.GetInstance()
	if err != nil {
		return nil, err
	}
	settings, ok := receiver.(*proxyman.ReceiverConfig)
	if !ok {
		return nil, fmt.Errorf("invalid Shadowsocks receiver settings")
	}
	return &ss2022HandlerConfig{tag: config.Tag, receiver: settings, proxy: raw.(*shadowsocks_2022.MultiUserServerConfig)}, nil
}

type ss2022Snapshot struct {
	service *shadowaead_2022.MultiService[*protocol.MemoryUser]
	users   map[string]*protocol.MemoryUser
}

// Authentication carries the actual immutable MemoryUser into dispatch. A
// removed generation cannot pass the dispatcher's pointer-based user gate.
type ss2022Proxy struct {
	update      sync.Mutex
	packetLocks [256]sync.Mutex
	packetKey   cipher.Block
	network     []X.Network
	base        *shadowaead_2022.MultiService[*protocol.MemoryUser]
	current     atomic.Pointer[ss2022Snapshot]
}

var _ proxy.Inbound = (*ss2022Proxy)(nil)
var _ proxy.UserManager = (*ss2022Proxy)(nil)

func newSS2022Proxy(config *shadowsocks_2022.MultiUserServerConfig) (*ss2022Proxy, error) {
	p := &ss2022Proxy{network: config.Network}
	if len(p.network) == 0 {
		p.network = []X.Network{X.Network_TCP, X.Network_UDP}
	}
	base, err := shadowaead_2022.NewMultiServiceWithPassword[*protocol.MemoryUser](config.Method, config.Key, 500, p, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid Shadowsocks 2022 server settings")
	}
	// Use the normalized server PSK used by the SDK to identify the session
	// stripe. The eight-byte session ID is stable across packet numbers.
	key, err := base64.StdEncoding.DecodeString(base.Password())
	if err != nil {
		return nil, fmt.Errorf("invalid Shadowsocks 2022 packet key")
	}
	p.packetKey, err = aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("invalid Shadowsocks 2022 packet key")
	}
	p.base = base
	users := make(map[string]*protocol.MemoryUser, len(config.Users))
	for i, entry := range config.Users {
		user, err := entry.ToMemoryUser()
		if err != nil {
			return nil, fmt.Errorf("invalid Shadowsocks 2022 user")
		}
		if user.Email == "" {
			user.Email = fmt.Sprintf("initial-%d", i)
		}
		key := strings.ToLower(user.Email)
		if users[key] != nil {
			return nil, fmt.Errorf("duplicate Shadowsocks 2022 user")
		}
		users[key] = user
	}
	return p, p.publish(users)
}

func (p *ss2022Proxy) publish(users map[string]*protocol.MemoryUser) error {
	snapshot := *p.base // credential maps are replaced; preserve SDK replay/NAT state
	records, passwords := make([]*protocol.MemoryUser, 0, len(users)), make([]string, 0, len(users))
	seen := make(map[string]bool, len(users))
	for _, user := range users {
		account, ok := user.Account.(*shadowsocks_2022.MemoryAccount)
		if !ok || seen[account.Key] {
			return fmt.Errorf("invalid or duplicate Shadowsocks 2022 key")
		}
		seen[account.Key] = true
		records, passwords = append(records, user), append(passwords, account.Key)
	}
	if err := snapshot.UpdateUsersWithPasswords(records, passwords); err != nil {
		return fmt.Errorf("invalid Shadowsocks 2022 user key")
	}
	p.current.Store(&ss2022Snapshot{service: &snapshot, users: users})
	return nil
}

func (p *ss2022Proxy) AddUser(_ context.Context, user *protocol.MemoryUser) error {
	p.update.Lock()
	defer p.update.Unlock()
	key := strings.ToLower(user.Email)
	users := p.current.Load().users
	if key == "" || users[key] != nil {
		return fmt.Errorf("empty or duplicate Shadowsocks 2022 user")
	}
	next := make(map[string]*protocol.MemoryUser, len(users)+1)
	for k, u := range users {
		next[k] = u
	}
	next[key] = user
	return p.publish(next)
}

func (p *ss2022Proxy) RemoveUser(_ context.Context, email string) error {
	p.update.Lock()
	defer p.update.Unlock()
	key := strings.ToLower(email)
	users := p.current.Load().users
	if users[key] == nil {
		return fmt.Errorf("Shadowsocks 2022 user not found")
	}
	next := make(map[string]*protocol.MemoryUser, len(users)-1)
	for k, u := range users {
		if k != key {
			next[k] = u
		}
	}
	return p.publish(next)
}

func (p *ss2022Proxy) GetUser(_ context.Context, email string) *protocol.MemoryUser {
	return p.current.Load().users[strings.ToLower(email)]
}

func (p *ss2022Proxy) GetUsers(context.Context) []*protocol.MemoryUser {
	users := p.current.Load().users
	result := make([]*protocol.MemoryUser, 0, len(users))
	for _, user := range users {
		result = append(result, user)
	}
	return result
}

func (p *ss2022Proxy) GetUsersCount(context.Context) int64 { return int64(len(p.current.Load().users)) }
func (p *ss2022Proxy) Network() []X.Network                { return p.network }

func (p *ss2022Proxy) Process(ctx context.Context, network X.Network, conn stat.Connection, dispatcher routing.Dispatcher) error {
	in := session.InboundFromContext(ctx)
	in.Name, in.CanSpliceCopy = "shadowsocks-2022-multi", 3
	meta := M.Metadata{}
	if in.Source.IsValid() {
		meta.Source = M.ParseSocksaddr(in.Source.NetAddr())
	}
	ctx = session.ContextWithDispatcher(ctx, dispatcher)
	if network == X.Network_TCP {
		return singbridge.ReturnError(p.current.Load().service.NewConnection(ctx, conn, meta))
	}
	reader := buf.NewReader(conn)
	pc := ss2022PacketSocket{conn}
	for {
		mb, err := reader.ReadMultiBuffer()
		if err != nil {
			buf.ReleaseMulti(mb)
			return singbridge.ReturnError(err)
		}
		for _, chunk := range mb {
			if chunk.Len() >= 2*aes.BlockSize {
				p.handlePacket(ctx, pc, chunk.Bytes(), meta)
			}
			chunk.Release()
		}
	}
}

func (p *ss2022Proxy) handlePacket(ctx context.Context, pc ss2022PacketSocket, raw []byte, meta M.Metadata) {
	var header [aes.BlockSize]byte
	p.packetKey.Decrypt(header[:], raw[:aes.BlockSize])
	lock := &p.packetLocks[header[7]]
	// The SDK replay window requires serialized input per session. Do not
	// let a blocked SDK NAT queue hold another UDP reader waiting on a lock.
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	packet := B.As(raw).ToOwned()
	if err := p.current.Load().service.NewPacket(ctx, pc, packet, meta); err != nil {
		packet.Release()
	}
}

func (p *ss2022Proxy) NewConnection(ctx context.Context, conn net.Conn, meta M.Metadata) error {
	ctx, err := p.authenticatedContext(ctx)
	if err != nil {
		return err
	}
	link, err := session.DispatcherFromContext(ctx).Dispatch(ctx, singbridge.ToDestination(meta.Destination, X.Network_TCP))
	if err != nil {
		return err
	}
	return singbridge.CopyConn(ctx, conn, link, conn)
}

func (p *ss2022Proxy) NewPacketConnection(ctx context.Context, conn N.PacketConn, meta M.Metadata) error {
	ctx, err := p.authenticatedContext(ctx)
	if err != nil {
		return err
	}
	destination := singbridge.ToDestination(meta.Destination, X.Network_UDP)
	link, err := session.DispatcherFromContext(ctx).Dispatch(ctx, destination)
	if err != nil {
		return err
	}
	return bufio.CopyPacketConn(ctx, conn, &ss2022PacketLink{singbridge.PacketConnWrapper{
		Reader: link.Reader, Writer: link.Writer, Dest: destination,
	}})
}

func (p *ss2022Proxy) authenticatedContext(ctx context.Context) (context.Context, error) {
	user, _ := auth.UserFromContext[*protocol.MemoryUser](ctx)
	if user == nil || p.GetUser(ctx, user.Email) != user {
		return nil, fmt.Errorf("Shadowsocks 2022 authentication expired")
	}
	// A UDP authentication callback can outlive the packet that created it.
	// Do not mutate the worker's inbound context shared by its other sessions.
	in := *session.InboundFromContext(ctx)
	in.User = user
	return session.ContextWithInbound(ctx, &in), nil
}

func (*ss2022Proxy) NewError(context.Context, error) {}

type ss2022PacketSocket struct{ net.Conn }

type ss2022PacketLink struct{ singbridge.PacketConnWrapper }

func (c *ss2022PacketLink) WritePacket(buffer *B.Buffer, destination M.Socksaddr) error {
	defer buffer.Release()
	return c.PacketConnWrapper.WritePacket(buffer, destination)
}

func (c ss2022PacketSocket) ReadPacket(buffer *B.Buffer) (M.Socksaddr, error) {
	_, err := buffer.ReadFrom(c)
	return M.Socksaddr{}, err
}

func (c ss2022PacketSocket) WritePacket(buffer *B.Buffer, _ M.Socksaddr) error {
	defer buffer.Release()
	_, err := buffer.WriteTo(c)
	return err
}
