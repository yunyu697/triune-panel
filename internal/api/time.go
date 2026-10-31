package api

import (
	"time"
)

func timeNow() time.Time { return time.Now() }

func expireUnix(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}
