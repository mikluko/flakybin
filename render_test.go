package main

import (
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBuildNavLinksValidate locks in the central refactor invariant: every
// href buildNav emits parses and validates on its target page, and each row
// highlights exactly one current chip.
func TestBuildNavLinksValidate(t *testing.T) {
	cases := []struct {
		mode   Mode
		q      string
		target string
	}{
		{Noise, "period=24h&duration=5m&uptime=99.9", "99.9"},
		{Noise, "period=1h&duration=5m&uptime=50", "50"},
		{Jitter, "period=24h&duration=2m&count=3", "95"},
		{Jitter, "period=1h&duration=7m&uptime=90", "90"}, // indivisible (jitter only)
		{Noise, "period=12m&duration=1m&count=2&seed=12345", "83.3333"},
	}
	for _, c := range cases {
		v, _ := url.ParseQuery(c.q)
		s, herr := parseSchedule(c.mode, v)
		if herr != nil {
			t.Fatalf("seed schedule %q: %s", c.q, herr.msg)
		}
		nav := buildNav(s, c.target)

		check := func(label, href string) {
			u, err := url.Parse(href)
			if err != nil {
				t.Fatalf("%s: unparseable href %q: %v", label, href, err)
			}
			segs := strings.Split(strings.Trim(u.Path, "/"), "/")
			mode, merr := parseMode(segs[0])
			if merr != nil {
				t.Errorf("%s: href %q bad mode: %s", label, href, merr.msg)
				return
			}
			if _, herr := parseSchedule(mode, u.Query()); herr != nil {
				t.Errorf("%s: href %q -> 400: %s", label, href, herr.msg)
			}
		}

		for _, row := range nav.Rows {
			cur := 0
			for _, chip := range row.Chips {
				check(c.q+" row="+row.Label, chip.Href)
				if chip.Current {
					cur++
				}
			}
			if cur != 1 {
				t.Errorf("%q row %q: want exactly 1 current chip, got %d", c.q, row.Label, cur)
			}
			// The uptime row must vary only uptime, holding the other axes fixed.
			if row.Label == "uptime" {
				for _, chip := range row.Chips {
					q := mustQuery(t, chip.Href)
					if q.Get("period") != prettyDur(s.Period) || q.Get("duration") != prettyDur(s.Duration) || q.Get("seed") != strconv.FormatUint(s.Seed, 10) {
						t.Errorf("uptime chip %q changed a fixed axis", chip.Href)
					}
				}
			}
		}
		for _, row := range nav.Fail {
			for _, chip := range row.Chips {
				check(c.q+" fail:"+row.Label, chip.Href)
			}
		}
	}
}

func TestModeChipOmitsDeadNoiseLink(t *testing.T) {
	// Jitter page with indivisible period/duration: the noise chip would 400,
	// so it must be omitted.
	v, _ := url.ParseQuery("period=1h&duration=7m&uptime=90")
	s, herr := parseSchedule(Jitter, v)
	if herr != nil {
		t.Fatal(herr.msg)
	}
	nav := buildNav(s, "90")
	for _, chip := range nav.Rows[0].Chips { // mode row
		if chip.Label == "noise" {
			t.Fatalf("noise mode chip should be omitted for indivisible jitter shape")
		}
	}
}

func TestUptimeCount(t *testing.T) {
	cases := []struct {
		mode Mode
		q    string
		want int // -1 means expect a 400
	}{
		{Noise, "period=1h&duration=5m&uptime=50", 6},
		{Noise, "period=1h&duration=5m&uptime=99", 1},     // round to 0 -> floor 1
		{Noise, "period=1h&duration=5m&uptime=99.999", 1}, // sub-100, never 400
		{Noise, "period=30m&duration=5m&uptime=8.3", 5},   // saturating -> clamp to capacity-1
		{Noise, "period=1h&duration=5m&uptime=100", -1},   // no room -> 400
		{Jitter, "period=1h&duration=5m&uptime=50", 6},    // jitter branch
		{Jitter, "period=1h&duration=5m&uptime=99.9", 1},  // clamp to 1
		{Jitter, "period=30m&duration=5m&uptime=0.01", 5}, // saturating -> capacity-1
	}
	for _, c := range cases {
		v, _ := url.ParseQuery(c.q)
		s, herr := parseSchedule(c.mode, v)
		if c.want == -1 {
			if herr == nil {
				t.Errorf("%s %q: expected 400", c.mode, c.q)
			}
			continue
		}
		if herr != nil {
			t.Errorf("%s %q: unexpected 400: %s", c.mode, c.q, herr.msg)
			continue
		}
		if s.Count != c.want {
			t.Errorf("%s %q: count = %d, want %d", c.mode, c.q, s.Count, c.want)
		}
		if up := s.UptimePct(); up <= 0 || up >= 100 {
			t.Errorf("%s %q: realized uptime %.4g out of (0,100)", c.mode, c.q, up)
		}
	}
}

func TestUptimePresets(t *testing.T) {
	dur := func(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
	cases := []struct {
		mode   Mode
		period time.Duration
		d      time.Duration
		target string
		want   []string
	}{
		// 24h/5m jitter: 99.9% rounds to 0 outages of 5m, so it is dropped.
		{Jitter, dur("24h"), dur("5m"), "90", []string{"50", "80", "90", "95", "99"}},
		// 24h/2h jitter: cap=12, so only low-availability targets stay feasible.
		{Jitter, dur("24h"), dur("2h"), "50", []string{"50", "80", "90", "95"}},
		// non-ladder target inserted by value, always kept.
		{Jitter, dur("24h"), dur("5m"), "85", []string{"50", "80", "85", "90", "95", "99"}},
		// unparseable target ignored.
		{Jitter, dur("24h"), dur("5m"), "bogus", []string{"50", "80", "90", "95", "99"}},
		// noise 1h/5m: slots=12.
		{Noise, dur("1h"), dur("5m"), "90", []string{"50", "80", "90", "95"}},
	}
	for _, c := range cases {
		got := uptimePresets(c.mode, c.period, c.d, c.target)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("uptimePresets(%v, %v, %v, %q) = %v, want %v", c.mode, c.period, c.d, c.target, got, c.want)
		}
	}
}

// TestDurationUptimeAligned checks the coupled-ladder invariant: every duration
// and uptime chip the explorer offers yields a feasible schedule whose realized
// availability lands within one outage of the target (no clamp surprise).
func TestDurationUptimeAligned(t *testing.T) {
	cases := []struct {
		mode   Mode
		q      string
		target string
	}{
		{Jitter, "period=24h&duration=5m&uptime=90", "90"},
		{Jitter, "period=1h&duration=2m&uptime=95", "95"},
		{Noise, "period=24h&duration=5m&uptime=99", "99"},
		{Noise, "period=1h&duration=5m&uptime=80", "80"},
		{Even, "period=24h&duration=5m&uptime=95", "95"},
		// Count-heavy load whose realized uptime (8.33%) is carried verbatim as
		// the target. Before the aligns() fix, the duration row offered chips
		// (e.g. 5m) that clamped count to capacity-1, pushing realized far off.
		{Jitter, "period=24m&duration=1m&count=22", "8.3333"},
	}
	for _, c := range cases {
		v, _ := url.ParseQuery(c.q)
		s, herr := parseSchedule(c.mode, v)
		if herr != nil {
			t.Fatalf("%s: %s", c.q, herr.msg)
		}
		nav := buildNav(s, c.target)
		for _, row := range nav.Rows {
			if row.Label != "duration" && row.Label != "uptime" {
				continue
			}
			for _, chip := range row.Chips {
				u, _ := url.Parse(chip.Href)
				segs := strings.Split(strings.Trim(u.Path, "/"), "/")
				mode, _ := parseMode(segs[0])
				ts, herr := parseSchedule(mode, u.Query())
				if herr != nil {
					t.Errorf("%s row %q chip %q -> 400: %s", c.q, row.Label, chip.Label, herr.msg)
					continue
				}
				targetU, _ := strconv.ParseFloat(u.Query().Get("uptime"), 64)
				// The offered chip must resolve a count in [1, capacity-1] with
				// neither the floor nor the clamp engaged (the clamp is invisible
				// post-hoc, so check the predicate directly).
				if !aligns(mode, ts.Period, ts.Duration, (100-targetU)/100) {
					t.Errorf("%s row %q chip %q: not aligned — count would be floored/clamped",
						c.q, row.Label, chip.Label)
				}
				realized := ts.UptimePct()
				slack := 100 * float64(ts.Duration) / float64(ts.Period) // one outage
				if diff := realized - targetU; diff > slack+0.01 || diff < -slack-0.01 {
					t.Errorf("%s row %q chip %q: realized %.3g vs target %.3g exceeds one-outage slack %.3g",
						c.q, row.Label, chip.Label, realized, targetU, slack)
				}
			}
		}
	}
}

func TestTrimFloatAndTarget(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{50, "50"},
		{50.5, "50.5"},
		{99.9, "99.9"},
		{99.99, "99.99"},
		{0.0001, "0.0001"},
		{99.99999, "100"}, // raw trimFloat rounds up
	} {
		if got := trimFloat(c.in); got != c.want {
			t.Errorf("trimFloat(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	// uptimeTarget must never produce a value that parses back to >=100, or the
	// derived explore links would 400.
	for _, in := range []float64{99.99999, 99.999996, 100, 95.5, 50} {
		got := uptimeTarget(in)
		f, err := strconv.ParseFloat(got, 64)
		if err != nil || f >= 100 {
			t.Errorf("uptimeTarget(%v) = %q, parses to %v (must be <100)", in, got, f)
		}
	}
}

func TestMergeWindows(t *testing.T) {
	w := func(a, b int) Window {
		return Window{time.Unix(int64(a)*60, 0), time.Unix(int64(b)*60, 0)}
	}
	cases := []struct {
		name string
		in   []Window
		want []Window
	}{
		{"empty", nil, nil},
		{"single", []Window{w(0, 5)}, []Window{w(0, 5)}},
		{"touching", []Window{w(0, 5), w(5, 10)}, []Window{w(0, 10)}},
		{"overlapping", []Window{w(0, 5), w(3, 8)}, []Window{w(0, 8)}},
		{"nested-no-shrink", []Window{w(0, 10), w(2, 4)}, []Window{w(0, 10)}},
		{"disjoint", []Window{w(0, 5), w(10, 15)}, []Window{w(0, 5), w(10, 15)}},
	}
	for _, c := range cases {
		got := mergeWindows(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%s: len %d, want %d", c.name, len(got), len(c.want))
			continue
		}
		for i := range got {
			if !got[i].Start.Equal(c.want[i].Start) || !got[i].End.Equal(c.want[i].End) {
				t.Errorf("%s: window %d = %v, want %v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestPeriodPresets(t *testing.T) {
	dur := func(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
	checks := []struct {
		mode Mode
		cur  time.Duration
		d    time.Duration
		df   float64
	}{
		{Noise, dur("1h"), dur("5m"), 0.1},
		{Noise, dur("1h"), dur("7m"), 0.1},
		{Jitter, dur("1h"), dur("7m"), 0.1},
		{Jitter, dur("24h"), dur("5m"), 0.5},
	}
	for _, c := range checks {
		got := periodPresets(c.mode, c.cur, c.d, c.df)
		if !slices.Contains(got, c.cur) {
			t.Errorf("%v %v/%v df=%g: current period missing from %v", c.mode, c.cur, c.d, c.df, got)
		}
		if !slices.IsSorted(got) {
			t.Errorf("%v %v/%v df=%g: not sorted: %v", c.mode, c.cur, c.d, c.df, got)
		}
		for _, p := range got {
			if p == c.cur {
				continue // current is force-included regardless of feasibility
			}
			if !aligns(c.mode, p, c.d, c.df) {
				t.Errorf("%v %v/%v df=%g: offered period %v does not align", c.mode, c.cur, c.d, c.df, p)
			}
			if c.mode == Noise && p%c.d != 0 {
				t.Errorf("%v %v/%v: noise period %v not divisible by duration", c.mode, c.cur, c.d, p)
			}
		}
	}
}

func TestWantsHTML(t *testing.T) {
	cases := []struct {
		format string
		accept string
		want   bool
	}{
		{"html", "*/*", true},
		{"json", "text/html", false},
		{"", "text/html,application/xhtml+xml", true},
		{"", "*/*", false},
		{"", "", false},            // curl-like
		{"xml", "text/html", true}, // unknown format falls through to Accept
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/noise/inspect?format="+c.format, nil)
		if c.accept != "" {
			r.Header.Set("Accept", c.accept)
		}
		if got := wantsHTML(r); got != c.want {
			t.Errorf("wantsHTML(format=%q accept=%q) = %v, want %v", c.format, c.accept, got, c.want)
		}
	}
}

// --- small test helpers ---

func mustQuery(t *testing.T, href string) url.Values {
	t.Helper()
	u, err := url.Parse(href)
	if err != nil {
		t.Fatalf("bad href %q: %v", href, err)
	}
	return u.Query()
}

func eqDurs(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
