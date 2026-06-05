package main

import "embed"

// staticFS holds the embedded web assets (docs page, schedule template,
// stylesheet) served by the HTTP handlers.
//
//go:embed static
var staticFS embed.FS

// mustReadAsset reads an embedded asset or panics (the files are compiled in,
// so a failure is a build-time mistake, not a runtime condition).
func mustReadAsset(name string) []byte {
	b, err := staticFS.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}
