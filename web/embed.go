// Package web contains the dependency-free, embedded control console.
package web

import (
	"embed"
	"net/http"
)

//go:embed assets/index.html assets/app.css assets/app.mjs assets/api.mjs
var files embed.FS

func ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := ""
	contentType := ""
	switch r.URL.Path {
	case "/":
		name, contentType = "index.html", "text/html; charset=utf-8"
	case "/ui/app.mjs":
		name, contentType = "app.mjs", "text/javascript; charset=utf-8"
	case "/ui/api.mjs":
		name, contentType = "api.mjs", "text/javascript; charset=utf-8"
	case "/ui/app.css":
		name, contentType = "app.css", "text/css; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := files.ReadFile("assets/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
