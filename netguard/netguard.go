// Package netguard guards outbound HTTP requests to URLs that came from data (a tenant's website,
// an uploaded media link, a provider result): only public unicast addresses are reached. The
// check runs in the dialer on the address actually connected to, after DNS, so a name that first
// resolves to a public address and later to a private one (DNS rebinding) is still refused. Every
// redirect is checked again.
//
// One copy for the fleet: marketflow-api (publicweb) and marketflow-ai (media downloads) used to
// hold their own (2026-11-17).
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"syscall"
	"time"
)

// ErrBlocked is a connection, redirect or URL the guard refuses.
var ErrBlocked = errors.New("netguard: destination not allowed")

// blocked are ranges Go's IP helpers do not already flag but that are not public hosts:
// "this network", carrier-grade NAT, IETF protocol assignments, documentation and benchmarking
// ranges, reserved, broadcast, NAT64 (which reaches IPv4 inside), discard and documentation IPv6.
var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
		"2001:db8::/32", "fc00::/7",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsPublic reports whether ip is a public unicast address: not loopback, private, link-local
// (the cloud metadata address 169.254.169.254), multicast, unspecified or any range in blocked.
func IsPublic(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// IsPublicIP is IsPublic for a net.IP.
func IsPublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	return ok && IsPublic(a)
}

// Options shape a guarded client. The zero value is the strict default: https only, port 443,
// at most 5 redirects.
type Options struct {
	Timeout time.Duration // whole request; 0 means 30 seconds
	// MaxRedirects is the most redirects followed; 0 means 5, a negative value means none.
	MaxRedirects int
	// AllowHTTP also allows plain http (port 80) for public pages and media that lack https.
	AllowHTTP bool
	// AllowPrivate turns the address and port checks off. Tests serving from 127.0.0.1 only.
	AllowPrivate bool
}

func (o Options) ports() map[string]bool {
	p := map[string]bool{"443": true}
	if o.AllowHTTP {
		p["80"] = true
	}
	return p
}

func (o Options) schemeAllowed(s string) bool {
	return s == "https" || (o.AllowHTTP && s == "http")
}

// Control is a net.Dialer Control that refuses non-public addresses and ports other than
// 443 (and 80 with AllowHTTP).
func (o Options) Control(_, address string, _ syscall.RawConn) error {
	if o.AllowPrivate {
		return nil
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ErrBlocked
	}
	if !o.ports()[port] {
		return fmt.Errorf("%w: port %s", ErrBlocked, port)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !IsPublic(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, host)
	}
	return nil
}

// Client returns an HTTP client that reaches only public addresses. It never uses a proxy from
// the environment: the dial check would then see the proxy, not the target.
func Client(o Options) *http.Client {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxRedirects := o.MaxRedirects
	if maxRedirects == 0 {
		maxRedirects = 5
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: o.Control}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if maxRedirects < 0 || len(via) >= maxRedirects {
				return fmt.Errorf("%w: too many redirects", ErrBlocked)
			}
			if !o.schemeAllowed(req.URL.Scheme) || req.URL.User != nil {
				return fmt.Errorf("%w: redirect to %s", ErrBlocked, req.URL.Scheme)
			}
			return nil
		},
	}
}

// CheckURL refuses, before any request, a URL whose scheme is not allowed, that carries
// credentials, has no host, names a port other than the allowed ones, or is an IP literal that
// is not public. Host names are checked at dial time by Client.
func (o Options) CheckURL(ctx context.Context, raw string) (*url.URL, error) {
	_ = ctx
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBlocked, err)
	}
	if !o.schemeAllowed(u.Scheme) || u.User != nil || u.Hostname() == "" {
		return nil, ErrBlocked
	}
	if o.AllowPrivate {
		return u, nil
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || !o.ports()[strconv.Itoa(n)] {
			return nil, fmt.Errorf("%w: port %s", ErrBlocked, p)
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !IsPublic(ip) {
		return nil, fmt.Errorf("%w: %s", ErrBlocked, ip)
	}
	return u, nil
}
