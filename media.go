package httpware

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// MediaOptions tunes StaticMedia. The zero value is the platform default.
type MediaOptions struct {
	// Immutable reports whether a file name is content-unique (never overwritten in place), so
	// browsers, service workers and Cloudflare may keep it for a year without revalidating.
	// Defaults to IsFingerprinted.
	Immutable func(name string) bool
	// Private reports paths that hold personal or sensitive files (e.g. KYC documents). They
	// get Cache-Control: private, no-store and no cross-origin read header, so no shared cache
	// (Cloudflare, proxies) or service worker keeps a copy. Defaults to none.
	Private func(name string) bool
	// MaxAge (seconds) for names that are not immutable. Default 86400 (1 day), with a 7 day
	// stale-while-revalidate window so a cached image shows instantly while it refreshes.
	MaxAge int
}

var fingerprint = regexp.MustCompile(`(?i)([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{16,})`)

// IsFingerprinted reports whether a file name carries a UUID or a long hex hash. Upload
// handlers across the fleet name files "{uuid}_{unix}.ext", so a new upload always gets a new
// URL and the old URL's bytes never change.
func IsFingerprinted(name string) bool {
	return fingerprint.MatchString(path.Base(name))
}

// StaticMedia serves user media (item images, logos, documents) from root with the headers
// that let it be cached close to the user. Mount it behind http.StripPrefix:
//
//	r.Handle("/media/*", http.StripPrefix("/media", httpware.StaticMedia(mediaRoot, httpware.MediaOptions{})))
//
// Compared with a bare http.FileServer it:
//   - never lists directories (404), so a tenant's uploads cannot be enumerated;
//   - refuses dotfiles and paths escaping root;
//   - sends Cache-Control: immutable for fingerprinted names, 1 day + stale-while-revalidate
//     otherwise, plus a strong ETag so revalidation is a cheap 304;
//   - allows any origin to read it (Access-Control-Allow-Origin: *, Cross-Origin-Resource-Policy:
//     cross-origin) so the frontends' service workers can store non-opaque copies;
//   - sends X-Content-Type-Options: nosniff, and a locked-down CSP for active types (SVG, HTML,
//     XML) so an uploaded file can never run script on the API origin.
func StaticMedia(root string, opts MediaOptions) http.Handler {
	if opts.Immutable == nil {
		opts.Immutable = IsFingerprinted
	}
	if opts.MaxAge <= 0 {
		opts.MaxAge = 86400
	}
	absRoot, _ := filepath.Abs(root)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		clean := path.Clean("/" + r.URL.Path)
		for _, part := range strings.Split(clean, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		full := filepath.Join(absRoot, filepath.FromSlash(clean))
		if full != absRoot && !strings.HasPrefix(full, absRoot+string(filepath.Separator)) {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}

		h := w.Header()
		private := opts.Private != nil && opts.Private(clean)
		if private {
			h.Set("Cache-Control", "private, no-store")
		} else if opts.Immutable(clean) {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			h.Set("Cache-Control", "public, max-age="+strconv.Itoa(opts.MaxAge)+", stale-while-revalidate=604800")
		}
		h.Set("ETag", `"`+strconv.FormatInt(info.Size(), 36)+"-"+strconv.FormatInt(info.ModTime().UnixNano(), 36)+`"`)
		if !private {
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		} else {
			h.Set("Cross-Origin-Resource-Policy", "same-site")
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Add("Vary", "Accept-Encoding")
		switch strings.ToLower(filepath.Ext(clean)) {
		case ".svg", ".html", ".htm", ".xml", ".xhtml":
			h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		}
		// ServeContent handles Range, If-None-Match/If-Modified-Since (304) and Content-Type.
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
	})
}
