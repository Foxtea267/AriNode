package xray

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	"github.com/Foxtea267/AriNode/core/xray/app/dispatcher"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/outbound"
)

type DNSConfig struct {
	Servers []interface{} `json:"servers"`
	Tag     string        `json:"tag"`
}

func (c *Xray) AddNode(tag string, info *panel.NodeInfo, config *conf.Options) error {
	c.access.Lock()
	defer c.access.Unlock()
	if info.RoutingEnabled() {
		if _, err := compilePolicy(tag, "validate", info, config); err != nil {
			return fmt.Errorf("node %s: %w", tag, err)
		}
	}
	c.nodeReportMinTrafficBytes[tag] = config.ReportMinTraffic * 1024
	err := updateDNSConfig(info)
	if err != nil {
		return fmt.Errorf("build dns error: %s", err)
	}
	inboundConfig, err := buildInbound(config, info, tag)
	if err != nil {
		return fmt.Errorf("build inbound error: %s", err)
	}
	err = c.addInbound(inboundConfig)
	if err != nil {
		return fmt.Errorf("add inbound error: %s", err)
	}
	outBoundConfig, err := buildOutbound(config, tag)
	if err != nil {
		_ = c.removeInbound(tag)
		return fmt.Errorf("build outbound error: %s", err)
	}
	err = c.addOutbound(outBoundConfig)
	if err != nil {
		_ = c.removeInbound(tag)
		return fmt.Errorf("add outbound error: %s", err)
	}
	if err := c.updatePolicyLocked(tag, info, config); err != nil {
		_ = c.removeInbound(tag)
		_ = c.removeOutbound(tag)
		return err
	}
	return nil
}

func (c *Xray) addInbound(config *core.InboundHandlerConfig) error {
	rawHandler, err := core.CreateObject(c.Server, config)
	if err != nil {
		return err
	}
	handler, ok := rawHandler.(inbound.Handler)
	if !ok {
		return fmt.Errorf("not an InboundHandler: %s", err)
	}
	if err := c.ihm.AddHandler(context.Background(), handler); err != nil {
		_ = handler.Close()
		return err
	}
	return nil
}

func (c *Xray) addOutbound(config *core.OutboundHandlerConfig) error {
	rawHandler, err := core.CreateObject(c.Server, config)
	if err != nil {
		return err
	}
	handler, ok := rawHandler.(outbound.Handler)
	if !ok {
		return fmt.Errorf("not an InboundHandler: %s", err)
	}
	if err := c.ohm.AddHandler(context.Background(), handler); err != nil {
		_ = handler.Close()
		return err
	}
	return nil
}

func (c *Xray) DelNode(tag string) error {
	c.access.Lock()
	defer c.access.Unlock()
	inErr := c.removeInbound(tag)
	c.users.mapLock.Lock()
	for user := range c.users.uidMap {
		if strings.HasPrefix(user, tag+"|") {
			delete(c.users.uidMap, user)
			if v, ok := c.dispatcher.LinkManagers.LoadAndDelete(user); ok {
				v.(*dispatcher.LinkManager).CloseAll()
			}
		}
	}
	c.users.mapLock.Unlock()
	c.dispatcher.SetNodePolicy(tag, nil)
	outErr := c.removeOutbound(tag)
	delete(c.nodeReportMinTrafficBytes, tag)
	c.dispatcher.Counter.Delete(tag)
	return errors.Join(inErr, outErr)
}

func (c *Xray) removeInbound(tag string) error {
	return c.ihm.RemoveHandler(context.Background(), tag)
}

func (c *Xray) removeOutbound(tag string) error {
	err := c.ohm.RemoveHandler(context.Background(), tag)
	return err
}
