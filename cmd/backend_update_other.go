//go:build !linux

package cmd

import "github.com/Foxtea267/AriNode/conf"

func startBackendUpdates(*conf.Conf, string, string) (func(*conf.Conf), func()) {
	return func(*conf.Conf) {}, func() {}
}
