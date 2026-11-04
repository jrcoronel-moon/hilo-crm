// Package httpx is the HTTP plumbing shared by the web and mcp services:
// security headers, client IP resolution behind trusted proxies, body limits,
// panic recovery, request logging, rate limiting and JSON helpers.
package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"
)

// MaxBody is the default request body limit (1 MiB). Endpoints that need
// less tighten it with http.MaxBytesReader.
const MaxBody = 1 << 20

// SecurityHeaders sets the headers every response carries. The CSP allows no
// inline script and no third-party origin: the web UI is server-rendered and
// its only script is served from /static.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
			"connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cross-Origin-Embedder-Policy", "require-corp")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// LimitBody caps every request body.
func LimitBody(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// Recover turns a panic into a 500 without leaking the panic value or stack
// to the client; both go to the log.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler { //nolint:errorlint // sentinel comparison is how net/http documents it
						panic(v)
					}
					log.Error("panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush on the real writer (the MCP
// transport streams).
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Logger logs one line per request. It never logs query strings, headers or
// bodies: they carry OAuth codes, tokens and personal data.
func Logger(log *slog.Logger, ips *ClientIP) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds(), "ip", ips.Of(r))
		})
	}
}

// ClientIP resolves the caller's address, honouring X-Forwarded-For only when
// the direct peer is a trusted proxy, and then only the right-most hop that is
// not itself a trusted proxy (the left part of the header is client-controlled).
type ClientIP struct {
	trusted []netip.Prefix
}

// NewClientIP builds a resolver trusting the given proxy ranges.
func NewClientIP(trusted []netip.Prefix) *ClientIP { return &ClientIP{trusted: trusted} }

func (c *ClientIP) isTrusted(a netip.Addr) bool {
	for _, p := range c.trusted {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// Of returns the client address for r.
func (c *ClientIP) Of(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !c.isTrusted(peer) {
		return host
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !c.isTrusted(a) {
			return a.Unmap().String()
		}
	}
	return peer.Unmap().String()
}

// Key is the rate-limit key for r: the client address, or its /64 network
// for IPv6, since a single subscriber usually holds a whole /64 and could
// otherwise rotate addresses to get a fresh limit each time.
func (c *ClientIP) Key(r *http.Request) string {
	ip := c.Of(r)
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	if a = a.Unmap(); a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a.WithZone(""), 64).Masked().String()
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes {"error": msg}. msg must be safe to show to the caller;
// internal details belong in the log, never here.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// DecodeJSON strictly decodes a JSON body (unknown fields rejected, single
// value only).
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected trailing data")
	}
	return nil
}

// BearerToken extracts a bearer token from the Authorization header.
func BearerToken(r *http.Request) string {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}
