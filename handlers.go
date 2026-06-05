package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// request bundles the parsed, validated inputs shared by every mode handler.
type request struct {
	sched  Schedule
	now    time.Time
	active Window
	inOut  bool
}

// parse resolves the mode path segment and common query parameters, then
// evaluates the schedule at the current instant. It writes an error response
// and returns ok=false on any problem.
func parse(w http.ResponseWriter, r *http.Request) (request, bool) {
	mode, herr := parseMode(r.PathValue("mode"))
	if herr != nil {
		writeError(w, herr)
		return request{}, false
	}
	sched, herr := parseSchedule(mode, r.URL.Query())
	if herr != nil {
		writeError(w, herr)
		return request{}, false
	}
	req := request{sched: sched, now: time.Now()}
	req.active, req.inOut = sched.ActiveWindow(req.now)
	setHeaders(w, req)
	return req, true
}

// setHeaders advertises the resolved schedule and current outage state.
func setHeaders(w http.ResponseWriter, req request) {
	h := w.Header()
	s := req.sched
	h.Set("X-Flaky-Mode", s.Mode.String())
	h.Set("X-Flaky-Period", s.Period.String())
	h.Set("X-Flaky-Duration", s.Duration.String())
	h.Set("X-Flaky-Count", strconv.Itoa(s.Count))
	h.Set("X-Flaky-Seed", strconv.FormatUint(s.Seed, 10))
	h.Set("X-Flaky-Uptime", strconv.FormatFloat(s.UptimePct(), 'f', 4, 64))
	if req.inOut {
		h.Set("X-Flaky-In-Outage", "true")
		h.Set("X-Flaky-Outage-Ends", req.active.End.UTC().Format(time.RFC3339))
	} else {
		h.Set("X-Flaky-In-Outage", "false")
		if up := s.Upcoming(req.now, 1); len(up) > 0 {
			h.Set("X-Flaky-Next-Outage", up[0].Start.UTC().Format(time.RFC3339))
		}
	}
}

// handleStatus serves /{mode}/status/{code}: returns the given status code
// during an outage, 200 otherwise.
func handleStatus(w http.ResponseWriter, r *http.Request) {
	req, ok := parse(w, r)
	if !ok {
		return
	}
	code, err := strconv.Atoi(r.PathValue("code"))
	if err != nil || code < 100 || code > 599 {
		writeError(w, badRequest("invalid status code %q: want 100-599", r.PathValue("code")))
		return
	}
	if !req.inOut {
		writeOK(w, req)
		return
	}
	if ra := r.URL.Query().Get("retry-after"); ra != "" {
		switch ra {
		case "auto":
			secs := max(int(math.Ceil(time.Until(req.active.End).Seconds())), 0)
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		default:
			if _, err := strconv.Atoi(ra); err != nil {
				writeError(w, badRequest("invalid retry-after %q: want \"auto\" or seconds", ra))
				return
			}
			w.Header().Set("Retry-After", ra)
		}
	}
	w.WriteHeader(code)
	fmt.Fprintf(w, "outage: HTTP %d until %s\n", code, req.active.End.UTC().Format(time.RFC3339))
}

// handleHang serves /{mode}/hang: during an outage it withholds the response
// until the outage ends (or for the duration given by ?for=), then replies.
func handleHang(w http.ResponseWriter, r *http.Request) {
	req, ok := parse(w, r)
	if !ok {
		return
	}
	if !req.inOut {
		writeOK(w, req)
		return
	}
	delay := time.Until(req.active.End)
	if v := r.URL.Query().Get("for"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			writeError(w, badRequest("invalid for %q: want a non-negative duration", v))
			return
		}
		delay = d
	}
	select {
	case <-time.After(delay):
		fmt.Fprintf(w, "hung for %s, outage ends %s\n", delay.Round(time.Millisecond), req.active.End.UTC().Format(time.RFC3339))
	case <-r.Context().Done():
	}
}

// handleDrop serves /{mode}/drop: during an outage it writes ?after= bytes of
// body (default 0) then drops the TCP connection, emulating a mid-flight reset.
func handleDrop(w http.ResponseWriter, r *http.Request) {
	req, ok := parse(w, r)
	if !ok {
		return
	}
	if !req.inOut {
		writeOK(w, req)
		return
	}
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, badRequest("invalid after %q: want a non-negative byte count", v))
			return
		}
		after = n
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, &httpError{http.StatusInternalServerError, "connection hijacking unsupported"})
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		writeError(w, &httpError{http.StatusInternalServerError, "hijack failed: " + err.Error()})
		return
	}
	defer conn.Close()
	if after > 0 {
		// Raw HTTP/1.1 response head, then a truncated body the client never
		// fully receives before the reset.
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", after+64)
		buf.Write([]byte(strings.Repeat("x", after)))
		buf.Flush()
	}
	// Force a RST rather than a graceful FIN so the client sees a hard drop.
	if tcp, ok := conn.(*net.TCPConn); ok {
		tcp.SetLinger(0)
	}
}

// handleHome serves "/": the interactive schedule explorer for a default
// schedule (HTML for browsers, JSON for scripts). The reference docs live at
// /docs.
func handleHome(w http.ResponseWriter, r *http.Request) {
	v := url.Values{
		"period":   {prettyDur(defaultPeriod)},
		"duration": {prettyDur(defaultDuration)},
		"seed":     {"42"},
		"uptime":   {defaultUptime},
	}
	sched, _ := parseSchedule(Jitter, v) // fixed inputs cannot fail
	req := request{sched: sched, now: time.Now()}
	req.active, req.inOut = sched.ActiveWindow(req.now)
	setHeaders(w, req)
	// Carry the clean target so the explorer highlights "90%" rather than the
	// fractional realized value the count quantizes to.
	serveSchedule(w, r, req, defaultUptime)
}

// handleInspect serves /{mode}/inspect: the schedule explorer (HTML) or a JSON
// view of the schedule and its upcoming windows. It triggers no failure, so it
// carries no status/hang/drop path segment — the schedule is identical across
// all of them.
func handleInspect(w http.ResponseWriter, r *http.Request) {
	req, ok := parse(w, r)
	if !ok {
		return
	}
	serveSchedule(w, r, req, "")
}

// serveSchedule renders req as the HTML explorer for browsers or JSON for
// scripts, per content negotiation. target, when non-empty, overrides the
// availability target carried across explore links (otherwise the request's
// uptime= is used, falling back to the realized value).
func serveSchedule(w http.ResponseWriter, r *http.Request, req request, target string) {
	if wantsHTML(r) {
		if target == "" {
			target = r.URL.Query().Get("uptime")
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset")) // timeline scroll, in periods
		renderScheduleHTML(w, req, target, offset)
		return
	}
	type windowView struct {
		Start    time.Time `json:"start"`
		End      time.Time `json:"end"`
		Duration string    `json:"duration"`
	}
	mkWin := func(win Window) windowView {
		return windowView{win.Start.UTC(), win.End.UTC(), win.End.Sub(win.Start).String()}
	}
	view := struct {
		Version    string       `json:"version"`
		Mode       string       `json:"mode"`
		Period     string       `json:"period"`
		Duration   string       `json:"duration"`
		Seed       uint64       `json:"seed"`
		Count      int          `json:"count"`
		SlotsTotal int          `json:"slots_total,omitempty"`
		UptimePct  float64      `json:"uptime_pct"`
		Now        time.Time    `json:"now"`
		InOutage   bool         `json:"in_outage"`
		Current    *windowView  `json:"current_window"`
		Upcoming   []windowView `json:"upcoming"`
	}{
		Version:    version,
		Mode:       req.sched.Mode.String(),
		Period:     req.sched.Period.String(),
		Duration:   req.sched.Duration.String(),
		Seed:       req.sched.Seed,
		Count:      req.sched.Count,
		SlotsTotal: req.sched.Slots(),
		UptimePct:  req.sched.UptimePct(),
		Now:        req.now.UTC(),
		InOutage:   req.inOut,
	}
	if req.inOut {
		cw := mkWin(req.active)
		view.Current = &cw
	}
	for _, win := range req.sched.Upcoming(req.now, 10) {
		view.Upcoming = append(view.Upcoming, mkWin(win))
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(view)
}

func writeOK(w http.ResponseWriter, req request) {
	next := ""
	if up := req.sched.Upcoming(req.now, 1); len(up) > 0 {
		next = up[0].Start.UTC().Format(time.RFC3339)
	}
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "ok: up at %s, next outage %s\n", req.now.UTC().Format(time.RFC3339), next)
}

func writeError(w http.ResponseWriter, e *httpError) {
	http.Error(w, e.msg, e.code)
}
