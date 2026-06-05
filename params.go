package main

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// httpError carries an HTTP status code alongside a message.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(format string, a ...any) *httpError {
	return &httpError{http.StatusBadRequest, fmt.Sprintf(format, a...)}
}

// parseMode resolves the {mode} path segment.
func parseMode(s string) (Mode, *httpError) {
	switch s {
	case "noise":
		return Noise, nil
	case "jitter":
		return Jitter, nil
	default:
		return 0, &httpError{http.StatusNotFound, fmt.Sprintf("unknown mode %q (want noise or jitter)", s)}
	}
}

// Defaults applied when a parameter is omitted, mirroring the values the
// explorer opens with so a bare endpoint URL still resolves a schedule.
const (
	defaultPeriod   = 24 * time.Hour
	defaultDuration = 15 * time.Minute
	defaultUptime   = "90"
)

// parseSchedule builds a Schedule from the common query parameters:
//
//	seed      uint64        outage RNG seed (default 0)
//	period    duration      length of one repeating cycle (default 24h)
//	duration  duration      length of a single outage (default 15m)
//	count     int           number of outages per period   } at most one;
//	uptime    float percent target availability            } default uptime=90
func parseSchedule(mode Mode, q url.Values) (Schedule, *httpError) {
	s := Schedule{Mode: mode}

	if v := q.Get("seed"); v != "" {
		seed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return s, badRequest("invalid seed %q: must be a non-negative integer", v)
		}
		s.Seed = seed
	}

	period, herr := optionalDuration(q, "period", defaultPeriod)
	if herr != nil {
		return s, herr
	}
	s.Period = period

	dur, herr := optionalDuration(q, "duration", defaultDuration)
	if herr != nil {
		return s, herr
	}
	s.Duration = dur

	if dur > period {
		return s, badRequest("duration %s exceeds period %s", dur, period)
	}

	// Noise requires an even division of the period into outage-sized slots.
	var slots int
	if mode == Noise {
		if period%dur != 0 {
			return s, badRequest("noise mode requires period (%s) divisible by duration (%s); remainder %s", period, dur, period%dur)
		}
		slots = int(period / dur)
		if slots > maxSlots {
			return s, badRequest("too many slots: period/duration = %d exceeds limit %d", slots, maxSlots)
		}
	}

	count, herr := resolveCount(mode, q, period, dur, slots)
	if herr != nil {
		return s, herr
	}
	s.Count = count
	return s, nil
}

// resolveCount derives the per-period outage count from count or uptime. The
// two are mutually exclusive; when neither is given it defaults to uptime=90.
func resolveCount(mode Mode, q url.Values, period, dur time.Duration, slots int) (int, *httpError) {
	countStr, hasCount := q["count"]
	uptimeStr, hasUptime := q["uptime"]
	if hasCount && hasUptime {
		return 0, badRequest("count and uptime are mutually exclusive; supply only one")
	}

	var count int
	if hasCount {
		c, err := strconv.Atoi(countStr[0])
		if err != nil || c < 1 {
			return 0, badRequest("invalid count %q: must be a positive integer", countStr[0])
		}
		count = c
	} else {
		uptimeVal := defaultUptime
		if hasUptime {
			uptimeVal = uptimeStr[0]
		}
		up, err := strconv.ParseFloat(uptimeVal, 64)
		if err != nil || up < 0 || up > 100 {
			return 0, badRequest("invalid uptime %q: must be a percentage in [0,100]", uptimeVal)
		}
		// Total downtime per period implied by the availability target. A
		// target of 100% (rounding to no downtime) is rejected. Any sub-100%
		// target is clamped to at least one outage and at most capacity-1, so
		// it never 400s and never renders a permanently down endpoint. Realized
		// availability is therefore quantized by the outage duration and only
		// approximates the requested target.
		downFrac := (100 - up) / 100
		if downFrac <= 0 {
			return 0, badRequest("uptime %.4g%% leaves no room for any outage", up)
		}
		switch mode {
		case Noise:
			count = int(math.Round(float64(slots) * downFrac))
		case Jitter:
			downtime := downFrac * float64(period)
			count = int(math.Round(downtime / float64(dur)))
		}
		if count < 1 {
			count = 1
		}
		if capacity := int(period / dur); capacity >= 2 && count > capacity-1 {
			count = capacity - 1
		}
	}

	if mode == Noise && count > slots {
		return 0, badRequest("count %d exceeds available slots %d (period/duration)", count, slots)
	}
	return count, nil
}

func optionalDuration(q url.Values, name string, def time.Duration) (time.Duration, *httpError) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, badRequest("invalid %s %q: %v", name, v, err)
	}
	if d <= 0 {
		return 0, badRequest("%s must be positive, got %s", name, d)
	}
	return d, nil
}
