package sing

import (
	"fmt"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/sagernet/sing-box/adapter"
)

func addQUICUsers(in adapter.Inbound, users []panel.UserInfo) error {
	updater, ok := in.(interface{ AddUsers([]panel.UserInfo) error })
	if !ok {
		return fmt.Errorf("QUIC inbound does not support safe user updates")
	}
	return updater.AddUsers(users)
}
