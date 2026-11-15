package counter_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/common/counter"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

type countingTestConn struct {
	reader                     *bytes.Reader
	writes                     bytes.Buffer
	readFinalError, writeError error
	writeSize                  int
}

func (c *countingTestConn) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if n > 0 && c.reader.Len() == 0 && c.readFinalError != nil {
		err = c.readFinalError
	}
	return n, err
}

func (c *countingTestConn) Write(p []byte) (int, error) {
	if c.writeSize > 0 && len(p) > c.writeSize {
		p = p[:c.writeSize]
	}
	n, _ := c.writes.Write(p)
	return n, c.writeError
}

func (*countingTestConn) Close() error                     { return nil }
func (*countingTestConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*countingTestConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*countingTestConn) SetDeadline(time.Time) error      { return nil }
func (*countingTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*countingTestConn) SetWriteDeadline(time.Time) error { return nil }

func TestConnReadWriteAccumulateAndRetainEOFTraffic(t *testing.T) {
	storage := &counter.TrafficStorage{}
	base := &countingTestConn{reader: bytes.NewReader([]byte("abcdef"))}
	conn := counter.NewConnCounter(base, storage)
	for range 3 {
		p := make([]byte, 2)
		if n, err := conn.Read(p); n != 2 || err != nil {
			t.Fatalf("read n=%d err=%v", n, err)
		}
		if n, err := conn.Write(p); n != 2 || err != nil {
			t.Fatalf("write n=%d err=%v", n, err)
		}
	}
	if n, err := conn.Read(make([]byte, 2)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF read n=%d err=%v", n, err)
	}
	if n, err := conn.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write n=%d err=%v", n, err)
	}
	if up, down := storage.UpCounter.Load(), storage.DownCounter.Load(); up != 6 || down != 6 {
		t.Fatalf("direct operations lost cumulative traffic after EOF/empty write: up=%d down=%d want=6/6", up, down)
	}
}

func TestConnCountsPositiveBytesAlongsideTransportErrors(t *testing.T) {
	readErr, writeErr := errors.New("read sentinel"), errors.New("write sentinel")
	storage := &counter.TrafficStorage{}
	storage.UpCounter.Store(10)
	storage.DownCounter.Store(20)
	base := &countingTestConn{reader: bytes.NewReader([]byte("last")), readFinalError: readErr, writeError: writeErr, writeSize: 3}
	conn := counter.NewConnCounter(base, storage)
	if n, err := conn.Read(make([]byte, 8)); n != 4 || err != readErr {
		t.Fatalf("partial read n=%d err=%v", n, err)
	}
	if n, err := conn.Write([]byte("partial write")); n != 3 || err != writeErr {
		t.Fatalf("partial write n=%d err=%v", n, err)
	}
	if up, down := storage.UpCounter.Load(), storage.DownCounter.Load(); up != 14 || down != 23 {
		t.Fatalf("partial positive bytes lost: up=%d down=%d want=14/23", up, down)
	}
}

func TestConnMixedDirectExtendedAndCopyAccounting(t *testing.T) {
	for _, copyPath := range []struct {
		name string
		copy func(io.Writer, io.Reader) (int64, error)
	}{
		{"io.Copy", io.Copy},
		{"sing_bufio.Copy", bufio.Copy},
	} {
		t.Run(copyPath.name, func(t *testing.T) {
			storage := &counter.TrafficStorage{}
			payload := []byte("abcdef")
			base := &countingTestConn{reader: bytes.NewReader(payload)}
			conn := counter.NewConnCounter(base, storage)
			extended := conn.(N.ExtendedConn)
			// Mix all paths on one storage, so a later ordinary operation must
			// not overwrite bytes recorded by extended copies or vice versa.
			if _, err := io.ReadFull(conn, make([]byte, 2)); err != nil {
				t.Fatal(err)
			}
			readBuffer := buf.NewSize(2)
			if err := extended.ReadBuffer(readBuffer); err != nil {
				t.Fatal(err)
			}
			readBuffer.Release()
			var received bytes.Buffer
			if n, err := copyPath.copy(&received, conn); err != nil || n != 2 || received.String() != "ef" {
				t.Fatalf("copy read n=%d err=%v received=%q", n, err, received.String())
			}
			if _, err := conn.Write(payload[:2]); err != nil {
				t.Fatal(err)
			}
			if err := extended.WriteBuffer(buf.As(payload[2:4])); err != nil {
				t.Fatal(err)
			}
			if n, err := copyPath.copy(conn, bytes.NewReader(payload[4:])); err != nil || n != 2 {
				t.Fatalf("copy write n=%d err=%v", n, err)
			}
			if !bytes.Equal(base.writes.Bytes(), payload) {
				t.Fatal("copy changed payload")
			}
			if up, down := storage.UpCounter.Load(), storage.DownCounter.Load(); up != 6 || down != 6 {
				t.Fatalf("mixed paths double counted or lost bytes: up=%d down=%d want=6/6", up, down)
			}
		})
	}
}

func TestConcurrentConnectionsAccumulateIntoSharedTraffic(t *testing.T) {
	const sessions, rounds = 4, 1000
	storage := &counter.TrafficStorage{}
	var workers sync.WaitGroup
	for range sessions {
		workers.Add(1)
		go func() {
			defer workers.Done()
			base := &countingTestConn{reader: bytes.NewReader(bytes.Repeat([]byte("up"), rounds))}
			conn := counter.NewConnCounter(base, storage)
			for range rounds {
				p := make([]byte, 2)
				if n, err := conn.Read(p); n != 2 || err != nil {
					t.Errorf("concurrent read n=%d err=%v", n, err)
					return
				}
				if n, err := conn.Write([]byte("down")); n != 4 || err != nil {
					t.Errorf("concurrent write n=%d err=%v", n, err)
					return
				}
			}
			if n, err := conn.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
				t.Errorf("concurrent EOF n=%d err=%v", n, err)
			}
		}()
	}
	workers.Wait()
	if up, down := storage.Snapshot(false); up != sessions*rounds*2 || down != sessions*rounds*4 {
		t.Fatalf("shared connection totals lost bytes: %d/%d want=%d/%d", up, down, sessions*rounds*2, sessions*rounds*4)
	}
}
