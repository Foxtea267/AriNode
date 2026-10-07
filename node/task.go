package node

import (
	"errors"
	"fmt"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/task"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/limiter"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) startTasks(node *panel.NodeInfo) {
	// fetch node info task
	c.nodeInfoMonitorPeriodic = &task.Task{
		Interval: authorizationInterval(node.PullInterval),
		Execute:  c.nodeInfoMonitor,
	}
	// fetch user list task
	c.userReportPeriodic = &task.Task{
		Interval: node.PushInterval,
		Execute:  c.reportUserTrafficTask,
	}
	log.WithField("tag", c.tag).Info("Start monitor node status")
	// delay to start nodeInfoMonitor
	_ = c.nodeInfoMonitorPeriodic.Start(false)
	log.WithField("tag", c.tag).Info("Start report node status")
	_ = c.userReportPeriodic.Start(false)
	if node.Security == panel.Tls {
		switch c.CertConfig.CertMode {
		case "none", "", "auto", "file", "self":
		default:
			c.renewCertPeriodic = &task.Task{
				Interval: time.Hour * 24,
				Execute:  c.renewCertTask,
			}
			log.WithField("tag", c.tag).Info("Start renew cert")
			// delay to start renewCert
			_ = c.renewCertPeriodic.Start(true)
		}
	}
	if c.LimitConfig.EnableDynamicSpeedLimit {
		c.traffic = make(map[string]int64)
		c.dynamicSpeedLimitPeriodic = &task.Task{
			Interval: time.Duration(c.LimitConfig.DynamicSpeedLimitConfig.Periodic) * time.Second,
			Execute:  c.SpeedChecker,
		}
		log.Printf("[%s: %d] Start dynamic speed limit", c.apiClient.NodeType, c.apiClient.NodeId)
	}
}

// The node API has no expiration timestamp. Bound the polling delay, and apply
// authorization before fetching/reloading node configuration so a broken config
// cannot keep expired users alive.
func authorizationInterval(interval time.Duration) time.Duration {
	if interval <= 0 || interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
}

func (c *Controller) nodeInfoMonitor() (err error) {
	newU, err := c.apiClient.GetUserList()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get user list failed")
		return nil
	}
	if err := c.applyUserList(newU, nil); err != nil {
		c.apiClient.InvalidateUserCache()
		log.WithError(err).WithField("tag", c.tag).Error("Apply user list failed; will retry full snapshot")
		return nil
	}
	// Alive/config requests must not delay applying a valid revocation snapshot.
	newA, aliveErr := c.apiClient.GetUserAlive()
	if aliveErr != nil {
		log.WithError(aliveErr).WithField("tag", c.tag).Error("Get alive list failed")
	} else {
		c.limiter.SetAliveList(newA, c.apiClient.AliveIPs)
		c.aliveMap = newA
	}
	newN, err := c.apiClient.GetNodeInfo()
	if err != nil {
		c.configError.Store(true)
		log.WithError(err).WithField("tag", c.tag).Error("Get node info failed; user authorization already synchronized")
		return nil
	}
	if newN == nil {
		newN = c.pendingNode
		if newN == nil {
			c.configError.Store(false)
		}
	}
	if newN != nil {
		c.pendingNode = newN
		if err := c.reloadNode(newN, nil, newA); err != nil {
			c.configError.Store(true)
			log.WithError(err).WithField("tag", c.tag).Error("Node reload failed; will retry")
		}
	}
	return nil
}

func (c *Controller) applyUserList(newU []panel.UserInfo, newA map[int]int) error {
	if newA != nil {
		c.limiter.SetAliveList(newA, c.apiClient.AliveIPs)
		c.aliveMap = newA
	}
	// node no changed, check users
	if newU == nil {
		return nil
	}
	deleted, added := compareUserList(c.userList, newU)
	if len(deleted) > 0 {
		// have deleted users
		if err := c.server.DelUsers(deleted, c.tag, c.info); err != nil {
			return fmt.Errorf("delete users: %w", err)
		}
		c.limiter.UpdateUser(c.tag, nil, deleted)
		// Commit successful revocations even when adding another user fails below.
		removed := make(map[string]bool, len(deleted))
		for _, user := range deleted {
			removed[user.Uuid] = true
		}
		remaining := make([]panel.UserInfo, 0, len(c.userList))
		for _, user := range c.userList {
			if !removed[user.Uuid] {
				remaining = append(remaining, user)
			}
		}
		c.userList = remaining
		if c.LimitConfig.EnableDynamicSpeedLimit {
			for _, user := range deleted {
				delete(c.traffic, user.Uuid)
			}
		}
	}
	if len(added) > 0 {
		// have added users
		_, err := c.server.AddUsers(&vCore.AddUsersParams{
			Tag:      c.tag,
			NodeInfo: c.info,
			Users:    added,
		})
		if err != nil {
			return fmt.Errorf("add users: %w", err)
		}
		c.limiter.UpdateUser(c.tag, added, nil)
	}
	c.userList = newU
	if len(added)+len(deleted) != 0 {
		log.WithField("tag", c.tag).
			Infof("%d user deleted, %d user added", len(deleted), len(added))
	}
	return nil
}

func (c *Controller) reloadNode(next *panel.NodeInfo, users []panel.UserInfo, alive map[int]int) error {
	if users == nil {
		users = c.userList
	}
	if alive == nil {
		alive = c.aliveMap
	}
	probe := &limiter.Limiter{}
	if err := probe.UpdateRule(next.LimiterRules()); err != nil {
		return fmt.Errorf("validate rule: %w", err)
	}
	if c.coreAdded && panel.InboundConfigEqual(c.info, next) {
		if updater, ok := c.server.(vCore.NodePolicyUpdater); ok {
			err := updater.UpdateNodePolicy(c.tag, next, c.Options)
			if err == nil {
				if err := c.limiter.UpdateRule(next.LimiterRules()); err != nil {
					return err
				}
				c.info = next
				c.pendingNode = nil
				c.configError.Store(false)
				c.updateNodeIntervals(next)
				log.WithField("node", c.tag).Info("Panel routing updated without restarting inbound")
				return nil
			}
			if !errors.Is(err, vCore.ErrPolicyReloadUnsupported) {
				return err
			}
		}
	}
	if next.Security == panel.Tls {
		if err := c.requestCert(); err != nil {
			return fmt.Errorf("request cert: %w", err)
		}
	}
	oldTag, oldInfo, oldUsers := c.tag, c.info, c.userList
	newTag := oldTag
	if c.Options.Name == "" {
		newTag = c.buildNodeTag(next)
	}
	if c.coreAdded {
		if err := c.server.DelNode(oldTag); err != nil {
			return fmt.Errorf("delete old node: %w", err)
		}
		c.coreAdded = false
	}
	restore := func(cause error) error {
		if oldInfo != nil {
			if err := c.server.AddNode(oldTag, oldInfo, c.Options); err == nil {
				c.coreAdded = true
				if _, userErr := c.server.AddUsers(&vCore.AddUsersParams{Tag: oldTag, Users: oldUsers, NodeInfo: oldInfo}); userErr != nil {
					log.WithError(userErr).WithField("tag", oldTag).Error("Restore old users failed")
				}
			} else {
				log.WithError(err).WithField("tag", oldTag).Error("Restore old node failed")
			}
		}
		return cause
	}
	if err := c.server.AddNode(newTag, next, c.Options); err != nil {
		return restore(fmt.Errorf("add new node: %w", err))
	}
	c.coreAdded = true
	if _, err := c.server.AddUsers(&vCore.AddUsersParams{Tag: newTag, Users: users, NodeInfo: next}); err != nil {
		if delErr := c.server.DelNode(newTag); delErr != nil {
			log.WithError(delErr).WithField("tag", newTag).Error("Clean failed replacement failed")
		}
		c.coreAdded = false
		return restore(fmt.Errorf("add new users: %w", err))
	}
	if oldTag != newTag {
		limiter.DeleteLimiter(oldTag)
	}
	c.limiter = limiter.AddLimiter(newTag, &c.LimitConfig, users, alive)
	c.limiter.SetAliveList(alive, c.apiClient.AliveIPs)
	if err := c.limiter.UpdateRule(next.LimiterRules()); err != nil {
		return fmt.Errorf("update rule: %w", err)
	}
	c.tag, c.info, c.userList, c.aliveMap = newTag, next, users, alive
	c.pendingNode = nil
	c.configError.Store(false)
	c.traffic = make(map[string]int64)
	c.updateNodeIntervals(next)
	log.WithField("tag", c.tag).Infof("Node reloaded with %d users", len(users))
	return nil
}

func (c *Controller) updateNodeIntervals(next *panel.NodeInfo) {
	if c.nodeInfoMonitorPeriodic != nil && next.PullInterval > 0 {
		_ = c.nodeInfoMonitorPeriodic.SetInterval(authorizationInterval(next.PullInterval))
	}
	if c.userReportPeriodic != nil && next.PushInterval > 0 {
		_ = c.userReportPeriodic.SetInterval(next.PushInterval)
	}
}

func (c *Controller) SpeedChecker() error {
	for u, t := range c.traffic {
		if t >= c.LimitConfig.DynamicSpeedLimitConfig.Traffic {
			err := c.limiter.UpdateDynamicSpeedLimit(c.tag, u,
				c.LimitConfig.DynamicSpeedLimitConfig.SpeedLimit,
				time.Now().Add(time.Duration(c.LimitConfig.DynamicSpeedLimitConfig.ExpireTime)*time.Minute))
			log.WithField("err", err).Error("Update dynamic speed limit failed")
			delete(c.traffic, u)
		}
	}
	return nil
}
