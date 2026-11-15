package sing

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-vmess"
	M "github.com/sagernet/sing/common/metadata"
)

type recordVMessConn struct {
	net.Conn
	buffer bytes.Buffer
}

func (c *recordVMessConn) Write(p []byte) (int, error) { return c.buffer.Write(p) }

func TestVMessReplayRejectedAcrossUserRefresh(t *testing.T) {
	router := &stableVLESSTestRouter{}
	in, err := newStableVMessInbound(context.Background(), router, log.NewNOPFactory().NewLogger("vmess-replay"), "vmess-replay",
		option.VMessInboundOptions{Users: []option.VMessUser{{Name: "healthy", UUID: stableVLESSTestUser}}})
	if err != nil {
		t.Fatal(err)
	}
	h := in.(*stableVMessInbound)
	defer h.Close()
	client, err := vmess.NewClient(stableVLESSTestUser, "auto", 0)
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
	replayRequest := func(frame []byte, want int32) {
		t.Helper()
		raw, peer := net.Pipe()
		defer raw.Close()
		defer peer.Close()
		_ = peer.SetWriteDeadline(time.Now().Add(time.Second))
		done := make(chan struct{})
		go func() { defer close(done); _, _ = peer.Write(frame) }()
		h.NewConnectionEx(context.Background(), raw, adapter.InboundContext{Source: M.ParseSocksaddr("127.0.0.1:12345")}, nil)
		<-done
		if got := router.routes.Load(); got != want {
			t.Fatalf("routed %d requests, want %d", got, want)
		}
	}
	frame := build()
	replayRequest(frame, 1)
	if err := h.AddUsers([]option.VMessUser{{Name: "new", UUID: stableVLESSTestOther}}); err != nil {
		t.Fatal(err)
	}
	replayRequest(frame, 1)
	replayRequest(build(), 2)
}
