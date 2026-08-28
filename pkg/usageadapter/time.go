package usageadapter

import (
	"strings"
	"time"
)

func parseProviderTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, &time.ParseError{Value: value, Layout: time.RFC3339}
}
