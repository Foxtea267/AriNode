package sing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/constant"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/apis/trafficpattern"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlcommon"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	mcommon "github.com/enfein/mieru/v3/pkg/common"
	"github.com/enfein/mieru/v3/pkg/protocol"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	"google.golang.org/protobuf/proto"
)

// Mieru uses its own encrypted transport; routing and accounting stay in sing-box.
type mieruInboundOptions struct {
	option.ListenOptions
	Transport      string `json:"transport,omitempty"`
	TrafficPattern string `json:"traffic_pattern,omitempty"`
}

type mieruInbound struct {
	inbound.Adapter
	ctx                         context.Context
	router                      adapter.Router
	logger                      log.ContextLogger
	options                     mieruInboundOptions
	mux                         *protocol.Mux
	mu                          sync.Mutex
	users                       map[string]*appctlpb.User
	connections                 map[net.Conn]string
	listener                    net.Listener
	packet                      net.PacketConn
	started, muxStarted, closed bool
	// A fresh unreachable credential lets an empty node listen without granting access.
	placeholder *appctlpb.User
}

func newMieruInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options mieruInboundOptions) (adapter.Inbound, error) {
	transport := strings.ToLower(options.Transport)
	if transport == "" {
		transport = "tcp"
	}
	if transport != "tcp" && transport != "udp" {
		return nil, fmt.Errorf("invalid mieru transport %q", options.Transport)
	}
	options.Transport = transport
	pattern, err := trafficpattern.Decode(options.TrafficPattern)
	if err != nil {
		return nil, fmt.Errorf("decode mieru traffic_pattern: %w", err)
	}
	config, err := trafficpattern.NewConfig(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid mieru traffic_pattern: %w", err)
	}
	var secret [64]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	placeholder := appctlcommon.HashUserPassword(&appctlpb.User{Name: proto.String(hex.EncodeToString(secret[:32])), Password: proto.String(hex.EncodeToString(secret[32:]))}, false)
	return &mieruInbound{
		Adapter: inbound.NewAdapter("mieru", tag), ctx: ctx, router: router, logger: logger, options: options,
		mux: protocol.NewMux(false).SetTrafficPattern(config), placeholder: placeholder,
		users: make(map[string]*appctlpb.User), connections: make(map[net.Conn]string),
	}, nil
}

type mieruListener struct{ net.Listener }

func (l mieruListener) Listen(context.Context, string, string) (net.Listener, error) {
	return l.Listener, nil
}

type mieruPacketListener struct{ net.PacketConn }

func (l mieruPacketListener) ListenPacket(context.Context, string, string) (net.PacketConn, error) {
	return l.PacketConn, nil
}

func (h *mieruInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	if h.started {
		return nil
	}
	host := "0.0.0.0"
	if h.options.Listen != nil {
		host = netip.Addr(*h.options.Listen).String()
	}
	address := net.JoinHostPort(host, strconv.Itoa(int(h.options.ListenPort)))
	var local net.Addr
	transport := mcommon.StreamTransport
	var err error
	// Bind synchronously: Mux.Start otherwise hides address-in-use failures.
	if h.options.Transport == "tcp" {
		h.listener, err = net.Listen("tcp", address)
		if err != nil {
			return err
		}
		local = h.listener.Addr()
		h.mux.SetStreamListenerFactory(mieruListener{h.listener})
	} else {
		h.packet, err = net.ListenPacket("udp", address)
		if err != nil {
			return err
		}
		local, transport = h.packet.LocalAddr(), mcommon.PacketTransport
		h.mux.SetPacketListenerFactory(mieruPacketListener{h.packet})
	}
	h.mux.SetEndpoints([]protocol.UnderlayProperties{protocol.NewUnderlayProperties(mcommon.DefaultMTU, transport, local, nil)})
	h.syncUsers()
	h.started = true
	return h.startMux()
}

// Wait for the first user snapshot before starting UDP authentication. Mieru
// creates its UDP underlay asynchronously, so starting it with placeholder
// credentials and immediately replacing them can leave a stale user snapshot.
func (h *mieruInbound) startMux() error {
	if !h.started || h.muxStarted || len(h.users) == 0 {
		return nil
	}
	if err := h.mux.Start(); err != nil {
		return err
	}
	h.muxStarted = true
	go h.acceptLoop()
	return nil
}

// Called under mu. Mieru retains this map, so every update gets a new snapshot.
func (h *mieruInbound) syncUsers() {
	users := make(map[string]*appctlpb.User, len(h.users)+1)
	for name, user := range h.users {
		users[name] = user
	}
	if len(users) == 0 {
		users[h.placeholder.GetName()] = h.placeholder
	}
	h.mux.SetServerUsers(users)
}

func (h *mieruInbound) AddUsers(users []panel.UserInfo) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	for _, u := range users {
		if u.Uuid == "" {
			return fmt.Errorf("empty mieru user UUID")
		}
	}
	for _, u := range users {
		h.users[u.Uuid] = appctlcommon.HashUserPassword(&appctlpb.User{Name: proto.String(u.Uuid), Password: proto.String(u.Uuid)}, false)
	}
	h.syncUsers()
	return h.startMux()
}

func (h *mieruInbound) DelUsers(names []string) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return net.ErrClosed
	}
	for _, name := range names {
		delete(h.users, name)
	}
	h.syncUsers()
	var revoked []net.Conn
	for conn, name := range h.connections {
		if _, ok := h.users[name]; name != "" && !ok {
			revoked = append(revoked, conn)
		}
	}
	h.mu.Unlock()
	for _, conn := range revoked {
		conn.Close()
	}
	return nil
}

func (h *mieruInbound) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	connections := make([]net.Conn, 0, len(h.connections))
	for conn := range h.connections {
		connections = append(connections, conn)
	}
	h.mu.Unlock()
	if h.listener != nil {
		h.listener.Close()
	}
	if h.packet != nil {
		h.packet.Close()
	}
	for _, conn := range connections {
		conn.Close()
	}
	return h.mux.Close()
}

func (h *mieruInbound) acceptLoop() {
	for {
		conn, err := h.mux.Accept()
		if err != nil {
			return
		}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			conn.Close()
			return
		}
		h.connections[conn] = ""
		h.mu.Unlock()
		go h.handle(conn)
	}
}

func (h *mieruInbound) handle(conn net.Conn) {
	cleanup := func() { conn.Close(); h.mu.Lock(); delete(h.connections, conn); h.mu.Unlock() }
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var request model.Request
	if err := request.ReadFromSocks5(conn); err != nil {
		cleanup()
		return
	}
	conn.SetReadDeadline(time.Time{})
	userContext, ok := conn.(apicommon.UserContext)
	if !ok {
		cleanup()
		return
	}
	name := userContext.UserName()
	h.mu.Lock()
	_, authorized := h.users[name]
	if !authorized || h.closed {
		h.mu.Unlock()
		cleanup()
		return
	}
	h.connections[conn] = name
	h.mu.Unlock()
	metadata := adapter.InboundContext{
		Inbound: h.Tag(), InboundType: h.Type(), User: name,
		Source: M.SocksaddrFromNet(conn.RemoteAddr()), Destination: M.ParseSocksaddr(request.DstAddr.String()),
	}
	onClose := func(error) { cleanup() }
	response := &mieruHandshakeConn{Conn: conn}
	switch request.Command {
	case constant.Socks5ConnectCmd:
		h.router.RouteConnectionEx(h.ctx, response, metadata, onClose)
	case constant.Socks5UDPAssociateCmd:
		if err := response.HandshakeSuccess(); err != nil {
			cleanup()
			return
		}
		// The tunnel wraps each datagram with the destination's SOCKS header.
		packet := bufio.NewPacketConn(apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn)))
		h.router.RoutePacketConnectionEx(h.ctx, packet, metadata, onClose)
	default:
		response.reply(constant.Socks5ReplyCommandNotSupported)
		cleanup()
	}
}

type mieruHandshakeConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *mieruHandshakeConn) reply(code uint8) error {
	c.once.Do(func() {
		c.err = (&model.Response{Reply: code, BindAddr: model.AddrSpec{IP: net.IPv4zero}}).WriteToSocks5(c.Conn)
	})
	return c.err
}
func (c *mieruHandshakeConn) HandshakeSuccess() error { return c.reply(constant.Socks5ReplySuccess) }
func (c *mieruHandshakeConn) HandshakeFailure(error) error {
	return c.reply(constant.Socks5ReplyServerFailure)
}
