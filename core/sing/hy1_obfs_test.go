//go:build with_quic && with_grpc

package sing

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core"
)

func TestHY1ObfsUserSnapshotsKeepHealthyTCPAndUDP(t *testing.T) {
	f := newRevocationQUICFixture(t, "hysteria")
	// Configure obfuscation before any client authenticates. Reuse the fixture's
	// real core and limiter without changing the shared fixture implementation.
	if err := f.server.DelNode(f.tag); err != nil {
		t.Fatal(err)
	}
	f.info.Hysteria.Obfs = "xplus-test"
	if err := f.server.AddNode(f.tag, f.info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.AddUsers(&core.AddUsersParams{Tag: f.tag, NodeInfo: f.info, Users: f.users}); err != nil {
		t.Fatal(err)
	}
	healthy, healthyGate := f.client(t, quicHealthyUUID)
	revoked, revokedGate := f.client(t, quicExpiredUUID)
	destination := quicFixedResponseTarget(t)
	healthyStream := quicDialVerified(t, healthy, destination)
	revokedStream := quicDialVerified(t, revoked, destination)
	healthyGate.blocked.Store(true)
	revokedGate.blocked.Store(true)

	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, address, err := echo.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(buffer[:n], address)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	packet, err := healthy.ListenPacket(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { packet.Close() })
	roundTripUDP := func() error {
		if err := packet.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		payload := []byte("HY1 obfs snapshot UDP")
		if _, err := packet.WriteTo(payload, echo.LocalAddr()); err != nil {
			return err
		}
		buffer := make([]byte, 128)
		n, _, err := packet.ReadFrom(buffer)
		if err == nil && !bytes.Equal(buffer[:n], payload) {
			return fmt.Errorf("obfuscated UDP payload changed")
		}
		return err
	}
	if err := roundTripUDP(); err != nil {
		t.Fatal(err)
	}

	// Starting an immutable SDK snapshot passes nil as the packet socket.
	// With XPlus enabled this must retain the existing real listener/obfuscator.
	f.addUsers(t, []panel.UserInfo{{Id: 109, Uuid: quicRenewedUUID}})
	if err := quicFixedRoundTrip(healthyStream); err != nil {
		t.Fatalf("obfuscated healthy TCP failed after snapshot update: %v", err)
	}
	if err := roundTripUDP(); err != nil {
		t.Fatalf("obfuscated healthy UDP failed after snapshot update: %v", err)
	}
	if err := quicDeleteWithoutPanic(f, f.users[:1]); err != nil {
		t.Fatal(err)
	}
	if err := quicFixedRoundTrip(revokedStream); err == nil {
		t.Fatal("revoked obfuscated stream still forwards")
	}
	quicAssertReusedDenied(t, revoked, destination)
	if err := quicFixedRoundTrip(healthyStream); err != nil {
		t.Fatalf("revocation closed healthy obfuscated TCP: %v", err)
	}
	if err := roundTripUDP(); err != nil {
		t.Fatalf("revocation closed healthy obfuscated UDP: %v", err)
	}
	if healthyGate.dials.Load() != 1 || revokedGate.dials.Load() != 1 {
		t.Fatalf("existing clients replaced QUIC parents: healthy=%d revoked=%d", healthyGate.dials.Load(), revokedGate.dials.Load())
	}
	fresh, _ := f.client(t, quicRenewedUUID)
	quicDialVerified(t, fresh, destination)
}
