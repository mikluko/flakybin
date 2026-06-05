package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestDicerollCombosValid asserts every shape /diceroll can pick resolves to a
// valid, aligned schedule.
func TestDicerollCombosValid(t *testing.T) {
	if len(dicerollCombos) == 0 {
		t.Fatal("no diceroll combos built")
	}
	for _, c := range dicerollCombos {
		mode, herr := parseMode(c.mode)
		if herr != nil {
			t.Errorf("combo mode %q: %s", c.mode, herr.msg)
			continue
		}
		q := url.Values{
			"period":   {prettyDur(c.period)},
			"duration": {prettyDur(c.duration)},
			"uptime":   {c.uptime},
		}
		s, herr := parseSchedule(mode, q)
		if herr != nil {
			t.Errorf("combo %v -> 400: %s", c, herr.msg)
			continue
		}
		up, _ := strconv.ParseFloat(c.uptime, 64)
		if !aligns(mode, s.Period, s.Duration, (100-up)/100) {
			t.Errorf("combo %v not aligned", c)
		}
	}
}

// TestDicerollRedirect asserts /diceroll 302-redirects to a Location that
// parses and validates.
func TestDicerollRedirect(t *testing.T) {
	srv := httptest.NewServer(routes())
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	for range 20 {
		resp, err := client.Get(srv.URL + "/diceroll")
		if err != nil {
			t.Fatalf("GET /diceroll: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("status %d, want 302", resp.StatusCode)
		}
		loc := resp.Header.Get("Location")
		u, err := url.Parse(loc)
		if err != nil {
			t.Fatalf("bad Location %q: %v", loc, err)
		}
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segs) != 2 || segs[1] != "inspect" {
			t.Fatalf("Location %q is not an inspect URL", loc)
		}
		mode, herr := parseMode(segs[0])
		if herr != nil {
			t.Fatalf("Location %q bad mode: %s", loc, herr.msg)
		}
		if _, herr := parseSchedule(mode, u.Query()); herr != nil {
			t.Fatalf("Location %q -> 400: %s", loc, herr.msg)
		}
		// And the target actually serves 200.
		tr, err := client.Get(srv.URL + loc)
		if err != nil {
			t.Fatalf("GET %s: %v", loc, err)
		}
		tr.Body.Close()
		if tr.StatusCode != http.StatusOK {
			t.Fatalf("redirect target %s -> %d", loc, tr.StatusCode)
		}
	}
}
