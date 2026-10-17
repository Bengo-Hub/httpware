package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bengo-Hub/httpware"
)

// LegacyPrefix is how services stored local media paths before object storage ("/media/...").
// Such a file stays on the service's media volume, served at <MEDIA_URL_BASE>/media/..., until a
// Relinker moves it to its key and the row is updated.
const LegacyPrefix = "/media/"

// Store reads and writes one service's files. It is safe for concurrent use.
type Store struct {
	service string
	cfg     Config
	s3      *s3Client // nil in local mode

	signer        *httpware.MediaSigner
	legacyPrivate func(rel string) bool
	legacyBases   []string // origins whose "/media/..." URLs are legacy paths
}

// Option tunes a Store.
type Option func(*Store)

// WithLocalSigner signs links to private files served from the media volume (local mode, and
// old private paths not yet relinked), matching httpware.StaticMedia's Signer.
func WithLocalSigner(s *httpware.MediaSigner) Option { return func(st *Store) { st.signer = s } }

// WithLegacyPrivate marks which old "/media/<rel>" paths hold private files (for example KYC), so
// they are only served with a signed link. Default: none.
func WithLegacyPrivate(f func(rel string) bool) Option {
	return func(st *Store) { st.legacyPrivate = f }
}

// WithLegacyBaseURL names other origins a service used to put in front of its "/media/..." paths,
// so a stored full URL "<base>/media/<rel>" is read as the legacy path "/media/<rel>". The
// configured MEDIA_URL_BASE is always one of them. Empty values are ignored.
func WithLegacyBaseURL(bases ...string) Option {
	return func(st *Store) {
		for _, b := range bases {
			if b = strings.TrimRight(strings.TrimSpace(b), "/"); b != "" {
				st.legacyBases = append(st.legacyBases, strings.ToLower(b))
			}
		}
	}
}

// New builds the store for service (the first key segment, e.g. "inventory"). With neither object
// storage nor MEDIA_ROOT configured it returns a store whose operations fail with ErrNotConfigured.
func New(service string, cfg Config, opts ...Option) (*Store, error) {
	if !validSegment(service) {
		return nil, fmt.Errorf("objectstore: invalid service %q", service)
	}
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 15 * time.Minute
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")
	cfg.LocalURLBase = strings.TrimRight(cfg.LocalURLBase, "/")
	st := &Store{service: service, cfg: cfg, legacyPrivate: func(string) bool { return false }}
	for _, o := range opts {
		o(st)
	}
	if cfg.Remote() {
		c, err := newS3Client(cfg)
		if err != nil {
			return nil, err
		}
		st.s3 = c
	}
	return st, nil
}

// Remote reports whether files go to object storage (false: local directory or nothing).
func (s *Store) Remote() bool { return s != nil && s.s3 != nil }

// Enabled reports whether the store can save files at all.
func (s *Store) Enabled() bool { return s != nil && (s.s3 != nil || s.cfg.LocalRoot != "") }

// Check is a readiness probe: both buckets reachable with the configured credentials (remote), or
// the media root writable (local). An unconfigured store reports nil, as there is nothing to
// reach.
func (s *Store) Check(ctx context.Context) error {
	switch {
	case s == nil || !s.Enabled():
		return nil
	case s.s3 != nil:
		if err := s.s3.headBucket(ctx, s.cfg.PrivateBucket); err != nil {
			return err
		}
		return s.s3.headBucket(ctx, s.cfg.PublicBucket)
	default:
		if err := os.MkdirAll(s.cfg.LocalRoot, 0o755); err != nil {
			return fmt.Errorf("objectstore: media root: %w", err)
		}
		return nil
	}
}

// MediaHandler is what a service mounts at /media:
//
//	if h := store.MediaHandler(); h != nil {
//		r.Handle("/media/*", http.StripPrefix("/media", h))
//	}
//
// It serves the media volume (MEDIA_ROOT): new files in local mode, and old paths not yet relinked
// in both modes, private ones only with a link signed by WithLocalSigner. With object storage, a
// key of a public type is permanently redirected to its public URL; a private key answers 404 (it is
// only reachable through a fresh presigned link). It returns nil when there is nothing to serve.
func (s *Store) MediaHandler() http.Handler {
	if s == nil || !s.Enabled() {
		return nil
	}
	var files http.Handler
	if s.cfg.LocalRoot != "" {
		files = httpware.StaticMedia(s.cfg.LocalRoot, httpware.MediaOptions{Private: s.privatePath, Signer: s.signer})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if s.s3 != nil {
			if info, err := parseKey(rel); err == nil {
				if !info.public || s.cfg.PublicBaseURL == "" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Cache-Control", "public, max-age=86400")
				http.Redirect(w, r, s.cfg.PublicBaseURL+"/"+awsURIEncode(rel, true), http.StatusMovedPermanently)
				return
			}
		}
		if files == nil || (s.signer == nil && s.privatePath(rel)) {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// privatePath reports whether a path below the media root holds a private file.
func (s *Store) privatePath(name string) bool {
	rel := strings.TrimPrefix(path.Clean("/"+name), "/")
	if info, err := parseKey(rel); err == nil {
		return !info.public
	}
	return s.legacyPrivate(rel)
}

// Key builds the key of a named object, for callers that choose the name (a content hash, a record
// id), e.g. Key(Owner{TenantID: t}, TypeDocuments, "invoices", id, hash+".pdf").
func (s *Store) Key(owner Owner, mediaType string, parts ...string) (string, error) {
	return buildKey(s.service, owner, mediaType, parts...)
}

// NewKey builds a fresh key for an upload: .../<media_type>/<yyyy>/<mm>/<uuid><ext>.
func (s *Store) NewKey(owner Owner, mediaType, ext string) (string, error) {
	return buildKey(s.service, owner, mediaType, newObjectName(ext))
}

// Put stores data at key. The media type in the key picks the bucket and cache headers.
func (s *Store) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if !s.Enabled() {
		return ErrNotConfigured
	}
	info, err := parseKey(key)
	if err != nil {
		return err
	}
	if s.s3 != nil {
		// Keys are unique per upload (a uuid or a content hash), so a public file never changes
		// under its URL and may be cached for a year.
		cc := "private, no-store"
		if info.public {
			cc = "public, max-age=31536000, immutable"
		}
		return s.s3.put(ctx, s.bucket(info), key, data, contentType, cc)
	}
	p, err := s.localPath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("objectstore: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("objectstore: %w", err)
	}
	return os.Rename(tmp, p)
}

// Get returns the stored bytes and content type of a key, or of a legacy path (read from the
// media volume). Local files report no content type.
func (s *Store) Get(ctx context.Context, ref string) ([]byte, string, error) {
	if !s.Enabled() {
		return nil, "", ErrNotConfigured
	}
	r, err := s.resolve(ref)
	if err != nil {
		return nil, "", err
	}
	if r.remote() {
		return s.s3.get(ctx, s.bucket(r.info), r.key)
	}
	b, err := os.ReadFile(r.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNotFound
	}
	return b, "", err
}

// Open streams a stored file without holding it in memory (large documents, e-books). The caller
// closes the reader. size is -1 when unknown; local files report no content type.
func (s *Store) Open(ctx context.Context, ref string) (io.ReadCloser, int64, string, error) {
	if !s.Enabled() {
		return nil, 0, "", ErrNotConfigured
	}
	r, err := s.resolve(ref)
	if err != nil {
		return nil, 0, "", err
	}
	if r.remote() {
		return s.s3.open(ctx, s.bucket(r.info), r.key)
	}
	f, err := os.Open(r.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, "", ErrNotFound
	}
	if err != nil {
		return nil, 0, "", err
	}
	size := int64(-1)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	return f, size, "", nil
}

// Serve writes a stored file to the response with range support, so PDF, EPUB and audio readers
// can seek. A remote file streams from the bucket with the client's Range header passed on; a
// local file goes through http.ServeContent. contentType is used when the store reports none.
// ErrNotFound is returned before anything is written, so the caller can answer 404; an error
// after the headers went out (a client that hung up) is returned for logging only.
func (s *Store) Serve(w http.ResponseWriter, r *http.Request, ref, contentType string) error {
	if !s.Enabled() {
		return ErrNotConfigured
	}
	res, err := s.resolve(ref)
	if err != nil {
		return err
	}
	if !res.remote() {
		f, err := os.Open(res.file)
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		http.ServeContent(w, r, path.Base(res.file), st.ModTime(), f)
		return nil
	}
	resp, err := s.s3.fetch(r.Context(), s.bucket(res.info), res.key, r.Header.Get("Range"))
	if err != nil {
		return err
	}
	h := w.Header()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		if cr := resp.Header.Get("Content-Range"); cr != "" {
			h.Set("Content-Range", cr)
		}
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return nil
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" && ct != "application/octet-stream" {
		contentType = ct
	}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	for _, name := range []string{"Content-Length", "Content-Range", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	h.Set("Accept-Ranges", "bytes")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return nil
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// Exists reports whether ref is stored.
func (s *Store) Exists(ctx context.Context, ref string) (bool, error) {
	if !s.Enabled() {
		return false, ErrNotConfigured
	}
	r, err := s.resolve(ref)
	if err != nil {
		return false, err
	}
	if r.remote() {
		return s.s3.exists(ctx, s.bucket(r.info), r.key)
	}
	_, err = os.Stat(r.file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Delete removes ref; a missing file is not an error. External URLs are ignored. Check Owned
// first when the file may be shared by other records.
func (s *Store) Delete(ctx context.Context, ref string) error {
	ref = s.Normalize(ref)
	if isExternal(ref) || ref == "" {
		return nil
	}
	if !s.Enabled() {
		return ErrNotConfigured
	}
	r, err := s.resolve(ref)
	if err != nil {
		return err
	}
	if r.remote() {
		return s.s3.del(ctx, s.bucket(r.info), r.key)
	}
	if err := os.Remove(r.file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// URL turns a stored reference into a link: a key of a public type gives the public URL, a private
// one a signed link valid for PresignTTL; a legacy "/media/..." path gives its address on the
// service's media route; a full http(s) URL is returned as is; "" stays "". A value that is none of
// these (e.g. an icon name kept in the same field) is returned unchanged, as is everything when no
// storage is configured.
func (s *Store) URL(ref string) string {
	u, err := s.link(ref, "", 0)
	if err != nil {
		return strings.TrimSpace(ref)
	}
	return u
}

// DownloadURL is a signed link (any type) that downloads as name, valid for ttl (0: PresignTTL).
func (s *Store) DownloadURL(ref, name string, ttl time.Duration) (string, error) {
	return s.link(ref, name, ttl)
}

func (s *Store) link(ref, downloadName string, ttl time.Duration) (string, error) {
	ref = s.Normalize(ref)
	if ref == "" || isExternal(ref) {
		return ref, nil
	}
	if s == nil || !s.Enabled() {
		return "", ErrNotConfigured
	}
	r, err := s.resolve(ref)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = s.cfg.PresignTTL
	}
	if r.remote() {
		if r.info.public && downloadName == "" && s.cfg.PublicBaseURL != "" {
			return s.cfg.PublicBaseURL + "/" + awsURIEncode(r.key, true), nil
		}
		return s.s3.presignGet(s.bucket(r.info), r.key, downloadName, ttl), nil
	}
	// A file on the media volume, served at <LocalURLBase>/media by MediaHandler.
	u := s.cfg.LocalURLBase + LegacyPrefix + awsURIEncode(r.rel, true)
	if !r.info.public && s.signer != nil {
		u = s.signer.Sign(u)
	}
	return u, nil
}

// resolved is where a reference lives: an object (remote mode key) or a file on the media volume
// (local mode key, or a legacy path in either mode).
type resolved struct {
	key  string // object key, "" for a legacy path
	rel  string // path below the media root (local files)
	file string // absolute local path (local files)
	info keyInfo
	s3   bool
}

func (r resolved) remote() bool { return r.s3 }

// resolve maps a reference to where it is stored. Keys of this or any service are accepted, so a
// service can read a file another one stored when handed its key.
func (s *Store) resolve(ref string) (resolved, error) {
	ref = s.Normalize(ref)
	if strings.HasPrefix(ref, LegacyPrefix) {
		rel := strings.SplitN(strings.TrimPrefix(ref, LegacyPrefix), "?", 2)[0]
		if dec, err := url.PathUnescape(rel); err == nil {
			rel = dec
		}
		if s.cfg.LocalRoot == "" {
			return resolved{}, fmt.Errorf("objectstore: %s is on the media volume but MEDIA_ROOT is not set", ref)
		}
		file, err := s.localPath(rel)
		if err != nil {
			return resolved{}, err
		}
		rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
		return resolved{rel: rel, file: file, info: keyInfo{public: !s.legacyPrivate(rel)}}, nil
	}
	info, err := parseKey(ref)
	if err != nil {
		return resolved{}, err
	}
	if s.s3 != nil {
		return resolved{key: ref, info: info, s3: true}, nil
	}
	file, err := s.localPath(ref)
	if err != nil {
		return resolved{}, err
	}
	return resolved{key: ref, rel: ref, file: file, info: info}, nil
}

// IsLegacy reports whether ref (in any form Normalize accepts) is an old media volume path that a
// Relinker should move.
func (s *Store) IsLegacy(ref string) bool {
	return strings.HasPrefix(s.Normalize(ref), LegacyPrefix)
}

// Owned reports whether ref (in any form Normalize accepts) is a key this service stored. Use it
// before deleting a file when a record is removed: legacy files and external URLs may be shared by
// several records and are left alone.
func (s *Store) Owned(ref string) bool {
	if s == nil {
		return false
	}
	ref = s.Normalize(ref)
	if !strings.HasPrefix(ref, s.service+"/") {
		return false
	}
	_, err := parseKey(ref)
	return err == nil
}

// Normalize turns a value a client sends back into what should be stored: a public URL or a
// presigned link of this store becomes its key, a full media URL of this service (MEDIA_URL_BASE or
// WithLegacyBaseURL) becomes its key or relative "/media/..." path, and anything else (a key, a
// legacy path, an external URL, "") is returned as it is. Use it before saving file fields.
func (s *Store) Normalize(ref string) string {
	ref = strings.TrimSpace(ref)
	if s == nil || !isExternal(ref) || strings.HasPrefix(strings.ToLower(ref), "data:") {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	p, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return ref
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	if s.cfg.PublicBaseURL != "" && origin == originOf(s.cfg.PublicBaseURL) {
		if key := strings.TrimPrefix(p, "/"); key != "" {
			if _, err := parseKey(key); err == nil {
				return key
			}
		}
	}
	if s.s3 != nil && origin == strings.ToLower(s.s3.endpoint.Scheme+"://"+s.s3.endpoint.Host) {
		for _, b := range []string{s.cfg.PublicBucket, s.cfg.PrivateBucket} {
			if key := strings.TrimPrefix(p, "/"+b+"/"); key != p {
				if _, err := parseKey(key); err == nil {
					return key
				}
			}
		}
	}
	for _, b := range append([]string{strings.ToLower(s.cfg.LocalURLBase)}, s.legacyBases...) {
		if b == "" || origin != originOf(b) {
			continue
		}
		basePath := strings.TrimSuffix(strings.TrimPrefix(b, originOf(b)), "/")
		rest := strings.TrimPrefix(p, basePath)
		if !strings.HasPrefix(rest, LegacyPrefix) {
			continue
		}
		// A key served by MediaHandler (local mode, or its redirect), else an old path.
		if key := strings.TrimPrefix(rest, LegacyPrefix); key != "" {
			if _, err := parseKey(key); err == nil {
				return key
			}
		}
		return rest
	}
	return ref
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

func (s *Store) bucket(info keyInfo) string {
	if info.public {
		return s.cfg.PublicBucket
	}
	return s.cfg.PrivateBucket
}

// localPath is where a path below the media root lives: LocalRoot/<rel>. Dotfiles and paths
// climbing out of the root are refused.
func (s *Store) localPath(rel string) (string, error) {
	root, err := filepath.Abs(s.cfg.LocalRoot)
	if err != nil {
		return "", err
	}
	for _, seg := range strings.Split(strings.Trim(rel, "/"), "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("objectstore: invalid media path %q", rel)
		}
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("objectstore: path escapes the media root")
	}
	return p, nil
}

func isExternal(ref string) bool {
	l := strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "data:")
}
