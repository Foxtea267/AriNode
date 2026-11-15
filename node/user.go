package node

import (
	"sort"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/sysstatus"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) reportUserTrafficTask() (err error) {
	// The scheduler serializes normal invocations. Also protect an explicit
	// overlapping invocation from sending/clearing the same pending snapshot.
	c.userReportMu.Lock()
	defer c.userReportMu.Unlock()
	if c.apiClient.Cluster != nil {
		return c.reportClusterTask()
	}
	if status, statusErr := sysstatus.Read(); statusErr == nil {
		if reportErr := c.apiClient.ReportStatus(status); reportErr != nil {
			log.WithError(reportErr).WithField("tag", c.tag).Warn("Report host status failed")
		}
		if c.machinePrimary.Load() {
			if reportErr := c.apiClient.ReportMachineStatus(status); reportErr != nil {
				log.WithError(reportErr).WithField("tag", c.tag).Warn("Report Xboard machine status failed")
			}
		}
	}
	userTraffic, readErr := c.server.GetUserTrafficSlice(c.tag, true)
	if readErr != nil {
		log.WithError(readErr).WithField("tag", c.tag).Warn("Read user traffic failed; retained pending report")
		userTraffic = nil
	}
	if len(userTraffic) > 0 && c.pendingUserTraffic == nil {
		c.pendingUserTraffic = make(map[int]panel.UserTraffic)
	}
	// Merge only fresh bytes. Replaying a pending request must never add its
	// own bytes again or count them as new online/dynamic-limit activity.
	for _, traffic := range userTraffic {
		pending := c.pendingUserTraffic[traffic.UID]
		pending.UID = traffic.UID
		pending.Upload += traffic.Upload
		pending.Download += traffic.Download
		c.pendingUserTraffic[traffic.UID] = pending
	}
	if err := c.sendPendingUserTraffic(); err != nil {
		entry := log.WithError(err).WithField("tag", c.tag)
		if c.userTrafficReport != nil {
			entry = entry.WithField("report_id", c.userTrafficReport.ReportID)
		}
		entry.Warn("Report user traffic failed; retained for retry")
	}

	if err := c.reportOnlineDevices(userTraffic); err != nil {
		log.WithError(err).WithField("tag", c.tag).Info("Report online users failed")
	}

	userTraffic = nil
	return nil
}

func (c *Controller) sendPendingUserTraffic() error {
	// A failed keyed upload may already have been queued by the panel.
	// Never downgrade it to the additive native endpoint.
	if c.userTrafficReport == nil {
		if len(c.pendingUserTraffic) == 0 {
			return nil
		}
		if !c.userTrafficLegacyPending && !c.userTrafficReceiptSupport && !time.Now().Before(c.userTrafficLegacyUntil) {
			ok, err := c.apiClient.TrafficReceipts()
			if err != nil {
				return err
			}
			c.userTrafficReceiptSupport = ok
			c.userTrafficLegacyUntil = time.Now().Add(5 * time.Minute)
		}
		rows := make([]panel.UserTraffic, 0, len(c.pendingUserTraffic))
		for _, traffic := range c.pendingUserTraffic {
			rows = append(rows, traffic)
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].UID < rows[j].UID })
		if c.userTrafficLegacyPending || !c.userTrafficReceiptSupport {
			// Pre-existing compatibility path: the stock endpoint does
			// not provide a way to distinguish lost ACK from a failed write.
			c.userTrafficLegacyPending = true
			if err := c.apiClient.ReportUserTraffic(rows); err != nil {
				return err
			}
			c.userTrafficLegacyPending = false
			c.pendingUserTraffic = nil
			log.WithField("tag", c.tag).Infof("Report %d users traffic (native)", len(rows))
			return nil
		}
		rows = rows[:min(len(rows), 100000)]
		report, err := panel.NewTrafficReport(rows)
		if err != nil {
			return err
		}
		c.userTrafficReport = report
		for _, row := range rows {
			delete(c.pendingUserTraffic, row.UID)
		}
	}
	report := c.userTrafficReport
	if err := c.apiClient.ReportTrafficWithReceipt(report); err != nil {
		return err
	}
	c.userTrafficReport = nil
	log.WithField("tag", c.tag).Infof("Report %d users traffic (acknowledged)", len(report.Traffic))
	return nil
}

func (c *Controller) reportOnlineDevices(userTraffic []panel.UserTraffic) error {
	onlineDevice, err := c.limiter.GetOnlineDevice()
	if err != nil {
		return err
	}
	// Preserve the configured traffic threshold used to exclude probe traffic.
	var result []panel.OnlineUser
	var nocountUID = make(map[int]struct{})
	for _, traffic := range userTraffic {
		total := traffic.Upload + traffic.Download
		if total < int64(c.Options.DeviceOnlineMinTraffic*1000) {
			nocountUID[traffic.UID] = struct{}{}
		}
	}
	for _, online := range *onlineDevice {
		if _, ok := nocountUID[online.UID]; !ok {
			result = append(result, online)
		}
	}
	data := make(map[int][]string)
	for _, onlineuser := range result {
		// json structure: { UID1:["ip1","ip2"],UID2:["ip3","ip4"] }
		data[onlineuser.UID] = append(data[onlineuser.UID], onlineuser.IP)
	}
	uncertain := make(map[int]bool)
	if c.Options.Core == "hysteria2" {
		// The standalone HY2 API has UID-level traffic but no stable connection
		// identity across migration. Do not invent source-IP activity or declare
		// an authorized user offline while its traffic is still flowing.
		allowed := c.limiter.AllowedUserIDs()
		for _, traffic := range userTraffic {
			if traffic.Upload+traffic.Download > 0 && allowed[traffic.UID] && c.reportedOnline[traffic.UID] && len(data[traffic.UID]) == 0 {
				uncertain[traffic.UID] = true
			}
		}
	}
	// XBoard replaces only the users present in /alive. Omitting a previously
	// reported UID leaves its old IPs and online_count stale until another update.
	for uid := range c.reportedOnline {
		if _, exists := data[uid]; !exists && !uncertain[uid] {
			data[uid] = []string{}
		}
	}
	if len(data) == 0 {
		return nil
	}
	if c.reportedOnline == nil {
		c.reportedOnline = make(map[int]bool)
	}
	// A lost acknowledgement may still have committed on the panel. Remember
	// every attempted UID so a later empty snapshot clears it and can be retried.
	for uid := range data {
		c.reportedOnline[uid] = true
	}
	if err := c.apiClient.ReportNodeOnlineUsers(&data); err != nil {
		return err
	}
	c.reportedOnline = uncertain
	for uid, ips := range data {
		if len(ips) > 0 {
			c.reportedOnline[uid] = true
		}
	}
	activeUsers, clearedUsers := 0, 0
	for _, ips := range data {
		if len(ips) > 0 {
			activeUsers++
		} else {
			clearedUsers++
		}
	}
	log.WithFields(log.Fields{"tag": c.tag, "online_users": activeUsers, "device_ips": len(result), "cleared_users": clearedUsers}).Info("Report online devices")
	return nil
}

func compareUserList(old, new []panel.UserInfo) (deleted, added []panel.UserInfo) {
	oldMap := make(map[panel.UserInfo]int)
	for i, user := range old {
		oldMap[user] = i
	}

	for _, user := range new {
		if _, exists := oldMap[user]; !exists {
			added = append(added, user)
		} else {
			delete(oldMap, user)
		}
	}

	for _, index := range oldMap {
		deleted = append(deleted, old[index])
	}

	return deleted, added
}
