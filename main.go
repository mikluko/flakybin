package main

import (
	"flag"
	"log"
	"net/http"
	"os"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

var (
	indexHTML = mustReadAsset("static/index.html")
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

	log.Printf("flakybin %s listening on %s", version, *addr)
	srv := &http.Server{Addr: *addr, Handler: handler}
	log.Fatal(srv.ListenAndServe())
}

// routes builds the request handler. Shared by main and tests.
func routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleHome)
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
	w.Write(indexHTML)
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
