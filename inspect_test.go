package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestInspectJSONFailureLinks checks the inspect JSON view exposes a link for
// every failure (all status codes plus hang and drop), each carrying the
// schedule's count-based query and the right extras.
func TestInspectJSONFailureLinks(t *testing.T) {
	srv := httptest.NewServer(routes())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/even/inspect?period=1h&duration=5m&count=4&seed=1&format=json")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var view struct {
		FailureLinks struct {
			Status map[string]string `json:"status"`
			Hang   string            `json:"hang"`
			Drop   string            `json:"drop"`
		} `json:"failure_links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}

	fm := view.FailureLinks
	// Every status code from both lists must be present.
	for _, c := range append(append([]statusCode{}, status4xx...), status5xx...) {
		href, ok := fm.Status[c.code]
		if !ok {
			t.Errorf("missing status link for %s", c.code)
			continue
		}
		if !strings.HasPrefix(href, "/even/status/"+c.code+"?") {
			t.Errorf("status %s href = %q, bad prefix", c.code, href)
		}
		if c.extra != "" && !strings.Contains(href, c.extra) {
			t.Errorf("status %s href = %q, missing extra %q", c.code, href, c.extra)
		}
		q := mustQuery(t, href)
		if q.Get("count") != "4" || q.Get("seed") != "1" {
			t.Errorf("status %s href = %q, missing count/seed", c.code, href)
		}
	}
	if len(fm.Status) != len(status4xx)+len(status5xx) {
		t.Errorf("status link count = %d, want %d", len(fm.Status), len(status4xx)+len(status5xx))
	}
	if !strings.HasPrefix(fm.Hang, "/even/hang?") {
		t.Errorf("hang href = %q", fm.Hang)
	}
	if !strings.HasPrefix(fm.Drop, "/even/drop?") || !strings.Contains(fm.Drop, "after=128") {
		t.Errorf("drop href = %q", fm.Drop)
	}
}
