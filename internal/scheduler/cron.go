// Package scheduler provides a cron-based scheduler that periodically checks
// for due schedules and executes them.
package scheduler

import (
	"strconv"
	"time"
)

// ParseCronFields splits a 5-field cron expression into its components.
// Returns nil if the expression does not have exactly 5 fields.
func ParseCronFields(expr string) []string {
	var fields []string
	field := ""
	for _, c := range expr {
		if c == ' ' || c == '\t' {
			if field != "" {
				fields = append(fields, field)
				field = ""
			}
		} else {
			field += string(c)
		}
	}
	if field != "" {
		fields = append(fields, field)
	}
	if len(fields) != 5 {
		return nil
	}
	return fields
}

// NextRun calculates the next execution time for a cron expression in the given timezone.
// It searches up to 48 hours from now. Returns the zero time if no match is found.
func NextRun(cronExpr, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Time{}
	}
	return NextRunFrom(cronExpr, time.Now().In(loc))
}

// NextRunFrom calculates the next execution time for a cron expression starting
// from the given time (which should already be in the desired timezone).
func NextRunFrom(cronExpr string, from time.Time) time.Time {
	fields := ParseCronFields(cronExpr)
	if fields == nil {
		return time.Time{}
	}

	candidate := from.Truncate(time.Minute).Add(time.Minute)
	limit := candidate.Add(48 * time.Hour)
	for candidate.Before(limit) {
		if CronMatchesTime(fields, candidate) {
			return candidate
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}
}

// IsDue checks whether a cron expression matches the current time in the given timezone.
// It truncates now to the nearest minute for comparison.
func IsDue(cronExpr, tz string, now time.Time) bool {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false
	}

	fields := ParseCronFields(cronExpr)
	if fields == nil {
		return false
	}

	localNow := now.In(loc).Truncate(time.Minute)
	return CronMatchesTime(fields, localNow)
}

// CronMatchesTime checks if a time matches a 5-field cron expression (minute hour day month weekday).
func CronMatchesTime(fields []string, t time.Time) bool {
	return fieldMatches(fields[0], t.Minute(), 0, 59) &&
		fieldMatches(fields[1], t.Hour(), 0, 23) &&
		fieldMatches(fields[2], t.Day(), 1, 31) &&
		fieldMatches(fields[3], int(t.Month()), 1, 12) &&
		fieldMatches(fields[4], int(t.Weekday()), 0, 6)
}

// fieldMatches checks if a value matches a single cron field.
// Supports: * (any), exact number, comma-separated values, ranges (e.g. 1-5), and steps (e.g. */5).
func fieldMatches(field string, value, min, max int) bool {
	if field == "*" {
		return true
	}

	for _, part := range splitByComma(field) {
		// Handle step: */N or range/N.
		if idx := indexByte(part, '/'); idx >= 0 {
			base := part[:idx]
			stepStr := part[idx+1:]
			step, err := strconv.Atoi(stepStr)
			if err != nil || step <= 0 {
				continue
			}
			if base == "*" {
				if (value-min)%step == 0 {
					return true
				}
			} else if rangeIdx := indexByte(base, '-'); rangeIdx >= 0 {
				lo, err1 := strconv.Atoi(base[:rangeIdx])
				hi, err2 := strconv.Atoi(base[rangeIdx+1:])
				if err1 != nil || err2 != nil {
					continue
				}
				if value >= lo && value <= hi && (value-lo)%step == 0 {
					return true
				}
			}
			continue
		}

		// Handle range: N-M.
		if rangeIdx := indexByte(part, '-'); rangeIdx >= 0 {
			lo, err1 := strconv.Atoi(part[:rangeIdx])
			hi, err2 := strconv.Atoi(part[rangeIdx+1:])
			if err1 != nil || err2 != nil {
				continue
			}
			if value >= lo && value <= hi {
				return true
			}
			continue
		}

		// Exact match.
		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		if n == value {
			return true
		}
	}

	return false
}

func splitByComma(s string) []string {
	var parts []string
	part := ""
	for _, c := range s {
		if c == ',' {
			if part != "" {
				parts = append(parts, part)
			}
			part = ""
		} else {
			part += string(c)
		}
	}
	if part != "" {
		parts = append(parts, part)
	}
	return parts
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
