package dsom

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These SOCKS servers really authenticate, CONNECT and forward requests to the
// independent HTTP fixtures. Their header simulates an observed public exit.
type mockProxy struct {
	listener           net.Listener
	exit               atomic.Value
	mode               atomic.Int32
	calls              atomic.Int32
	forwarded          atomic.Int32
	username, password string
	queryPorts         map[string]bool
	mu                 sync.Mutex
	connections        map[net.Conn]bool
	wg                 sync.WaitGroup
}

func proxyFixture(t *testing.T, ports map[string]bool, username, password string) *mockProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &mockProxy{listener: listener, username: username, password: password, queryPorts: ports, connections: map[net.Conn]bool{}}
	p.exit.Store("116.206.253.134")
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.connections[connection] = true
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer func() { p.mu.Lock(); delete(p.connections, connection); p.mu.Unlock(); connection.Close() }()
				p.serve(connection)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		p.mu.Lock()
		for c := range p.connections {
			c.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}
func (p *mockProxy) endpoint() Endpoint {
	address := p.listener.Addr().(*net.TCPAddr)
	return Endpoint{Server: "127.0.0.1", Port: address.Port}
}
func (p *mockProxy) serve(connection net.Conn) {
	p.calls.Add(1)
	connection.SetDeadline(time.Now().Add(4 * time.Second))
	if p.mode.Load() == 1 {
		connection.Write([]byte("not SOCKS5"))
		return
	}
	if p.mode.Load() == 2 {
		return
	}
	var head [2]byte
	if _, err := io.ReadFull(connection, head[:]); err != nil || head[0] != 5 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(connection, methods); err != nil {
		return
	}
	method := byte(0)
	if p.username != "" {
		method = 2
	}
	connection.Write([]byte{5, method})
	if method == 2 {
		if _, err := io.ReadFull(connection, head[:]); err != nil {
			return
		}
		user := make([]byte, int(head[1]))
		if _, err := io.ReadFull(connection, user); err != nil {
			return
		}
		var size [1]byte
		if _, err := io.ReadFull(connection, size[:]); err != nil {
			return
		}
		password := make([]byte, int(size[0]))
		if _, err := io.ReadFull(connection, password); err != nil {
			return
		}
		if string(user) != p.username || string(password) != p.password || p.mode.Load() == 3 {
			connection.Write([]byte{1, 1})
			return
		}
		connection.Write([]byte{1, 0})
	}
	var request [4]byte
	if _, err := io.ReadFull(connection, request[:]); err != nil {
		return
	}
	target, err := readAddress(connection, request[3])
	if err != nil {
		return
	}
	if p.mode.Load() == 4 {
		connection.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	upstream, err := net.DialTimeout("tcp", target, time.Second)
	if err != nil {
		connection.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	p.forwarded.Add(1)
	connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	connection.SetDeadline(time.Time{})
	_, port, _ := net.SplitHostPort(target)
	if p.queryPorts[port] {
		if p.mode.Load() == 5 {
			connection.Write([]byte("HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
			return
		}
		request, err := http.ReadRequest(bufio.NewReader(connection))
		if err != nil {
			return
		}
		request.Header.Set("X-Mock-Exit", p.exit.Load().(string))
		if err := request.Write(upstream); err != nil {
			return
		}
		done := make(chan struct{})
		go func() { io.Copy(upstream, connection); upstream.Close(); close(done) }()
		io.Copy(connection, upstream)
		connection.Close()
		<-done
		return
	}
	done := make(chan struct{})
	go func() { io.Copy(upstream, connection); upstream.Close(); close(done) }()
	io.Copy(connection, upstream)
	connection.Close()
	<-done
}

type sourceFixture struct {
	sources  []Source
	ports    map[string]bool
	mode     atomic.Int32
	requests atomic.Int32
}

func sourcesFixture(t *testing.T) *sourceFixture {
	f := &sourceFixture{ports: map[string]bool{}}
	for i := 0; i < 2; i++ {
		index := i
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.requests.Add(1)
			if f.mode.Load() == 1 && index == 1 {
				http.Error(w, "source outage", 503)
				return
			}
			value := r.Header.Get("X-Mock-Exit")
			if value == "" {
				value = "198.51.100.1"
			} // A direct-host request must not pass.
			if f.mode.Load() == 2 && index == 1 {
				value = "1.1.1.1"
			}
			if f.mode.Load() == 3 {
				value = "127.0.0.1"
			}
			if index == 0 {
				fmt.Fprint(w, value)
			} else {
				fmt.Fprintf(w, `{"ip":%q}`, value)
			}
		}))
		t.Cleanup(server.Close)
		_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
		f.ports[port] = true
		source := Source{URL: server.URL}
		if i == 1 {
			source.JSONField = "ip"
		}
		f.sources = append(f.sources, source)
	}
	return f
}
func testConfig(tag string, gateway Endpoint, sources []Source) Config {
	return Config{Tag: tag, Enabled: true, Mode: "auto", Gateway: &gateway, Direct: Direct{Enabled: true}, HealthCheck: HealthCheck{Interval: 60, Timeout: 1, FailureThreshold: 2, SuccessThreshold: 2, Cooldown: 300, Sources: sources}}
}
func workerFixture(t *testing.T, c Config) *worker {
	t.Helper()
	normalized, err := normalize(c)
	if err != nil {
		t.Fatal(err)
	}
	w, err := newWorker(normalized, t.TempDir(), make(chan struct{}, 4))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.close)
	return w
}
func checkWorker(t *testing.T, w *worker) Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	w.check(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}
func relayDial(t *testing.T, w *worker, target string) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	port := w.listener.Addr().(*net.TCPAddr).Port
	e := endpoint{Endpoint: Endpoint{Server: "127.0.0.1", Port: port}, username: "dsom", password: w.token}
	connection, err := dialSOCKS(ctx, e, target, &resolver{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}
func echoFixture(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return listener
}
func assertEcho(t *testing.T, c net.Conn, value string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(c, value); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, len(value))
	if _, err := io.ReadFull(c, body); err != nil || string(body) != value {
		t.Fatalf("echo failed: %v", err)
	}
	c.SetDeadline(time.Time{})
}
func addressPort(t *testing.T, value string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		t.Fatal(err)
	}
	result, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
