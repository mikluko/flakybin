package main

import (
	"encoding/binary"
	"hash/fnv"
	"math/rand"
	"sort"
	"time"
)

// maxSlots bounds the slot count in noise mode so a request cannot force a
// huge allocation (noise selection permutes a slice of N ints).
const maxSlots = 100_000

// Mode is the outage-placement strategy.
type Mode int

const (
	// Noise divides each period into N = period/duration fixed slots and
	// marks Count of them as outages. Period must divide evenly by duration.
	Noise Mode = iota
	// Jitter drops Count outages of length duration at seed-hashed offsets
	// anywhere within each period. No divisibility constraint.
	Jitter
	// Even spaces Count outages of length duration uniformly across the period
	// (one per period/Count interval); the seed shifts the whole pattern's
	// phase. No divisibility constraint.
	Even
)

func (m Mode) String() string {
	switch m {
	case Jitter:
		return "jitter"
	case Even:
		return "even"
	default:
		return "noise"
	}
}

// Window is a half-open outage interval [Start, End).
type Window struct {
	Start time.Time
	End   time.Time
}

func (w Window) contains(t time.Time) bool {
	return !t.Before(w.Start) && t.Before(w.End)
}

// Schedule is a deterministic outage schedule. The same field values always
// produce the same windows on the absolute time line, so results are
// reproducible across restarts and across processes.
type Schedule struct {
	Mode     Mode
	Period   time.Duration
	Duration time.Duration
	Seed     uint64
	Count    int // resolved number of outages per period
}

// Slots returns N = period/duration for noise mode (0 for jitter and even).
func (s Schedule) Slots() int {
	if s.Mode == Noise {
		return int(s.Period / s.Duration)
	}
	return 0
}

// UptimePct is the fraction of each period that is available, as a percentage.
func (s Schedule) UptimePct() float64 {
	down := float64(s.Count) * float64(s.Duration)
	return (1 - down/float64(s.Period)) * 100
}

// periodIndex returns the index of the period containing t.
func (s Schedule) periodIndex(t time.Time) int64 {
	return t.UnixNano() / int64(s.Period)
}

// windowsForPeriod returns the outage windows for the given period index,
// sorted by start time.
func (s Schedule) windowsForPeriod(n int64) []Window {
	periodStart := n * int64(s.Period)
	dur := int64(s.Duration)
	out := make([]Window, 0, s.Count)
	switch s.Mode {
	case Noise:
		for _, slot := range s.selectedSlots(n) {
			start := periodStart + int64(slot)*dur
			out = append(out, Window{time.Unix(0, start), time.Unix(0, start+dur)})
		}
	case Jitter:
		span := int64(s.Period) - dur // max valid offset
		for k := 0; k < s.Count; k++ {
			off := int64(0)
			if span > 0 {
				off = int64(jitterHash(s.Seed, n, k) % uint64(span+1))
			}
			start := periodStart + off
			out = append(out, Window{time.Unix(0, start), time.Unix(0, start+dur)})
		}
	case Even:
		// One outage per equal interval (spacing = period/Count), all shifted
		// by the same seed-derived phase so the pattern stays evenly spaced.
		spacing := int64(s.Period) / int64(s.Count)
		phase := int64(0)
		if max := spacing - dur; max > 0 {
			phase = int64(jitterHash(s.Seed, n, 0) % uint64(max+1))
		}
		for k := 0; k < s.Count; k++ {
			start := periodStart + int64(k)*spacing + phase
			out = append(out, Window{time.Unix(0, start), time.Unix(0, start+dur)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// selectedSlots returns the sorted indices of outage slots for period n.
func (s Schedule) selectedSlots(n int64) []int {
	N := s.Slots()
	r := rand.New(rand.NewSource(srcSeed(s.Seed, n)))
	perm := r.Perm(N)[:s.Count]
	sort.Ints(perm)
	return perm
}

// ActiveWindow reports the outage window containing t, if any.
func (s Schedule) ActiveWindow(t time.Time) (Window, bool) {
	for _, w := range s.windowsForPeriod(s.periodIndex(t)) {
		if w.contains(t) {
			return w, true
		}
	}
	return Window{}, false
}

// Upcoming returns up to k windows whose End is after t, sorted by start.
// A currently-active window is included as the first element.
func (s Schedule) Upcoming(t time.Time, k int) []Window {
	out := make([]Window, 0, k)
	n := s.periodIndex(t)
	for len(out) < k && n < s.periodIndex(t)+1024 {
		for _, w := range s.windowsForPeriod(n) {
			if w.End.After(t) {
				out = append(out, w)
				if len(out) == k {
					return out
				}
			}
		}
		n++
	}
	return out
}

// srcSeed mixes the schedule seed with a period index into a stable PRNG seed.
func srcSeed(seed uint64, n int64) int64 {
	return int64(seed*0x9E3779B97F4A7C15 + uint64(n)*0xBF58476D1CE4E5B9)
}

// jitterHash deterministically hashes (seed, period, k) into a 64-bit value.
func jitterHash(seed uint64, n int64, k int) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], seed)
	h.Write(b[:])
	binary.BigEndian.PutUint64(b[:], uint64(n))
	h.Write(b[:])
	binary.BigEndian.PutUint64(b[:], uint64(k))
	h.Write(b[:])
	return h.Sum64()
}
