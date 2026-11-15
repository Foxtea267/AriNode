//go:build !with_quic

package sing

import "github.com/sagernet/sing-box/adapter/inbound"

// The dependency's registered stubs retain their usual clear build-tag error.
func registerQUICInbounds(_ *inbound.Registry) {}
