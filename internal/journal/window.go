package journal

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func ParseWindow(sinceText, untilText string, now time.Time) (time.Time, time.Time, error) {
	loc := now.Location()
	today := dateStart(now)
	since, err := parseSince(sinceText, today)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	until, err := parseUntil(untilText, today, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !since.Before(until) {
		return time.Time{}, time.Time{}, fmt.Errorf("journal window starts at or after it ends")
	}
	return since, until, nil
}

func parseSince(text string, today time.Time) (time.Time, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	switch text {
	case "", "today":
		return today, nil
	case "yesterday":
		return today.AddDate(0, 0, -1), nil
	}
	if d, err := time.ParseInLocation("2006-01-02", text, today.Location()); err == nil {
		return d, nil
	}
	n, unit, ok := relative(text)
	if !ok {
		return time.Time{}, fmt.Errorf("cannot parse --since %q: want today, yesterday, 7d, 4w, 3mo, 1y or YYYY-MM-DD", text)
	}
	switch unit {
	case "d":
		return today.AddDate(0, 0, -(n - 1)), nil
	case "w":
		return today.AddDate(0, 0, -(n*7 - 1)), nil
	case "mo":
		return today.AddDate(0, -n, 0), nil
	case "y":
		return today.AddDate(-n, 0, 0), nil
	}
	return time.Time{}, fmt.Errorf("unknown --since unit %q", unit)
}

func parseUntil(text string, today time.Time, loc *time.Location) (time.Time, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	switch text {
	case "", "today":
		return today.AddDate(0, 0, 1), nil
	case "yesterday":
		return today, nil
	}
	if d, err := time.ParseInLocation("2006-01-02", text, loc); err == nil {
		return d.AddDate(0, 0, 1), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse --until %q: want today, yesterday or YYYY-MM-DD", text)
}

func relative(text string) (int, string, bool) {
	for _, unit := range []string{"mo", "d", "w", "y"} {
		if strings.HasSuffix(text, unit) {
			n, err := strconv.Atoi(strings.TrimSuffix(text, unit))
			return n, unit, err == nil && n > 0
		}
	}
	return 0, "", false
}

func dateStart(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
