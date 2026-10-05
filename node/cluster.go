package node

import (
	"github.com/Foxtea267/AriNode/common/sysstatus"
	log "github.com/sirupsen/logrus"
	"time"
)

func (c *Controller) reportClusterTask() error {
	// Retain the request ID for retries; native alive/status would overwrite peers.
	if c.pendingClusterReport == nil {
		// Allocate the report ID before draining counters, so an entropy failure
		// cannot discard an already-consumed traffic snapshot.
		report, err := c.apiClient.NewClusterReport(nil, nil, nil)
		if err != nil {
			log.WithError(err).Warn("Create cluster report failed")
			return nil
		}
		traffic, err := c.server.GetUserTrafficSlice(c.tag, true)
		if err != nil {
			log.WithError(err).Warn("Read cluster traffic failed")
			return nil
		}
		online, err := c.limiter.GetOnlineDevice()
		if err != nil {
			log.WithError(err).Warn("Read cluster devices failed")
			return nil
		}
		if c.clusterAlive == nil {
			c.clusterAlive = make(map[int]map[string]time.Time)
		}
		now := time.Now()
		for _, d := range *online {
			if c.clusterAlive[d.UID] == nil {
				c.clusterAlive[d.UID] = make(map[string]time.Time)
			}
			c.clusterAlive[d.UID][d.IP] = now
		}
		for _, u := range traffic {
			if u.Upload+u.Download > 0 {
				for ip := range c.clusterAlive[u.UID] {
					c.clusterAlive[u.UID][ip] = now
				}
			}
		}
		allowed := c.limiter.AllowedUserIDs()
		retention := 3 * c.info.PushInterval
		if retention < 180*time.Second {
			retention = 180 * time.Second
		}
		alive := make(map[int][]string)
		for uid, ips := range c.clusterAlive {
			if !allowed[uid] {
				delete(c.clusterAlive, uid)
				continue
			}
			for ip, seen := range ips {
				if now.Sub(seen) > retention {
					delete(ips, ip)
				} else {
					alive[uid] = append(alive[uid], ip)
				}
			}
			if len(ips) == 0 {
				delete(c.clusterAlive, uid)
			}
		}
		var status *sysstatus.Status
		if value, err := sysstatus.Read(); err == nil {
			status = &value
		}
		for _, u := range traffic {
			v := report.Traffic[u.UID]
			v[0] += u.Upload
			v[1] += u.Download
			report.Traffic[u.UID] = v
		}
		report.Alive, report.Status = alive, status
		c.pendingClusterReport = report
	}
	if err := c.apiClient.ReportCluster(c.pendingClusterReport); err != nil {
		log.WithError(err).WithField("tag", c.tag).Warn("Cluster report failed; retained for retry")
		return nil
	}
	c.pendingClusterReport = nil
	return nil
}
