package limiter

import (
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
)

func (l *Limiter) AddDynamicSpeedLimit(tag string, userInfo *panel.UserInfo, limitNum int, expire int64) error {
	return l.UpdateDynamicSpeedLimit(tag, userInfo.Uuid, limitNum, time.Now().Add(time.Duration(expire)*time.Second))
}

// determineSpeedLimit returns the minimum non-zero rate
func determineSpeedLimit(limit1, limit2 int) (limit int) {
	if limit1 == 0 || limit2 == 0 {
		if limit1 > limit2 {
			return limit1
		} else if limit1 < limit2 {
			return limit2
		} else {
			return 0
		}
	} else {
		if limit1 > limit2 {
			return limit2
		} else if limit1 < limit2 {
			return limit1
		} else {
			return limit1
		}
	}
}
