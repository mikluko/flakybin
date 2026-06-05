package main

import (
	"net/url"
	"testing"
	"time"
)

func mustParse(t *testing.T, mode Mode, q string) Schedule {
	t.Helper()
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatalf("bad query %q: %v", q, err)
	}
	s, herr := parseSchedule(mode, v)
	if herr != nil {
		t.Fatalf("parseSchedule(%q): %s", q, herr.msg)
	}
	return s
}

// TestDeterministic verifies the same schedule yields identical windows
// regardless of when it is evaluated, for a given absolute period.
func TestDeterministic(t *testing.T) {
	for _, mode := range []Mode{Noise, Jitter} {
		s := mustParse(t, mode, "period=1h&duration=5m&count=3&seed=7")
		n := int64(12345)
		a := s.windowsForPeriod(n)
		b := s.windowsForPeriod(n)
		if len(a) != 3 || len(b) != 3 {
			t.Fatalf("%s: want 3 windows, got %d/%d", mode, len(a), len(b))
		}
		for i := range a {
			if !a[i].Start.Equal(b[i].Start) || !a[i].End.Equal(b[i].End) {
				t.Fatalf("%s: window %d not stable: %v vs %v", mode, i, a[i], b[i])
			}
		}
	}
}

// TestNoiseAlignment checks noise windows fall on duration-sized grid
// boundaries and are exactly one duration long.
func TestNoiseAlignment(t *testing.T) {
	s := mustParse(t, Noise, "period=1h&duration=5m&count=4&seed=1")
	if s.Slots() != 12 {
		t.Fatalf("want 12 slots, got %d", s.Slots())
	}
	for _, w := range s.windowsForPeriod(0) {
		if w.End.Sub(w.Start) != 5*time.Minute {
			t.Fatalf("window length %s != 5m", w.End.Sub(w.Start))
		}
		if w.Start.UnixNano()%int64(5*time.Minute) != 0 {
			t.Fatalf("window start %v not aligned to 5m grid", w.Start)
		}
	}
}

// TestSeedChangesPlacement ensures a different seed reshuffles outages.
func TestSeedChangesPlacement(t *testing.T) {
	a := mustParse(t, Noise, "period=1h&duration=5m&count=3&seed=1").windowsForPeriod(0)
	b := mustParse(t, Noise, "period=1h&duration=5m&count=3&seed=2").windowsForPeriod(0)
	same := true
	for i := range a {
		if !a[i].Start.Equal(b[i].Start) {
			same = false
		}
	}
	if same {
		t.Fatal("different seeds produced identical placement")
	}
}

// TestActiveWindow confirms ActiveWindow agrees with windowsForPeriod.
func TestActiveWindow(t *testing.T) {
	s := mustParse(t, Noise, "period=1h&duration=5m&count=2&seed=3")
	win := s.windowsForPeriod(0)[0]
	mid := win.Start.Add(2 * time.Minute)
	got, ok := s.ActiveWindow(mid)
	if !ok || !got.Start.Equal(win.Start) {
		t.Fatalf("expected active window %v at %v, got %v ok=%v", win, mid, got, ok)
	}
	before := win.Start.Add(-time.Nanosecond)
	if _, ok := s.ActiveWindow(before); ok {
		t.Fatalf("did not expect outage just before window start")
	}
}

func TestUptimeResolution(t *testing.T) {
	// 1h / 5m = 12 slots. 50% uptime => 6 outage slots.
	s := mustParse(t, Noise, "period=1h&duration=5m&uptime=50")
	if s.Count != 6 {
		t.Fatalf("uptime=50 -> want count 6, got %d", s.Count)
	}
	if up := s.UptimePct(); up < 49.9 || up > 50.1 {
		t.Fatalf("UptimePct = %v, want ~50", up)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		mode Mode
		q    string
	}{
		{Noise, "period=1h&duration=7m&count=1"},           // not divisible
		{Noise, "period=1h&duration=5m&count=1&uptime=99"}, // both (mutually exclusive)
		{Noise, "period=1h&duration=5m&count=99"},          // count > slots
		{Noise, "period=1h&duration=5m&uptime=100"},        // no outage room
		{Jitter, "period=5m&duration=10m&count=1"},         // duration > period
	}
	for _, c := range cases {
		v, _ := url.ParseQuery(c.q)
		if _, herr := parseSchedule(c.mode, v); herr == nil {
			t.Errorf("expected error for %s %q", c.mode, c.q)
		}
	}
}

// TestDefaults verifies omitted parameters fall back to the explorer defaults
// (period 24h, duration 15m, uptime 90), so a bare endpoint URL still resolves.
func TestDefaults(t *testing.T) {
	cases := []struct {
		mode Mode
		q    string
	}{
		{Jitter, ""},                     // everything defaulted
		{Jitter, "period=1h"},            // duration + uptime defaulted
		{Noise, "duration=5m"},           // period + uptime defaulted
		{Noise, "period=1h&duration=5m"}, // neither count nor uptime
		{Jitter, "seed=7"},               // only seed
	}
	for _, c := range cases {
		v, _ := url.ParseQuery(c.q)
		s, herr := parseSchedule(c.mode, v)
		if herr != nil {
			t.Fatalf("%s %q: unexpected error %s", c.mode, c.q, herr.msg)
		}
		if !v.Has("period") && s.Period != defaultPeriod {
			t.Errorf("%q: period = %s, want default %s", c.q, s.Period, defaultPeriod)
		}
		if !v.Has("duration") && s.Duration != defaultDuration {
			t.Errorf("%q: duration = %s, want default %s", c.q, s.Duration, defaultDuration)
		}
		if s.Count < 1 {
			t.Errorf("%q: count %d < 1", c.q, s.Count)
		}
		if up := s.UptimePct(); up <= 0 || up >= 100 {
			t.Errorf("%q: realized uptime %.4g out of (0,100)", c.q, up)
		}
	}
}

func TestParseModeRejectsUnknown(t *testing.T) {
	if _, herr := parseMode("bogus"); herr == nil {
		t.Fatal("expected error for unknown mode")
	}
}
