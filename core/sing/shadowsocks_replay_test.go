package sing

import (
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

func TestSS2022ReplayRejectedAcrossUserRefresh(t *testing.T) {
	const method = "2022-blake3-aes-128-gcm"
	key := func(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 16)) }
	router := &stableVLESSTestRouter{}
	in, err := newStableShadowsocksInbound(context.Background(), router, log.NewNOPFactory().NewLogger("ss2022-replay"), "ss2022-replay",
		option.ShadowsocksInboundOptions{Method: method, Password: key(1),
			Users: []option.ShadowsocksUser{{Name: "healthy", Password: key(2)}}})
	if err != nil {
		t.Fatal(err)
	}
	h := in.(*stableShadowsocksInbound)
	defer h.Close()
	client, err := shadowaead_2022.NewWithPassword(method, key(1)+":"+key(2), nil)
	if err != nil {
		t.Fatal(err)
	}
	build := func() []byte {
		t.Helper()
		wire := &recordVMessConn{}
		if _, err := client.DialEarlyConn(wire, M.ParseSocksaddr("127.0.0.1:80")).Write([]byte("payload")); err != nil {
			t.Fatal(err)
		}
		return wire.buffer.Bytes()
	}
	request := func(frame []byte, want int32) {
		t.Helper()
		raw, peer := net.Pipe()
		defer raw.Close()
		defer peer.Close()
		_ = peer.SetWriteDeadline(time.Now().Add(time.Second))
		done := make(chan struct{})
		go func() { defer close(done); _, _ = peer.Write(frame) }()
		h.NewConnectionEx(context.Background(), raw, adapter.InboundContext{Source: M.ParseSocksaddr("127.0.0.1:12345")}, nil)
		_ = raw.Close()
		<-done
		if got := router.routes.Load(); got != want {
			t.Fatalf("routed %d requests, want %d", got, want)
		}
	}
	// The pinned SDK's published minimum is two bytes shorter than the
	// complete identity header. These packets must never reach its parser.
	for _, n := range []int{30, 31} {
		h.NewPacketEx(buf.As(make([]byte, n)), M.ParseSocksaddr("127.0.0.1:12345"))
	}
	frame := build()
	request(frame, 1)
	if err := h.AddUsers([]option.ShadowsocksUser{{Name: "new", Password: key(3)}}); err != nil {
		t.Fatal(err)
	}
	request(frame, 1)
	request(build(), 2)
}
