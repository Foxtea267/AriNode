package dsom

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

var (
	errConnect  = errors.New("proxy_tcp_connect_failed")
	errProtocol = errors.New("socks5_handshake_failed")
	errAuth     = errors.New("socks5_authentication_failed")
	errForward  = errors.New("socks5_forwarding_failed")
)

type dnsEntry struct {
	addresses []netip.Addr
	expires   time.Time
}
type resolver struct {
	mu      sync.Mutex
	entries map[string]dnsEntry
	flights map[string]chan struct{}
	lookup  func(context.Context, string) ([]netip.Addr, error)
}

func (r *resolver) peek(host string) []string {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []string{ip.String()}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := []string{}
	for _, ip := range r.entries[host].addresses {
		result = append(result, ip.String())
	}
	return result
}

func (r *resolver) addresses(ctx context.Context, host string, ttl time.Duration) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	r.mu.Lock()
	if cached := r.entries[host]; time.Now().Before(cached.expires) {
		r.mu.Unlock()
		if len(cached.addresses) == 0 {
			return nil, errConnect
		}
		return append([]netip.Addr(nil), cached.addresses...), nil
	}
	if pending := r.flights[host]; pending != nil {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, errConnect
		case <-pending:
			return r.addresses(ctx, host, ttl)
		}
	}
	if r.flights == nil {
		r.flights = map[string]chan struct{}{}
	}
	pending := make(chan struct{})
	r.flights[host] = pending
	r.mu.Unlock()
	lookup := r.lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	ips, err := lookup(ctx, host)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.flights, host)
	close(pending)
	if r.entries == nil {
		r.entries = map[string]dnsEntry{}
	}
	if err != nil || len(ips) == 0 {
		r.entries[host] = dnsEntry{expires: time.Now().Add(5 * time.Second)}
		return nil, errConnect
	}
	if r.entries == nil {
		r.entries = map[string]dnsEntry{}
	}
	if len(r.entries) >= 256 {
		for key := range r.entries {
			delete(r.entries, key)
			break
		}
	}
	r.entries[host] = dnsEntry{addresses: ips, expires: time.Now().Add(ttl)}
	return append([]netip.Addr(nil), ips...), nil
}

func (r *resolver) dial(ctx context.Context, e endpoint, ttl time.Duration) (net.Conn, error) {
	ips, err := r.addresses(ctx, e.Server, ttl)
	if err != nil {
		return nil, errConnect
	}
	for i, ip := range ips {
		attempt, stop := ctx, func() {}
		if deadline, ok := ctx.Deadline(); ok {
			attempt, stop = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(ips)-i))
		}
		connection, err := (&net.Dialer{}).DialContext(attempt, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(e.Port)))
		stop()
		if err == nil {
			r.mu.Lock()
			entry := r.entries[e.Server]
			if i > 0 && len(entry.addresses) == len(ips) {
				entry.addresses = append([]netip.Addr{ip}, append(append([]netip.Addr(nil), ips[:i]...), ips[i+1:]...)...)
				r.entries[e.Server] = entry
			}
			r.mu.Unlock()
			return connection, nil
		}
	}
	return nil, errConnect
}

// Requests contain destination domain names: DNS for an IP-query service is
// resolved by the upstream proxy, never mistaken for the AriNode host's exit.
func dialSOCKS(ctx context.Context, e endpoint, target string, dns *resolver, ttl time.Duration) (_ net.Conn, resultErr error) {
	connection, err := dns.dial(ctx, e, ttl)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer func() {
		stop()
		if resultErr != nil {
			connection.Close()
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		connection.SetDeadline(deadline)
	}
	method := byte(0)
	if e.username != "" {
		method = 2
	}
	if _, err := connection.Write([]byte{5, 1, method}); err != nil {
		return nil, errProtocol
	}
	var reply [2]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil || reply[0] != 5 || reply[1] != method {
		return nil, errProtocol
	}
	if method == 2 {
		request := append([]byte{1, byte(len(e.username))}, []byte(e.username)...)
		request = append(request, byte(len(e.password)))
		request = append(request, []byte(e.password)...)
		if _, err := connection.Write(request); err != nil {
			return nil, errAuth
		}
		if _, err := io.ReadFull(connection, reply[:]); err != nil || reply[0] != 1 || reply[1] != 0 {
			return nil, errAuth
		}
	}
	address, err := encodeAddress(target)
	if err != nil {
		return nil, errForward
	}
	if _, err := connection.Write(append([]byte{5, 1, 0}, address...)); err != nil {
		return nil, errForward
	}
	var head [4]byte
	if _, err := io.ReadFull(connection, head[:]); err != nil || head[0] != 5 || head[1] != 0 || head[2] != 0 {
		return nil, errForward
	}
	if _, err := readAddress(connection, head[3]); err != nil {
		return nil, errForward
	}
	connection.SetDeadline(time.Time{})
	return connection, nil
}

func encodeAddress(target string) ([]byte, error) {
	host, value, err := net.SplitHostPort(target)
	if err != nil {
		return nil, errForward
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return nil, errForward
	}
	var result []byte
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		if ip.Is4() {
			result = append([]byte{1}, ip.AsSlice()...)
		} else {
			result = append([]byte{4}, ip.AsSlice()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, errForward
		}
		result = append([]byte{3, byte(len(host))}, []byte(host)...)
	}
	return binary.BigEndian.AppendUint16(result, uint16(port)), nil
}

func readAddress(reader io.Reader, kind byte) (string, error) {
	length := 0
	switch kind {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(reader, size[:]); err != nil || size[0] == 0 {
			return "", errProtocol
		}
		length = int(size[0])
	default:
		return "", errProtocol
	}
	data := make([]byte, length+2)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", errProtocol
	}
	port := binary.BigEndian.Uint16(data[length:])
	host := string(data[:length])
	if kind != 3 {
		ip, ok := netip.AddrFromSlice(data[:length])
		if !ok {
			return "", errProtocol
		}
		host = ip.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}
