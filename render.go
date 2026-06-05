package main

import (
	"crypto/rand"
	"encoding/binary"
	"html/template"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// wantsHTML decides whether to render the schedule graphically. An explicit
// ?format= wins; otherwise it honors the Accept header, so browsers get the
// timeline and scripts (curl, */*) get JSON.
func wantsHTML(r *http.Request) bool {
	switch r.URL.Query().Get("format") {
	case "html":
		return true
	case "json":
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// timeline geometry (SVG user units).
const (
	tlWidth    = 920
	tlGutter   = 150 // left label column
	tlRightPad = 20
	tlTrack    = 28 // track height
	tlGap      = 12
	tlPeriods  = 8 // periods drawn
	tlTrackW   = tlWidth - tlGutter - tlRightPad
)

// uptimeLadder is the human-facing set of availability targets offered as
// explore chips. The current target is always added on top of these.
var uptimeLadder = []string{"50", "80", "90", "95", "99", "99.9"}

var scheduleTmpl = template.Must(template.ParseFS(staticFS, "static/schedule.html"))

// --- view model -------------------------------------------------------------

type pageView struct {
	Title        string
	Status       statusView
	Nav          navView
	Timeline     timelineView
	PeriodsShown int
	JSONHref     string
	Version      string
}

type statusView struct {
	InOutage  bool
	Label     string
	UptimePct float64
	Switch    *switchView
}

type switchView struct {
	Label string // "next outage" / "recovers"
	In    string // "4m15s"
	At    string // "07:19:00 UTC"
}

type navView struct {
	Rows []navRow
	Fail []navRow
}

type navRow struct {
	Label string
	Chips []navChip
}

type navChip struct {
	Label   string
	Href    string
	Current bool
	Blank   bool // open in a new tab when followed
	Copy    bool // plain click copies the URL; modifier-click follows
}

type timelineView struct {
	Width, Height int
	Tracks        []trackView
	Earlier       string // href: scroll one window into the past
	Later         string // href: scroll one window into the future
	NowHref       string // href: jump back to the current period
	AtNow         bool   // true when the window starts at the current period
}

type trackView struct {
	Label   string
	LabelY  int
	Up      rectView
	Outages []rectView
	Now     *nowView
}

type rectView struct {
	X, W       float64
	HitX, HitW float64 // wider transparent hover target
	Y, H       int
	Title      string
}

type nowView struct {
	X      float64
	Y1, Y2 int
	LabelY int
}

// --- rendering --------------------------------------------------------------

// renderScheduleHTML builds the page view model and executes the template.
//
// reqUptime is the uptime= value from the request, if any. It is the invariant
// carried across the explore links so availability stays fixed while other
// axes change; when absent it is derived from the current schedule.
func renderScheduleHTML(w http.ResponseWriter, req request, reqUptime string, offset int) {
	s := req.sched
	target := reqUptime
	if target == "" {
		target = uptimeTarget(s.UptimePct())
	}
	page := pageView{
		Title:        s.Mode.String(),
		Status:       buildStatus(req),
		Nav:          buildNav(s, target),
		Timeline:     buildTimeline(req, target, offset),
		PeriodsShown: tlPeriods,
		JSONHref: "/" + s.Mode.String() + "/inspect?period=" + prettyDur(s.Period) +
			"&duration=" + prettyDur(s.Duration) +
			"&seed=" + strconv.FormatUint(s.Seed, 10) +
			"&uptime=" + target + "&format=json",
		Version: version,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	scheduleTmpl.Execute(w, page)
}

func buildStatus(req request) statusView {
	s := req.sched
	sv := statusView{UptimePct: s.UptimePct()}
	var switchAt time.Time
	if req.inOut {
		sv.InOutage, sv.Label = true, "in outage"
		sv.Switch = &switchView{Label: "recovers"}
		switchAt = req.active.End
	} else {
		sv.Label = "available"
		if up := s.Upcoming(req.now, 1); len(up) > 0 {
			sv.Switch = &switchView{Label: "next outage"}
			switchAt = up[0].Start
		}
	}
	if sv.Switch != nil {
		rel := time.Until(switchAt)
		if rel > time.Hour {
			rel = rel.Round(time.Minute)
		} else {
			rel = rel.Round(time.Second)
		}
		sv.Switch.In = rel.String()
		sv.Switch.At = switchAt.UTC().Format("15:04:05 MST")
	}
	return sv
}

// buildNav assembles the explore chip rows (mode, period, uptime, seed) plus
// the detached failure-endpoint row.
//
// Explore links carry the uptime TARGET verbatim; each target page derives its
// own count from it, so the target never drifts across hops. Realized
// availability still differs from the target because count is quantized by the
// outage duration and clamped to [1, capacity-1]. Emulate links instead
// reproduce the exact current schedule by count.
func buildNav(s Schedule, uptime string) navView {
	cur := s.Mode.String()

	// inspect link varying one axis while holding availability (uptime) fixed.
	inspect := func(mode string, p, d time.Duration, seed uint64, up string) string {
		return "/" + mode + "/inspect?period=" + prettyDur(p) +
			"&duration=" + prettyDur(d) +
			"&seed=" + strconv.FormatUint(seed, 10) +
			"&uptime=" + up
	}

	var nav navView

	// mode. Skip the noise chip when the current period is not divisible by the
	// duration: noise needs an even division into slots, so the link would 400.
	// (even and jitter have no such constraint.)
	modeRow := navRow{Label: "mode"}
	for _, m := range []string{"even", "jitter", "noise"} {
		if m == "noise" && s.Period%s.Duration != 0 {
			continue
		}
		modeRow.Chips = append(modeRow.Chips, navChip{
			Label:   m,
			Href:    inspect(m, s.Period, s.Duration, s.Seed, uptime),
			Current: m == cur,
		})
	}
	nav.Rows = append(nav.Rows, modeRow)

	// uptime and duration are coupled through the per-period downtime budget
	// D = (1 - uptime) * period: a single outage may not exceed D, so the
	// duration ladder is capped at D and the uptime ladder is capped at the
	// availability one current-length outage already implies. Every offered
	// (duration, uptime) pair is therefore jointly feasible (count >= 1).
	// period, duration and uptime are coupled through the outage count: a chip
	// is offered only if its (period, duration, uptime) resolves to a count in
	// [1, capacity-1] (see aligns) — at least one outage, and at least one slot
	// still up — so no offered chip is silently floored or clamped. The current
	// value on each axis is always included even if it falls outside that set.
	downFrac := downtimeFrac(uptime, s.UptimePct())

	// period
	periodRow := navRow{Label: "period"}
	for _, p := range periodPresets(s.Mode, s.Period, s.Duration, downFrac) {
		periodRow.Chips = append(periodRow.Chips, navChip{
			Label:   durLabel(p),
			Href:    inspect(cur, p, s.Duration, s.Seed, uptime),
			Current: p == s.Period,
		})
	}
	nav.Rows = append(nav.Rows, periodRow)

	// duration (outage length)
	durRow := navRow{Label: "duration"}
	for _, d := range durationPresets(s.Mode, s.Period, s.Duration, downFrac) {
		durRow.Chips = append(durRow.Chips, navChip{
			Label:   durLabel(d),
			Href:    inspect(cur, s.Period, d, s.Seed, uptime),
			Current: d == s.Duration,
		})
	}
	nav.Rows = append(nav.Rows, durRow)

	// uptime (human availability targets)
	upRow := navRow{Label: "uptime"}
	for _, u := range uptimePresets(s.Mode, s.Period, s.Duration, uptime) {
		upRow.Chips = append(upRow.Chips, navChip{
			Label:   u + "%",
			Href:    inspect(cur, s.Period, s.Duration, s.Seed, u),
			Current: u == uptime,
		})
	}
	nav.Rows = append(nav.Rows, upRow)

	// seed: fixed presets plus a "random" chip. The random chip always points
	// at a fresh value and is the active one whenever the current seed is not a
	// preset.
	seedRow := navRow{Label: "seed"}
	for _, sd := range presetSeeds {
		seedRow.Chips = append(seedRow.Chips, navChip{
			Label:   strconv.FormatUint(sd, 10),
			Href:    inspect(cur, s.Period, s.Duration, sd, uptime),
			Current: sd == s.Seed,
		})
	}
	seedRow.Chips = append(seedRow.Chips, navChip{
		Label:   "random",
		Href:    inspect(cur, s.Period, s.Duration, randomSeed(), uptime),
		Current: !isPresetSeed(s.Seed),
	})
	nav.Rows = append(nav.Rows, seedRow)

	// emulate: exact current schedule (count-based) against the live endpoints,
	// grouped by failure class. Each chip copies its URL on click.
	base := "period=" + prettyDur(s.Period) +
		"&duration=" + prettyDur(s.Duration) +
		"&seed=" + strconv.FormatUint(s.Seed, 10) +
		"&count=" + strconv.Itoa(s.Count)
	emu := func(path, extra string) string {
		u := "/" + cur + "/" + path + "?" + base
		if extra != "" {
			u += "&" + extra
		}
		return u
	}
	statusRow := func(label string, codes []statusCode) navRow {
		row := navRow{Label: label}
		for _, c := range codes {
			row.Chips = append(row.Chips, navChip{
				Label: c.code, Href: emu("status/"+c.code, c.extra), Blank: true, Copy: true,
			})
		}
		return row
	}
	nav.Fail = []navRow{
		statusRow("4xx", status4xx),
		statusRow("5xx", status5xx),
		{Label: "other", Chips: []navChip{
			{Label: "hang", Href: emu("hang", ""), Blank: true, Copy: true},
			{Label: "drop", Href: emu("drop", "after=128"), Blank: true, Copy: true},
		}},
	}
	return nav
}

// statusCode is one emulate chip: an HTTP status with optional extra query
// (e.g. retry-after for codes that carry it).
type statusCode struct{ code, extra string }

var (
	status4xx = []statusCode{
		{"400", ""}, {"401", ""}, {"403", ""}, {"404", ""},
		{"408", ""}, {"418", ""}, {"429", "retry-after=auto"},
	}
	status5xx = []statusCode{
		{"500", ""}, {"502", ""}, {"503", "retry-after=auto"},
		{"504", "retry-after=auto"}, {"507", ""},
	}
)

// buildTimeline computes the stacked-track timeline view: one track per period,
// outage windows merged and positioned proportionally, with a now marker.
//
// offset scrolls the window by whole periods (negative = past, positive =
// future); 0 starts at the current period.
func buildTimeline(req request, target string, offset int) timelineView {
	s := req.sched
	tl := timelineView{Width: tlWidth, Height: tlPeriods*(tlTrack+tlGap) + tlGap}

	scroll := func(off int) string {
		u := "/" + s.Mode.String() + "/inspect?period=" + prettyDur(s.Period) +
			"&duration=" + prettyDur(s.Duration) +
			"&seed=" + strconv.FormatUint(s.Seed, 10) +
			"&uptime=" + target
		if off != 0 {
			u += "&offset=" + strconv.Itoa(off)
		}
		return u
	}
	tl.Earlier = scroll(offset - tlPeriods)
	tl.Later = scroll(offset + tlPeriods)
	tl.NowHref = scroll(0)
	tl.AtNow = offset == 0

	nowIndex := s.periodIndex(req.now)
	base := nowIndex + int64(offset)
	for i := range tlPeriods {
		n := base + int64(i)
		periodStart := time.Unix(0, n*int64(s.Period))
		y := tlGap + i*(tlTrack+tlGap)
		t := trackView{
			Label:  periodStart.UTC().Format("Jan 02 15:04"),
			LabelY: y + tlTrack/2 + 4,
			Up:     rectView{X: tlGutter, W: tlTrackW, Y: y, H: tlTrack},
		}
		for _, win := range mergeWindows(s.windowsForPeriod(n)) {
			startFrac := float64(win.Start.Sub(periodStart)) / float64(s.Period)
			wFrac := float64(win.End.Sub(win.Start)) / float64(s.Period)
			bw := wFrac * tlTrackW
			if bw < 2 {
				bw = 2
			}
			x := tlGutter + startFrac*tlTrackW
			// Widen the hover target so thin jitter bars are easy to hit.
			hitW := bw
			if hitW < 10 {
				hitW = 10
			}
			hitX := x - (hitW-bw)/2
			if hitX < tlGutter {
				hitX = tlGutter
			}
			if hitX+hitW > tlGutter+tlTrackW {
				hitX = tlGutter + tlTrackW - hitW
			}
			t.Outages = append(t.Outages, rectView{
				X:     x,
				W:     bw,
				HitX:  hitX,
				HitW:  hitW,
				Y:     y,
				H:     tlTrack,
				Title: outageTitle(win),
			})
		}
		if n == nowIndex { // the period containing "now" — may be off-screen
			nowFrac := float64(req.now.Sub(periodStart)) / float64(s.Period)
			t.Now = &nowView{
				X:      tlGutter + nowFrac*tlTrackW,
				Y1:     y - 3,
				Y2:     y + tlTrack + 3,
				LabelY: y - 6,
			}
		}
		tl.Tracks = append(tl.Tracks, t)
	}
	return tl
}

// outageTitle is the hover text for an outage block: its date, start→end (UTC),
// and length. A block may span more than one duration when adjacent outages
// were merged, so the length is the block's own span.
func outageTitle(win Window) string {
	start := win.Start.UTC()
	end := win.End.UTC()
	endFmt := "15:04:05"
	if end.YearDay() != start.YearDay() || end.Year() != start.Year() {
		endFmt = "Jan 02 15:04:05"
	}
	return durLabel(end.Sub(start)) + " · " +
		start.Format("Jan 02 15:04:05") + " → " + end.Format(endFmt) + " UTC"
}

// --- preset ladders ---------------------------------------------------------

// mergeWindows coalesces touching or overlapping windows in a sorted slice
// into single intervals, so adjacent outage slots draw as one block.
func mergeWindows(in []Window) []Window {
	if len(in) == 0 {
		return in
	}
	out := []Window{in[0]}
	for _, w := range in[1:] {
		last := &out[len(out)-1]
		if !w.Start.After(last.End) { // touching or overlapping
			if w.End.After(last.End) {
				last.End = w.End
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

// roundPeriods and roundDurations are the human round values the explore
// ladders are built from. Each candidate is kept only if it aligns (below).
var roundPeriods = []time.Duration{
	10 * time.Minute, 30 * time.Minute, time.Hour,
	6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour,
}

var roundDurations = []time.Duration{
	time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute,
	10 * time.Minute, 15 * time.Minute, 20 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour,
	8 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// aligns reports whether (period, duration) at the given downtime fraction
// resolves to an outage count in [1, capacity-1] using the SAME arithmetic as
// resolveCount: at least one outage, at least one slot still up, and neither
// the count<1 floor nor the count>capacity-1 clamp engaged. Offered explore
// chips must align, so following any one of them changes realized availability
// by at most a rounding step (never a silent floor/clamp jump). It is the
// single feasibility predicate shared by the period, duration and uptime rows.
func aligns(mode Mode, period, dur time.Duration, downFrac float64) bool {
	if dur < time.Minute || dur > period {
		return false
	}
	var capacity, count int
	switch mode {
	case Noise:
		if period%dur != 0 {
			return false
		}
		capacity = int(period / dur)
		count = int(math.Round(float64(capacity) * downFrac))
	default: // Jitter, Even
		capacity = int(period / dur)
		count = int(math.Round(downFrac * float64(period) / float64(dur)))
	}
	return capacity >= 2 && count >= 1 && count <= capacity-1
}

func sortedDurset(set map[time.Duration]bool) []time.Duration {
	out := make([]time.Duration, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// periodPresets returns the round periods that, with the current duration and
// downtime target, yield a feasible (aligned) schedule. The current period is
// always included.
func periodPresets(mode Mode, cur, dur time.Duration, downFrac float64) []time.Duration {
	set := map[time.Duration]bool{cur: true}
	for _, p := range roundPeriods {
		if aligns(mode, p, dur, downFrac) {
			set[p] = true
		}
	}
	return sortedDurset(set)
}

// durationPresets returns the round outage lengths that, with the current
// period and downtime target, yield a feasible (aligned) schedule. In noise
// mode aligns also requires the duration to divide the period. The current
// duration is always included.
func durationPresets(mode Mode, period, cur time.Duration, downFrac float64) []time.Duration {
	set := map[time.Duration]bool{cur: true}
	for _, d := range roundDurations {
		if aligns(mode, period, d, downFrac) {
			set[d] = true
		}
	}
	return sortedDurset(set)
}

// downtimeFrac parses the carried uptime target into a downtime fraction,
// falling back to the realized value if the target is not a number.
func downtimeFrac(target string, realized float64) float64 {
	u, err := strconv.ParseFloat(target, 64)
	if err != nil {
		u = realized
	}
	f := (100 - u) / 100
	if f < 0 {
		return 0
	}
	return f
}

// uptimePresets returns the availability ladder with the current target merged
// in, ordered by numeric value. A ladder entry is kept only if it yields a
// feasible (aligned) schedule for the current period and duration; the current
// target is always kept.
func uptimePresets(mode Mode, period, dur time.Duration, target string) []string {
	seen := map[string]bool{}
	type up struct {
		s string
		v float64
	}
	var list []up
	add := func(s string, force bool) {
		if s == "" || seen[s] {
			return
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return
		}
		if !force && !aligns(mode, period, dur, (100-v)/100) {
			return
		}
		seen[s] = true
		list = append(list, up{s, v})
	}
	for _, s := range uptimeLadder {
		add(s, false)
	}
	add(target, true)
	slices.SortFunc(list, func(a, b up) int {
		switch {
		case a.v < b.v:
			return -1
		case a.v > b.v:
			return 1
		default:
			return 0
		}
	})
	out := make([]string, len(list))
	for i, u := range list {
		out[i] = u.s
	}
	return out
}

// presetSeeds are the fixed seed chips; any other seed is represented by the
// "random" chip instead.
var presetSeeds = []uint64{0, 1, 2, 42}

func isPresetSeed(seed uint64) bool {
	return slices.Contains(presetSeeds, seed)
}

// randomSeed returns a fresh small seed for the "random" chip. It is generated
// at render time, so the chip points at a different schedule on each page load.
func randomSeed() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	// Keep it short and unlikely to collide with the presets.
	return binary.BigEndian.Uint64(b[:])%999_900 + 100
}

// randIndex returns a random index in [0, n) using crypto/rand (a small modulo
// bias is acceptable for picking a random schedule).
func randIndex(n int) int {
	if n <= 0 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return int(binary.BigEndian.Uint64(b[:]) % uint64(n))
}

// dcombo is one feasible (mode, period, duration, uptime) shape for /diceroll.
type dcombo struct {
	mode     string
	period   time.Duration
	duration time.Duration
	uptime   string
}

// dicerollCombos is every aligned (feasible) shape across the explore ladders —
// the population /diceroll samples from. Built once at startup.
var dicerollCombos = buildDicerollCombos()

func buildDicerollCombos() []dcombo {
	modes := []struct {
		name string
		m    Mode
	}{{"even", Even}, {"jitter", Jitter}, {"noise", Noise}}
	var out []dcombo
	for _, md := range modes {
		for _, p := range roundPeriods {
			for _, u := range uptimeLadder {
				up, err := strconv.ParseFloat(u, 64)
				if err != nil {
					continue
				}
				df := (100 - up) / 100
				for _, d := range roundDurations {
					if aligns(md.m, p, d, df) {
						out = append(out, dcombo{md.name, p, d, u})
					}
				}
			}
		}
	}
	return out
}

// durLabel is the human display form of a duration, extending prettyDur with
// days and weeks ("1d", "1w"). It is for UI text only — URLs use prettyDur,
// since time.ParseDuration does not accept "d"/"w".
func durLabel(d time.Duration) string {
	const day = 24 * time.Hour
	const week = 7 * day
	switch {
	case d >= week && d%week == 0:
		return strconv.FormatInt(int64(d/week), 10) + "w"
	case d >= day && d%day == 0:
		return strconv.FormatInt(int64(d/day), 10) + "d"
	default:
		return prettyDur(d)
	}
}

// prettyDur formats whole-unit durations compactly ("30m", "6h"), falling back
// to the standard form otherwise. Output round-trips through time.ParseDuration.
func prettyDur(d time.Duration) string {
	switch {
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	case d%time.Second == 0:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	default:
		return d.String()
	}
}

// trimFloat formats a percentage with up to 4 decimals and no trailing zeros.
func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

// uptimeTarget renders a uptime percentage for carrying in explore links. It
// caps just below 100 so a near-perfect schedule (whose UptimePct rounds to
// 100 at 4 decimals) does not produce uptime=100 links, which would 400.
func uptimeTarget(pct float64) string {
	if pct > 99.9999 {
		pct = 99.9999
	}
	return trimFloat(pct)
}
