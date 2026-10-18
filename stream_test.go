package httpware

import (
	"net/http/httptest"
	"testing"
)

func TestIsStreaming(t *testing.T) {
	cases := map[string]bool{"text/event-stream": true, "application/x-ndjson+gzip": true, "application/json": false, "": false}
	for accept, want := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept", accept)
		if got := IsStreaming(r); got != want {
			t.Errorf("Accept %q: got %v want %v", accept, got, want)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Upgrade", "WebSocket")
	if !IsStreaming(r) {
		t.Error("websocket upgrade is streaming")
	}
}
