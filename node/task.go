package node

import (
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
		Interval: node.PullInterval,
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
		case "none", "", "file", "self":
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

func (c *Controller) nodeInfoMonitor() (err error) {
	// get node info
	newN := c.pendingNode
	if newN == nil {
		newN, err = c.apiClient.GetNodeInfo()
	}
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get node info failed")
		return nil
	}
	// get user info
	newU, err := c.apiClient.GetUserList()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get user list failed")
		return nil
	}
	// get user alive
	newA, err := c.apiClient.GetUserAlive()
	if err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("Get alive list failed")
		return nil
	}
	if newN != nil {
		c.pendingNode = newN
		if err := c.reloadNode(newN, newU, newA); err != nil {
			log.WithError(err).WithField("tag", c.tag).Error("Node reload failed; will retry")
		}
		return nil
	}
	// update alive list
	if newA != nil {
		c.limiter.AliveList = newA
	}
	// node no changed, check users
	if newU == nil {
		return nil
	}
	deleted, added := compareUserList(c.userList, newU)
	if len(deleted) > 0 {
		// have deleted users
		err = c.server.DelUsers(deleted, c.tag, c.info)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("Delete users failed")
			return nil
		}
	}
	if len(added) > 0 {
		// have added users
		_, err = c.server.AddUsers(&vCore.AddUsersParams{
			Tag:      c.tag,
			NodeInfo: c.info,
			Users:    added,
		})
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("Add users failed")
			return nil
		}
	}
	if len(added) > 0 || len(deleted) > 0 {
		// update Limiter
		c.limiter.UpdateUser(c.tag, added, deleted)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("limiter users failed")
			return nil
		}
		// clear traffic record
		if c.LimitConfig.EnableDynamicSpeedLimit {
			for i := range deleted {
				delete(c.traffic, deleted[i].Uuid)
			}
		}
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
	if err := probe.UpdateRule(&next.Rules); err != nil {
		return fmt.Errorf("validate rule: %w", err)
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
	if err := c.limiter.UpdateRule(&next.Rules); err != nil {
		return fmt.Errorf("update rule: %w", err)
	}
	c.tag, c.info, c.userList, c.aliveMap = newTag, next, users, alive
	c.pendingNode = nil
	c.traffic = make(map[string]int64)
	if c.nodeInfoMonitorPeriodic != nil && next.PullInterval > 0 {
		_ = c.nodeInfoMonitorPeriodic.SetInterval(next.PullInterval)
	}
	if c.userReportPeriodic != nil && next.PushInterval > 0 {
		_ = c.userReportPeriodic.SetInterval(next.PushInterval)
	}
	log.WithField("tag", c.tag).Infof("Node reloaded with %d users", len(users))
	return nil
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
