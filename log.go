package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// accessLog wraps a handler with one log line per request:
//
//	<remote> <method> <uri> <status> <duration>
//
// Status is "-" when the connection was hijacked (e.g. the drop endpoint),
// since no status line is written in that case.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &logWriter{ResponseWriter: w}
		next.ServeHTTP(lw, r)
		status := "-"
		if lw.status != 0 {
			status = fmt.Sprintf("%d", lw.status)
		}
		log.Printf("%s %s %s %s %s",
			r.RemoteAddr, r.Method, r.URL.RequestURI(), status, time.Since(start).Round(time.Millisecond))
	})
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
