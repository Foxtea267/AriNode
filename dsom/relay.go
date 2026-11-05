package dsom

import (
	"context"
	"crypto/subtle"
	"io"
	"net"
	"time"
)

func (w *worker) track(connection net.Conn) bool {
	w.connectionsMu.Lock()
	defer w.connectionsMu.Unlock()
	if w.closed {
		connection.Close()
		return false
	}
	w.connections[connection] = true
	return true
}
func (w *worker) untrack(connection net.Conn) {
	w.connectionsMu.Lock()
	delete(w.connections, connection)
	w.connectionsMu.Unlock()
	connection.Close()
}
func (w *worker) accept() {
	defer w.wg.Done()
	for {
		connection, err := w.listener.Accept()
		if err != nil {
			return
		}
		if !w.track(connection) {
			continue
		}
		w.wg.Add(1)
		go func() { defer w.wg.Done(); defer w.untrack(connection); w.serve(connection) }()
	}
}
func (w *worker) serve(connection net.Conn) {
	connection.SetDeadline(time.Now().Add(10 * time.Second))
	var head [2]byte
	if _, err := io.ReadFull(connection, head[:]); err != nil || head[0] != 5 || head[1] == 0 {
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(connection, methods); err != nil {
		return
	}
	found := false
	for _, method := range methods {
		found = found || method == 2
	}
	if !found {
		connection.Write([]byte{5, 255})
		return
	}
	if _, err := connection.Write([]byte{5, 2}); err != nil {
		return
	}
	if _, err := io.ReadFull(connection, head[:]); err != nil || head[0] != 1 || head[1] == 0 {
		return
	}
	user := make([]byte, int(head[1]))
	if _, err := io.ReadFull(connection, user); err != nil {
		return
	}
	var length [1]byte
	if _, err := io.ReadFull(connection, length[:]); err != nil {
		return
	}
	password := make([]byte, int(length[0]))
	if _, err := io.ReadFull(connection, password); err != nil {
		return
	}
	if subtle.ConstantTimeCompare(user, []byte("dsom")) != 1 || subtle.ConstantTimeCompare(password, []byte(w.token)) != 1 {
		connection.Write([]byte{1, 1})
		return
	}
	if _, err := connection.Write([]byte{1, 0}); err != nil {
		return
	}
	var request [4]byte
	if _, err := io.ReadFull(connection, request[:]); err != nil || request[0] != 5 || request[2] != 0 {
		return
	}
	target, err := readAddress(connection, request[3])
	if err != nil {
		return
	}
	// Consume the complete request before closing: unread TCP data can cause a
	// reset that discards the explicit command-not-supported reply on Windows.
	if request[1] != 1 {
		connection.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	selected := w.selected.Load()
	if selected == nil {
		connection.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	ctx, cancel := context.WithTimeout(w.ctx, time.Duration(selected.timeout)*time.Second)
	upstream, err := dialSOCKS(ctx, selected.endpoint, target, &w.dns, time.Duration(selected.dnsTTL)*time.Second)
	cancel()
	if err != nil {
		connection.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	if !w.track(upstream) {
		return
	}
	defer w.untrack(upstream)
	if _, err := connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	connection.SetDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		io.Copy(upstream, connection)
		if half, ok := upstream.(interface{ CloseWrite() error }); ok {
			half.CloseWrite()
		}
		close(done)
	}()
	io.Copy(connection, upstream)
	if half, ok := connection.(interface{ CloseWrite() error }); ok {
		half.CloseWrite()
	}
	<-done
}
