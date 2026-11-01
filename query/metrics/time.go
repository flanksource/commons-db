package metrics

import "time"

func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00") }
