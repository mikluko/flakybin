package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

var (
	styleCSS  = mustReadAsset("static/style.css")
	robotsTxt = mustReadAsset("static/robots.txt")
)

func main() {
	addr := flag.String("addr", defaultAddr(), "listen address")
	quiet := flag.Bool("quiet", false, "disable per-request access logging")
	flag.Parse()

	var handler http.Handler = routes()
	if !*quiet {
		handler = accessLog(handler)
	}

	srv := &http.Server{
		Addr:    *addr,
		Handler: handler,
		// Slowloris protection and idle-connection cleanup. WriteTimeout is
		// deliberately NOT set: the /hang endpoint holds the response open
		// until the outage window (or ?for=), and a write deadline would sever
		// it. Do not add WriteTimeout.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Shut down gracefully on SIGINT/SIGTERM so rolling updates drain cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Printf("flakybin %s listening on %s", version, *addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		stop() // restore default signal handling for a second, force-quit signal
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	}
}

// routes builds the request handler. Shared by main and tests.
func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleHome)
	mux.HandleFunc("GET /diceroll", handleDiceroll)
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /robots.txt", handleRobots)
	mux.HandleFunc("GET /style.css", handleStyle)
	mux.HandleFunc("GET /docs", handleDocs)
	mux.HandleFunc("GET /doc", handleDocs)
	mux.HandleFunc("GET /{mode}/inspect", handleInspect)
	mux.HandleFunc("GET /{mode}/status/{code}", handleStatus)
	mux.HandleFunc("GET /{mode}/hang", handleHang)
	mux.HandleFunc("GET /{mode}/drop", handleDrop)
	return withServerHeader(mux)
}

// withServerHeader advertises the build version on every response.
func withServerHeader(next http.Handler) http.Handler {
	server := "flakybin/" + version
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", server)
		next.ServeHTTP(w, r)
	})
}

func defaultAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}

// handleDocs serves the static reference documentation at /docs.
func handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	docsTmpl.ExecuteTemplate(w, "base", struct{ Version string }{version})
}

// handleStyle serves the shared stylesheet used by both the explorer and docs.
func handleStyle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(styleCSS)
}

// handleRobots serves robots.txt, which keeps crawlers off the failure
// endpoints — only the home page and docs are allowed.
func handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(robotsTxt)
}

// healthzPath is the liveness/readiness probe endpoint. It always returns 204
// and is excluded from the access log (see accessLog).
const healthzPath = "/healthz"

// handleHealthz is the always-200-ish health probe: 204 No Content.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}
