package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// accessLog wraps a handler with one log line per request:
//
//	<client-ip> <method> <uri> <status> <duration> "<user-agent>"
//
// The client IP is the real caller behind a proxy/ingress (see clientIP).
// Status is "-" when the connection was hijacked (e.g. the drop endpoint),
// since no status line is written in that case.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health probes are frequent and uninteresting; don't log them.
		if r.URL.Path == healthzPath {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		lw := &logWriter{ResponseWriter: w}
		next.ServeHTTP(lw, r)
		status := "-"
		if lw.status != 0 {
			status = fmt.Sprintf("%d", lw.status)
		}
		log.Printf("%s %s %s %s %s %q",
			clientIP(r), r.Method, r.URL.RequestURI(), status,
			time.Since(start).Round(time.Millisecond), r.UserAgent())
	})
}

// clientIP returns the originating client address, preferring proxy headers set
// by an ingress/load balancer over the direct peer (which would otherwise be the
// proxy itself). Order: leftmost X-Forwarded-For, then X-Real-IP, then the
// connection peer with its port stripped.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// logWriter records the response status code while delegating everything else,
// including hijacking, so it stays transparent to the drop endpoint.
type logWriter struct {
	http.ResponseWriter
	status int
}

func (l *logWriter) WriteHeader(code int) {
	l.status = code
	l.ResponseWriter.WriteHeader(code)
}

func (l *logWriter) Write(b []byte) (int, error) {
	if l.status == 0 {
		l.status = http.StatusOK
	}
	return l.ResponseWriter.Write(b)
}

func (l *logWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := l.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hj.Hijack()
}
