package config

import (
	"os"
	"testing"
)

// TestEOTimerIntervalReportsAnUnreadableValue is the covering test for the
// setting that used to fall silent.
//
// Every other numeric setting goes through getEnvAsInt, which discards
// strconv's error and returns the default. For EO_TIMER_INTERVAL_SECONDS that
// meant a typo arrived at main.eoTimerInterval as an ordinary positive 30: the
// "non-positive value" warning could not fire, and nothing anywhere named the
// bad string. The fallback is still 30 — that part is right — but it is now
// reported, so the operator finds out at startup rather than wondering why the
// interval they set is not in effect.
//
// The single-line mutation that makes this fail: in Load, go back to
// `TimerIntervalSeconds: getEnvAsInt("EO_TIMER_INTERVAL_SECONDS", 30)` and
// drop TimerIntervalUnparsed — the "not a number" case then reports nothing.
func TestEOTimerIntervalReportsAnUnreadableValue(t *testing.T) {
	cases := []struct {
		name         string
		set          bool
		raw          string
		wantSeconds  int
		wantUnparsed string
	}{
		{name: "unset falls back silently", set: false, wantSeconds: 30, wantUnparsed: ""},
		{name: "a valid value is used", set: true, raw: "5", wantSeconds: 5, wantUnparsed: ""},
		{name: "surrounding space is tolerated", set: true, raw: "  45 ", wantSeconds: 45, wantUnparsed: ""},
		// The case the old code lost: a letter O for a zero.
		{name: "a typo is reported, not swallowed", set: true, raw: "3O", wantSeconds: 30, wantUnparsed: "3O"},
		// Present but blank is the same intent as unset: `KEY=` in a
		// .env file means "not configured", so it is not a mistake.
		{name: "a blank value is treated as unset", set: true, raw: "   ", wantSeconds: 30, wantUnparsed: ""},
		{name: "a word is reported", set: true, raw: "thirty", wantSeconds: 30, wantUnparsed: "thirty"},
		// Readable but unusable. NOT reported as unparsed: the use site
		// refuses it by name, and reporting it here would make the two
		// warnings contradict each other.
		{name: "zero is readable and left to the use site", set: true, raw: "0", wantSeconds: 0, wantUnparsed: ""},
		{name: "a negative value is readable too", set: true, raw: "-5", wantSeconds: -5, wantUnparsed: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("EO_TIMER_INTERVAL_SECONDS", tc.raw)
			} else {
				// t.Setenv registers the restore; os.Unsetenv then makes the
				// variable genuinely absent, which is a different state from
				// present-and-empty and is what this case is about.
				t.Setenv("EO_TIMER_INTERVAL_SECONDS", "")
				if err := os.Unsetenv("EO_TIMER_INTERVAL_SECONDS"); err != nil {
					t.Fatalf("unset: %v", err)
				}
			}
			seconds, unparsed := eoTimerIntervalFromEnv()
			if seconds != tc.wantSeconds {
				t.Errorf("seconds = %d, want %d", seconds, tc.wantSeconds)
			}
			if unparsed != tc.wantUnparsed {
				t.Errorf("unparsed = %q, want %q", unparsed, tc.wantUnparsed)
			}

			// And it reaches the config the server actually reads.
			cfg := Load()
			if cfg.EOPlatform.TimerIntervalSeconds != tc.wantSeconds {
				t.Errorf("cfg.EOPlatform.TimerIntervalSeconds = %d, want %d",
					cfg.EOPlatform.TimerIntervalSeconds, tc.wantSeconds)
			}
			if cfg.EOPlatform.TimerIntervalUnparsed != tc.wantUnparsed {
				t.Errorf("cfg.EOPlatform.TimerIntervalUnparsed = %q, want %q",
					cfg.EOPlatform.TimerIntervalUnparsed, tc.wantUnparsed)
			}
		})
	}
}

// TestEOTimerIntervalDefaultIsShared keeps the default in one place: config
// owns it and main.eoTimerInterval renders it as a Duration, so the two cannot
// state different numbers.
func TestEOTimerIntervalDefaultIsShared(t *testing.T) {
	if EOTimerIntervalDefaultSeconds != 30 {
		t.Errorf("EOTimerIntervalDefaultSeconds = %d, want 30", EOTimerIntervalDefaultSeconds)
	}
}
