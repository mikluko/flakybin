package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestCrawlNoBrokenLinks walks the served link graph in-process: starting from
// the home page and a few representative inspect pages, it follows every
// generated explore link (transitively) and asserts each target renders 200.
// Every emulate (failure) link is validated by parsing rather than fetched, so
// the crawl never trips a hang or connection drop.
//
// Links are enumerated with buildNav, the same source the template renders, so
// the crawl follows exactly what a user can click without scraping HTML.
func TestCrawlNoBrokenLinks(t *testing.T) {
	srv := httptest.NewServer(routes())
	defer srv.Close()
	client := srv.Client()

	const maxVisited = 600
	seeds := []string{
		"/",
		"/docs",
		"/jitter/inspect?period=24h&duration=5m&uptime=99",
		"/noise/inspect?period=24h&duration=5m&uptime=99.9",
		"/jitter/inspect?period=1h&duration=7m&uptime=90", // indivisible (jitter only)
		"/noise/inspect?period=30m&duration=15m&uptime=50",
	}

	visited := map[string]bool{}
	queue := append([]string{}, seeds...)

	for len(queue) > 0 && len(visited) < maxVisited {
		u := queue[0]
		queue = queue[1:]
		if visited[u] {
			continue
		}
		visited[u] = true

		resp, err := client.Get(srv.URL + u)
		if err != nil {
			t.Fatalf("GET %s: %v", u, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s -> %d", u, resp.StatusCode)
			continue
		}

		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatalf("bad url %q: %v", u, err)
		}
		segs := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segs) != 2 || segs[1] != "inspect" {
			continue // home/docs: nothing to enumerate
		}
		mode, herr := parseMode(segs[0])
		if herr != nil {
			t.Errorf("%s: bad mode: %s", u, herr.msg)
			continue
		}
		sched, herr := parseSchedule(mode, parsed.Query())
		if herr != nil {
			t.Errorf("followed link %s is a 400: %s", u, herr.msg)
			continue
		}
		target := parsed.Query().Get("uptime")
		if target == "" {
			target = uptimeTarget(sched.UptimePct())
		}

		nav := buildNav(sched, target)
		for _, row := range nav.Rows {
			for _, chip := range row.Chips {
				assertLinkValid(t, chip.Href)
				if isInspect(chip.Href) {
					queue = append(queue, chip.Href)
				}
			}
		}
		for _, row := range nav.Fail {
			for _, chip := range row.Chips {
				assertLinkValid(t, chip.Href) // validate, do not fetch (would hang/drop)
			}
		}
	}

	if len(visited) < len(seeds) {
		t.Fatalf("crawl visited only %d pages", len(visited))
	}
	t.Logf("crawled %d pages, 0 broken", len(visited))
}

// assertLinkValid parses a generated href the way the server would and asserts
// it resolves to a valid mode and schedule (i.e. would not 400).
func assertLinkValid(t *testing.T, href string) {
	t.Helper()
	u, err := url.Parse(href)
	if err != nil {
		t.Errorf("unparseable href %q: %v", href, err)
		return
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	mode, herr := parseMode(segs[0])
	if herr != nil {
		t.Errorf("href %q: bad mode: %s", href, herr.msg)
		return
	}
	if _, herr := parseSchedule(mode, u.Query()); herr != nil {
		t.Errorf("href %q -> 400: %s", href, herr.msg)
	}
}

func isInspect(href string) bool {
	u, err := url.Parse(href)
	if err != nil {
		return false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(segs) == 2 && segs[1] == "inspect"
}
