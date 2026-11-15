package hy2

import (
	"fmt"
	"strings"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type Hysteria2node struct {
	Auth          *V2bX
	Hy2server     server.Server
	Tag           string
	Logger        *zap.Logger
	EventLogger   server.EventLogger
	TrafficLogger server.TrafficLogger
}

func (h *Hysteria2) AddNode(tag string, info *panel.NodeInfo, config *conf.Options) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.Hy2nodes[tag]; exists {
		return fmt.Errorf("node already exists")
	}
	if info.RoutingEnabled() {
		return fmt.Errorf("node %s: panel routing requires the sing-box core for hysteria2", tag)
	}
	var err error
	hyconfig := &server.Config{}
	var c serverConfig
	v := viper.New()
	if len(config.Hysteria2ConfigPath) != 0 {
		v.SetConfigFile(config.Hysteria2ConfigPath)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("read Hysteria2 server config: %w", err)
		}
		if err := v.Unmarshal(&c); err != nil {
			return fmt.Errorf("parse Hysteria2 server config: %w", err)
		}
	}
	auth := &V2bX{tag: tag}
	n := Hysteria2node{
		Auth:   auth,
		Tag:    tag,
		Logger: h.Logger,
		EventLogger: &serverLogger{
			auth:   auth,
			Tag:    tag,
			logger: h.Logger,
		},
		TrafficLogger: &HookServer{
			auth: auth,
			Tag:  tag,

			ReportMinTrafficBytes: config.ReportMinTraffic * 1024,
		},
	}

	hyconfig, err = n.getHyConfig(info, config, &c)
	if err != nil {
		return err
	}
	hyconfig.Authenticator = auth
	s, err := server.NewServer(hyconfig)
	if err != nil {
		_ = hyconfig.Conn.Close()
		return err
	}
	n.Hy2server = s
	h.Hy2nodes[tag] = n
	go func() {
		if err := s.Serve(); err != nil {
			if !strings.Contains(err.Error(), "quic: server closed") {
				h.Logger.Error("Server Error", zap.Error(err))
			}
		}
	}()
	return nil
}

func (h *Hysteria2) DelNode(tag string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	n, ok := h.Hy2nodes[tag]
	if !ok {
		return nil
	}
	n.Auth.close()
	delete(h.Hy2nodes, tag)
	return n.Hy2server.Close()
}
