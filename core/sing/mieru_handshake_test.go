package sing

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/Foxtea267/AriNode/common/rate"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/juju/ratelimit"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

type mieruTargetWriteBarrier struct {
	bytes.Buffer
	received chan struct{}
	resume   chan struct{}
}

func (w *mieruTargetWriteBarrier) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	close(w.received)
	<-w.resume // bytes reached the target, but its Write has not returned yet
	return n, err
}

func TestMieruCopyTrafficBecomesVisibleAfterTargetWrite(t *testing.T) {
	source, client := net.Pipe()
	defer source.Close()
	defer client.Close()
	source.SetDeadline(time.Now().Add(2 * time.Second))
	client.SetDeadline(time.Now().Add(2 * time.Second))
	storage := &counter.TrafficStorage{}
	conn := counter.NewConnCounter(rate.NewConnRateLimiter(source, ratelimit.NewBucketWithRate(1_000_000, 1_000_000)), storage)
	payload := []byte("AriNode Mieru authenticated forwarding")
	target := &mieruTargetWriteBarrier{received: make(chan struct{}), resume: make(chan struct{})}
	var once sync.Once
	resume := func() { once.Do(func() { close(target.resume) }) }
	defer resume()
	go func() { client.Write(payload); client.Close() }()
	finished := make(chan error, 1)
	go func() { _, err := bufio.Copy(target, conn); finished <- err }()
	select {
	case <-target.received:
	case <-time.After(2 * time.Second):
		t.Fatal("target did not receive the request")
	}
	if !bytes.Equal(target.Bytes(), payload) || storage.UpCounter.Load() != 0 {
		t.Fatal("expected delivered bytes before the upload callback became visible")
	}
	resume()
	select {
	case err := <-finished:
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("copy did not finish after the target Write returned")
	}
	if got := storage.UpCounter.Load(); got != int64(len(payload)) {
		t.Fatalf("upload did not accumulate after delivery: got %d want %d", got, len(payload))
	}
}

func TestMieruHandshakeSurvivesRoutedConnectionWrappers(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		peer, client := mieruHandshakeChain(t)
		defer peer.Close()
		defer client.Close()

		reported := make(chan error, 1)
		go func() { reported <- N.ReportConnHandshakeSuccess(client, nil) }()
		readMieruHandshakeReply(t, peer, 0)
		if err := <-reported; err != nil {
			t.Fatalf("report success: %v", err)
		}
		if err := N.ReportConnHandshakeSuccess(client, nil); err != nil {
			t.Fatalf("duplicate success report: %v", err)
		}
		assertNoDuplicateMieruReply(t, peer)
	})

	t.Run("failure", func(t *testing.T) {
		peer, client := mieruHandshakeChain(t)
		defer peer.Close()
		defer client.Close()

		failure := errors.New("target connection refused")
		reported := make(chan error, 1)
		go func() { reported <- N.ReportHandshakeFailure(client, failure) }()
		readMieruHandshakeReply(t, peer, 1)
		if err := <-reported; !errors.Is(err, failure) {
			t.Fatalf("report target failure lost original error: %v", err)
		}
		if err := N.ReportConnHandshakeSuccess(client, nil); err != nil {
			t.Fatalf("second report after failure: %v", err)
		}
		assertNoDuplicateMieruReply(t, peer)
	})
}

// Mirrors HookServer.RoutedConnection: counter -> ExtendedConn -> rate ->
// Mieru's internal SOCKS5 handshake reporter.
func mieruHandshakeChain(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	peer, server := net.Pipe()
	if err := server.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		peer.Close()
		server.Close()
		t.Fatal(err)
	}
	base := &mieruHandshakeConn{Conn: server}
	limited := rate.NewConnRateLimiter(base, ratelimit.NewBucketWithRate(1_000_000, 1_000_000))
	counterConn := counter.NewConnCounter(limited, &counter.TrafficStorage{})
	if success, ok := common.Cast[N.HandshakeSuccess](counterConn); !ok || success != base {
		peer.Close()
		server.Close()
		t.Fatal("success reporter hidden by counter/extended/rate wrapper chain")
	}
	if failure, ok := common.Cast[N.HandshakeFailure](counterConn); !ok || failure != base {
		peer.Close()
		server.Close()
		t.Fatal("failure reporter hidden by counter/extended/rate wrapper chain")
	}
	return peer, counterConn
}

func readMieruHandshakeReply(t *testing.T, conn net.Conn, expected uint8) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var response model.Response
	if err := response.ReadFromSocks5(conn); err != nil {
		t.Fatalf("read Mieru internal SOCKS5 reply: %v", err)
	}
	if response.Reply != expected {
		t.Fatalf("reply=%d, want %d", response.Reply, expected)
	}
}

func assertNoDuplicateMieruReply(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
	var extra [1]byte
	if n, err := conn.Read(extra[:]); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expected no duplicate handshake reply, got n=%d err=%v", n, err)
	}
}
