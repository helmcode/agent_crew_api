package scheduler

import (
	"testing"
	"time"
)

func TestParseCronFields(t *testing.T) {
	tests := []struct {
		expr string
		want int // expected number of fields, 0 means nil
	}{
		{"0 9 * * *", 5},
		{"*/5 * * * *", 5},
		{"0 0 1 1 *", 5},
		{"* * *", 0},    // too few
		{"* * * * * *", 0}, // too many
		{"", 0},
		{"single", 0},
	}
	for _, tc := range tests {
		fields := ParseCronFields(tc.expr)
		if tc.want == 0 {
			if fields != nil {
				t.Errorf("ParseCronFields(%q) = %v, want nil", tc.expr, fields)
			}
		} else if len(fields) != tc.want {
			t.Errorf("ParseCronFields(%q) got %d fields, want %d", tc.expr, len(fields), tc.want)
		}
	}
}

func TestCronMatchesTime(t *testing.T) {
	// Monday, Jan 6, 2025 at 09:30
	tm := time.Date(2025, 1, 6, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		expr string
		want bool
	}{
		{"30 9 * * *", true},           // exact minute+hour
		{"* * * * *", true},            // every minute
		{"30 9 6 1 *", true},           // exact day+month
		{"30 9 * * 1", true},           // Monday
		{"0 9 * * *", false},           // wrong minute
		{"30 10 * * *", false},         // wrong hour
		{"30 9 7 1 *", false},          // wrong day
		{"30 9 * 2 *", false},          // wrong month
		{"30 9 * * 0", false},          // Sunday, not Monday
		{"*/10 * * * *", true},         // 30 % 10 == 0
		{"*/7 * * * *", false},         // 30 % 7 != 0
		{"25-35 * * * *", true},        // 30 in range 25-35
		{"31-35 * * * *", false},       // 30 not in range
		{"0,15,30,45 * * * *", true},   // 30 in list
		{"0,15,45 * * * *", false},     // 30 not in list
		{"0-30/15 * * * *", true},      // 30 in range with step: 0,15,30
		{"0-30/15 10 * * *", false},    // right minute but wrong hour
	}

	for _, tc := range tests {
		fields := ParseCronFields(tc.expr)
		if fields == nil {
			t.Errorf("failed to parse %q", tc.expr)
			continue
		}
		got := CronMatchesTime(fields, tm)
		if got != tc.want {
			t.Errorf("CronMatchesTime(%q, %v) = %v, want %v", tc.expr, tm, got, tc.want)
		}
	}
}

func TestFieldMatches(t *testing.T) {
	tests := []struct {
		field string
		value int
		min   int
		max   int
		want  bool
	}{
		{"*", 0, 0, 59, true},
		{"*", 59, 0, 59, true},
		{"5", 5, 0, 59, true},
		{"5", 4, 0, 59, false},
		{"1,3,5", 3, 0, 59, true},
		{"1,3,5", 2, 0, 59, false},
		{"1-5", 1, 0, 59, true},
		{"1-5", 5, 0, 59, true},
		{"1-5", 6, 0, 59, false},
		{"*/15", 0, 0, 59, true},
		{"*/15", 15, 0, 59, true},
		{"*/15", 30, 0, 59, true},
		{"*/15", 45, 0, 59, true},
		{"*/15", 10, 0, 59, false},
		{"10-20/5", 10, 0, 59, true},
		{"10-20/5", 15, 0, 59, true},
		{"10-20/5", 20, 0, 59, true},
		{"10-20/5", 12, 0, 59, false},
		{"10-20/5", 25, 0, 59, false},
	}

	for _, tc := range tests {
		got := fieldMatches(tc.field, tc.value, tc.min, tc.max)
		if got != tc.want {
			t.Errorf("fieldMatches(%q, %d, %d, %d) = %v, want %v",
				tc.field, tc.value, tc.min, tc.max, got, tc.want)
		}
	}
}

func TestIsDue(t *testing.T) {
	// Create a specific time: Tuesday Jan 7 2025 at 10:00 UTC.
	tm := time.Date(2025, 1, 7, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		cron string
		tz   string
		want bool
	}{
		{"0 10 * * *", "UTC", true},
		{"0 10 * * 2", "UTC", true},     // Tuesday
		{"0 10 * * 1", "UTC", false},    // Monday
		{"0 11 * * *", "UTC", false},    // wrong hour
		{"* * * * *", "UTC", true},      // every minute
		{"0 10 7 * *", "UTC", true},     // 7th of month
		{"0 10 8 * *", "UTC", false},    // 8th of month
		// Timezone conversion: 10:00 UTC = 05:00 America/New_York (EST)
		{"0 5 * * *", "America/New_York", true},
		{"0 10 * * *", "America/New_York", false},
		// Invalid inputs.
		{"0 10 * * *", "Invalid/Zone", false},
		{"bad cron", "UTC", false},
	}

	for _, tc := range tests {
		got := IsDue(tc.cron, tc.tz, tm)
		if got != tc.want {
			t.Errorf("IsDue(%q, %q, %v) = %v, want %v", tc.cron, tc.tz, tm, got, tc.want)
		}
	}
}

func TestNextRun(t *testing.T) {
	// NextRun uses time.Now() so we test NextRunFrom directly.
	from := time.Date(2025, 1, 7, 10, 0, 0, 0, time.UTC) // Tue 10:00

	tests := []struct {
		cron string
		want time.Time
	}{
		// Next minute that matches "*/5" after 10:00 is 10:05.
		{"*/5 * * * *", time.Date(2025, 1, 7, 10, 5, 0, 0, time.UTC)},
		// Next occurrence of "0 9 * * *" after Tue 10:00 is Wed 09:00.
		{"0 9 * * *", time.Date(2025, 1, 8, 9, 0, 0, 0, time.UTC)},
		// Next occurrence of "30 10 * * *" is today at 10:30.
		{"30 10 * * *", time.Date(2025, 1, 7, 10, 30, 0, 0, time.UTC)},
		// Every minute: next is 10:01.
		{"* * * * *", time.Date(2025, 1, 7, 10, 1, 0, 0, time.UTC)},
	}

	for _, tc := range tests {
		got := NextRunFrom(tc.cron, from)
		if !got.Equal(tc.want) {
			t.Errorf("NextRunFrom(%q, %v) = %v, want %v", tc.cron, from, got, tc.want)
		}
	}

	// Invalid cron returns zero time.
	got := NextRunFrom("bad", time.Now())
	if !got.IsZero() {
		t.Errorf("NextRunFrom(bad) = %v, want zero", got)
	}
}

func TestNextRun_WithTimezone(t *testing.T) {
	// NextRun with a specific timezone.
	result := NextRun("0 0 * * *", "America/New_York")

	if result.IsZero() {
		t.Fatal("NextRun returned zero time for valid cron+timezone")
	}
	// The result should be within the next 25 hours.
	if result.After(time.Now().Add(25 * time.Hour)) {
		t.Errorf("NextRun result too far in the future: %v", result)
	}
}

func TestNextRun_InvalidTimezone(t *testing.T) {
	result := NextRun("0 * * * *", "Invalid/TZ")
	if !result.IsZero() {
		t.Errorf("expected zero time for invalid timezone, got %v", result)
	}
}
