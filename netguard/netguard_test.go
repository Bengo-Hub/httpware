package netguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestIsPublic(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.5.5", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"198.18.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "192.0.2.10", "::1", "fd00::1",
		"fe80::1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "2001:db8::1",
	} {
		if IsPublic(netip.MustParseAddr(s)) {
			t.Errorf("%s must not be public", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700::1111", "::ffff:8.8.8.8"} {
		if !IsPublic(netip.MustParseAddr(s)) {
			t.Errorf("%s must be public", s)
		}
	}
	if IsPublicIP(net.ParseIP("10.0.0.1")) || !IsPublicIP(net.ParseIP("8.8.4.4")) {
		t.Error("IsPublicIP disagrees with IsPublic")
	}
}

func TestControl(t *testing.T) {
	strict := Options{}
	web := Options{AllowHTTP: true}
	cases := []struct {
		o    Options
		addr string
		ok   bool
	}{
		{strict, "8.8.8.8:443", true},
		{strict, "8.8.8.8:80", false},
		{web, "8.8.8.8:80", true},
		{web, "8.8.8.8:8080", false},
		{strict, "10.0.0.1:443", false},
		{strict, "169.254.169.254:443", false},
		{strict, "[::1]:443", false},
		{Options{AllowPrivate: true}, "127.0.0.1:9999", true},
	}
	for _, c := range cases {
		if err := c.o.Control("tcp", c.addr, nil); (err == nil) != c.ok {
			t.Errorf("Control(%+v, %s) = %v", c.o, c.addr, err)
		}
	}
}

func TestCheckURL(t *testing.T) {
	ctx := context.Background()
	strict := Options{}
	web := Options{AllowHTTP: true}
	for _, raw := range []string{"https://example.com/a", "https://example.com:443/a", "https://8.8.8.8/"} {
		if _, err := strict.CheckURL(ctx, raw); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://example.com/", "file:///etc/passwd", "gopher://x/", "https://user:pw@example.com/",
		"https:///nohost", "https://example.com:8443/", "https://127.0.0.1/", "https://[::1]/", "::not a url",
	} {
		if _, err := strict.CheckURL(ctx, raw); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	if _, err := web.CheckURL(ctx, "http://example.com/x"); err != nil {
		t.Errorf("AllowHTTP should accept http: %v", err)
	}
}

func TestClientRefusesLoopbackAndRedirectsInside(t *testing.T) {
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer inside.Close()
	if _, err := Client(Options{Timeout: 5 * time.Second, AllowHTTP: true}).Get(inside.URL); err == nil {
		t.Fatal("a loopback fetch must be refused")
	}
	if _, err := Client(Options{Timeout: 5 * time.Second, AllowHTTP: true, AllowPrivate: true}).Get(inside.URL); err != nil {
		t.Fatalf("AllowPrivate must reach the test server: %v", err)
	}

	// A redirect from an allowed server to another scheme is refused.
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://example.com/", http.StatusFound)
	}))
	defer hop.Close()
	if _, err := Client(Options{AllowHTTP: true, AllowPrivate: true}).Get(hop.URL); err == nil {
		t.Fatal("a redirect to another scheme must be refused")
	}
	// Redirect loops stop at the limit.
	var loop *httptest.Server
	loop = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, loop.URL, http.StatusFound)
	}))
	defer loop.Close()
	if _, err := Client(Options{AllowHTTP: true, AllowPrivate: true, MaxRedirects: 2}).Get(loop.URL); err == nil {
		t.Fatal("redirects beyond the limit must be refused")
	}
	if _, err := Client(Options{AllowHTTP: true, AllowPrivate: true, MaxRedirects: -1}).Get(hop.URL); err == nil {
		t.Fatal("MaxRedirects -1 follows none")
	}
}
