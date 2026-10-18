package httpware

import (
	"net/http"
	"strings"
	"time"
)

// IsStreaming reports whether r opens a long-lived connection: a WebSocket upgrade, a
// Server-Sent Events stream, or a JSON Lines stream (tenant exports, which may run for minutes
// and are gzipped already).
func IsStreaming(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		strings.Contains(accept, "text/event-stream") ||
		strings.Contains(accept, "application/x-ndjson")
}

// BypassForStreaming wraps a middleware so it never runs on WebSocket or SSE requests.
//
// Use it for middleware that breaks long-lived connections: chi's Compress (its writer cannot
// Hijack), chi's Timeout (cancels the request context after N seconds, killing the stream) and
// response buffering. It replaces the bypassForWebsocket helper that was copied into each
// service's router, and also covers SSE, which that helper missed.
//
//	r.Use(httpware.BypassForStreaming(middleware.Compress(5)))
//	r.Use(httpware.BypassForStreaming(middleware.Timeout(30 * time.Second)))
func BypassForStreaming(mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := mw(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if IsStreaming(r) {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

// StreamHeaders sets the headers every SSE response needs behind ingress-nginx: no caching and
// no proxy buffering (without X-Accel-Buffering: no, nginx holds events until its buffer fills).
func StreamHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

// ExtendWriteDeadline lifts the server's WriteTimeout for one streaming response so the stream
// is not cut at the server-wide limit. Call it at the start of an SSE/WebSocket handler; the
// caller keeps its own heartbeat and context cancellation. Ignored when the writer chain does
// not support deadlines.
func ExtendWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
}
