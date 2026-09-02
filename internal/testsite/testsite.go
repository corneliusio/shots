// Package testsite serves deterministic pages for HTTP and browser tests.
//
// The pages are embedded, so a server started from any package behaves the
// same regardless of the working directory. Every response is uncacheable.
package testsite

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"
)

// Basic authentication credentials accepted by /basic-auth.
const (
	BasicAuthUser     = "user"
	BasicAuthPassword = "secret"
)

// maxDelay bounds /slow so a bad query cannot hang a test run.
const maxDelay = 5 * time.Second

//go:embed pages
var content embed.FS

// New starts a test server. The caller must close it.
func New() *httptest.Server {
	return httptest.NewServer(Handler())
}

// Handler serves the test site:
//
//	/                      the embedded pages; /index.html canonicalizes to /
//	/redirect?to=&status=  a redirect, by default 302 to /redirected.html
//	/status/{code}         that HTTP status
//	/slow?ms=              a response delayed by ms milliseconds
//	/slow-image?ms=        an SVG image delayed by ms milliseconds
//	/basic-auth            401 until BasicAuthUser and BasicAuthPassword are sent
//	/set-cookie?name=&value= sets a cookie, then redirects to /storage.html
//	/reset                 closes the connection without a response
func Handler() http.Handler {
	pages, err := fs.Sub(content, "pages")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(pages))
	mux.HandleFunc("GET /redirect", redirect)
	mux.HandleFunc("GET /status/{code}", status)
	mux.HandleFunc("GET /slow", slow)
	mux.HandleFunc("GET /slow-image", slowImage)
	mux.HandleFunc("GET /basic-auth", basicAuth)
	mux.HandleFunc("GET /set-cookie", setCookie)
	mux.HandleFunc("GET /reset", reset)
	return noStore(mux)
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func redirect(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("to")
	if to == "" {
		to = "/redirected.html"
	}
	if !strings.HasPrefix(to, "/") {
		http.Error(w, "to must be a site-relative path", http.StatusBadRequest)
		return
	}
	code := http.StatusFound
	if raw := r.URL.Query().Get("status"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 300 || parsed > 399 {
			http.Error(w, "status must be a redirect status", http.StatusBadRequest)
			return
		}
		code = parsed
	}
	http.Redirect(w, r, to, code)
}

func status(w http.ResponseWriter, r *http.Request) {
	code, err := strconv.Atoi(r.PathValue("code"))
	if err != nil || code < 100 || code > 599 {
		http.Error(w, "code must be an HTTP status", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintf(w, "<!doctype html>\n<title>Status %d</title>\n<h1 id=status>%d</h1>\n", code, code)
}

func slow(w http.ResponseWriter, r *http.Request) {
	delay, ok := requestedDelay(w, r)
	if !ok {
		return
	}
	select {
	case <-time.After(delay):
	case <-r.Context().Done():
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html>\n<title>Slow</title>\n<h1 id=slow>%d</h1>\n", delay.Milliseconds())
}

func slowImage(w http.ResponseWriter, r *http.Request) {
	delay, ok := requestedDelay(w, r)
	if !ok {
		return
	}
	select {
	case <-time.After(delay):
	case <-r.Context().Done():
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`)
}

func requestedDelay(w http.ResponseWriter, r *http.Request) (time.Duration, bool) {
	delay := 200 * time.Millisecond
	if raw := r.URL.Query().Get("ms"); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < 0 {
			http.Error(w, "ms must be a non-negative number", http.StatusBadRequest)
			return 0, false
		}
		delay = min(time.Duration(ms)*time.Millisecond, maxDelay)
	}
	return delay, true
}

func basicAuth(w http.ResponseWriter, r *http.Request) {
	user, password, ok := r.BasicAuth()
	if !ok || user != BasicAuthUser || password != BasicAuthPassword {
		w.Header().Set("WWW-Authenticate", `Basic realm="shots"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html>\n<title>Authenticated</title>\n<h1 id=user>%s</h1>\n", user)
}

func setCookie(w http.ResponseWriter, r *http.Request) {
	name, value := r.URL.Query().Get("name"), r.URL.Query().Get("value")
	if name == "" {
		name, value = "shots_session", "granted"
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/"})
	http.Redirect(w, r, "/storage.html", http.StatusFound)
}

// reset drops the connection, so clients see a network failure rather than an
// HTTP error.
func reset(w http.ResponseWriter, r *http.Request) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	conn.Close()
}
