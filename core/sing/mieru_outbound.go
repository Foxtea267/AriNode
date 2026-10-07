package sing

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/enfein/mieru/v3/apis/client"
	apicommon "github.com/enfein/mieru/v3/apis/common"
	"github.com/enfein/mieru/v3/apis/trafficpattern"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/dialer"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"google.golang.org/protobuf/proto"
)

type mieruOutboundOptions struct {
	option.ServerOptions
	option.DialerOptions
	Username       string `json:"username"`
	Password       string `json:"password"`
	Transport      string `json:"transport,omitempty"`
	TrafficPattern string `json:"traffic_pattern,omitempty"`
}
type mieruOutbound struct {
	outbound.Adapter
	client client.Client
}
type mieruTransportDialer struct{ N.Dialer }

func (d mieruTransportDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.Dialer.DialContext(ctx, network, M.ParseSocksaddr(address))
}
func (d mieruTransportDialer) ListenPacket(ctx context.Context, network, local, remote string) (net.PacketConn, error) {
	return d.Dialer.ListenPacket(ctx, M.ParseSocksaddr(remote))
}

type mieruDestination struct{ network, address string }

func (a mieruDestination) Network() string { return a.network }
func (a mieruDestination) String() string  { return a.address }

func newMieruOutbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, o mieruOutboundOptions) (adapter.Outbound, error) {
	if o.Server == "" || o.ServerPort == 0 || o.Username == "" || o.Password == "" {
		return nil, fmt.Errorf("Mieru requires server, server_port, username and password")
	}
	transport := appctlpb.TransportProtocol_TCP
	switch strings.ToLower(o.Transport) {
	case "", "tcp":
	case "udp":
		transport = appctlpb.TransportProtocol_UDP
	default:
		return nil, fmt.Errorf("invalid Mieru transport")
	}
	pattern, err := trafficpattern.Decode(o.TrafficPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid Mieru traffic pattern")
	}
	endpoint := &appctlpb.ServerEndpoint{PortBindings: []*appctlpb.PortBinding{{Port: proto.Int32(int32(o.ServerPort)), Protocol: &transport}}}
	if net.ParseIP(o.Server) != nil {
		endpoint.IpAddress = &o.Server
	} else {
		endpoint.DomainName = &o.Server
	}
	transportDialer, err := dialer.New(ctx, o.DialerOptions, net.ParseIP(o.Server) == nil)
	if err != nil {
		return nil, fmt.Errorf("invalid Mieru dialer")
	}
	wrapped := mieruTransportDialer{transportDialer}
	c := client.NewClient()
	if err := c.Store(&client.ClientConfig{Profile: &appctlpb.ClientProfile{ProfileName: proto.String(tag), User: &appctlpb.User{Name: &o.Username, Password: &o.Password}, HandshakeMode: appctlpb.HandshakeMode_HANDSHAKE_STANDARD.Enum(), TrafficPattern: pattern, Servers: []*appctlpb.ServerEndpoint{endpoint}}, Dialer: wrapped, PacketDialer: wrapped, Resolver: net.DefaultResolver, DNSConfig: &apicommon.ClientDNSConfig{BypassDialerDNS: true}}); err != nil {
		return nil, fmt.Errorf("invalid Mieru profile")
	}
	var dependencies []string
	if o.Detour != "" {
		dependencies = []string{o.Detour}
	}
	return &mieruOutbound{Adapter: outbound.NewAdapter("mieru", tag, []string{"tcp", "udp"}, dependencies), client: c}, nil
}
func (o *mieruOutbound) Start(stage adapter.StartStage) error {
	if stage == adapter.StartStateStart {
		return o.client.Start()
	}
	return nil
}
func (o *mieruOutbound) Close() error { return o.client.Stop() }
func (o *mieruOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	return o.client.DialContext(ctx, mieruDestination{network, destination.String()})
}
func (o *mieruOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	conn, err := o.client.DialContext(ctx, &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, err
	}
	return apicommon.NewUDPAssociateWrapper(apicommon.NewPacketOverStreamTunnel(conn)), nil
}
