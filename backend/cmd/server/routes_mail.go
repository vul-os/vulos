package main

import (
	"net/http"
	"os"
)

// routes_mail.go — exposes the URL of the LilMail service that the OS shell
// embeds as the built-in Mail app. LilMail (github.com/exolutionza/lilmail) is
// the default mail client for Vulos OS; it runs as a local service and is
// embedded same-window via an iframe.
//
// The URL is configurable via VULOS_MAIL_URL (default http://localhost:3000).
// LilMail must be configured with `frame_ancestors` covering this OS origin so
// the shell is permitted to embed it.
func registerMailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/mail/url", func(w http.ResponseWriter, r *http.Request) {
		url := os.Getenv("VULOS_MAIL_URL")
		if url == "" {
			url = "http://localhost:3000"
		}
		writeJSON(w, map[string]string{"url": url})
	})
}
