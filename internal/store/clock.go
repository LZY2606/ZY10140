package store

import "time"

func tsNow() string { return ts(time.Now().UTC()) }
func timeTS(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}
