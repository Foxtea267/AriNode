package xray

import (
	"strings"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/format"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/proxy/shadowsocks"
	"github.com/xtls/xray-core/proxy/shadowsocks_2022"
)

func buildSSUsers(tag string, userInfo []panel.UserInfo, cypher string, serverKey string) ([]*protocol.User, error) {
	users := make([]*protocol.User, len(userInfo))
	for i := range userInfo {
		user, err := buildSSUser(tag, &userInfo[i], cypher, serverKey)
		if err != nil {
			return nil, err
		}
		users[i] = user
	}
	return users, nil
}

func buildSSUser(tag string, userInfo *panel.UserInfo, cypher string, serverKey string) (*protocol.User, error) {
	password, err := format.ShadowsocksUserPassword(cypher, userInfo.Uuid)
	if err != nil {
		return nil, err
	}
	if serverKey == "" {
		ssAccount := &shadowsocks.Account{
			Password:   userInfo.Uuid,
			CipherType: getCipherFromString(cypher),
		}
		return &protocol.User{
			Level:   0,
			Email:   format.UserTag(tag, userInfo.Uuid),
			Account: serial.ToTypedMessage(ssAccount),
		}, nil
	} else {
		ssAccount := &shadowsocks_2022.Account{
			Key: password,
		}
		return &protocol.User{
			Level:   0,
			Email:   format.UserTag(tag, userInfo.Uuid),
			Account: serial.ToTypedMessage(ssAccount),
		}, nil
	}
}

func getCipherFromString(c string) shadowsocks.CipherType {
	switch strings.ToLower(c) {
	case "aes-128-gcm", "aead_aes_128_gcm":
		return shadowsocks.CipherType_AES_128_GCM
	case "aes-256-gcm", "aead_aes_256_gcm":
		return shadowsocks.CipherType_AES_256_GCM
	case "chacha20-poly1305", "aead_chacha20_poly1305", "chacha20-ietf-poly1305":
		return shadowsocks.CipherType_CHACHA20_POLY1305
	case "none", "plain":
		return shadowsocks.CipherType_NONE
	default:
		return shadowsocks.CipherType_UNKNOWN
	}
}
