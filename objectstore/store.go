package objectstore

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Bengo-Hub/httpware"
)

// LegacyPrefix is how services stored local media paths before object storage ("/media/...").
const LegacyPrefix = "/media/"

// Store reads and writes one service's files. It is safe for concurrent use.
type Store struct {
	service string
	cfg     Config
	s3      *s3Client // nil in local mode

	signer        *httpware.MediaSigner
	legacyPrivate func(rel string) bool
	legacyKeys    bool
}

// Option tunes a Store.
type Option func(*Store)

// WithLocalSigner signs private links in local mode, matching httpware.StaticMedia's Signer.
func WithLocalSigner(s *httpware.MediaSigner) Option { return func(st *Store) { st.signer = s } }

// WithLegacyPrivate marks which old "/media/<rel>" paths held private files (for example KYC),
// so they resolve to the private bucket after the one-time copy. Default: none.
func WithLegacyPrivate(f func(rel string) bool) Option {
	return func(st *Store) { st.legacyPrivate = f }
}

// WithLegacyKeys treats a stored value that is neither a key of this layout nor a "/media/" path
// (for example an object key a service used before, "documents/<tenant>/bills/x.pdf") as a legacy
// path, so it resolves to <service>/legacy/<public|private>/<value> once copied there.
func WithLegacyKeys() Option { return func(st *Store) { st.legacyKeys = true } }

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
	path, err := s.localPath(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("objectstore: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("objectstore: %w", err)
	}
	return os.Rename(tmp, path)
}

// Get returns the stored bytes and content type. ref may be a key or a legacy "/media/..." path.
func (s *Store) Get(ctx context.Context, ref string) ([]byte, string, error) {
	if !s.Enabled() {
		return nil, "", ErrNotConfigured
	}
	key, info, err := s.resolve(ref)
	if err != nil {
		return nil, "", err
	}
	if s.s3 != nil {
		return s.s3.get(ctx, s.bucket(info), key)
	}
	path, err := s.localPathRef(ref, key)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNotFound
	}
	return b, "", err
}

// Exists reports whether ref is stored.
func (s *Store) Exists(ctx context.Context, ref string) (bool, error) {
	if !s.Enabled() {
		return false, ErrNotConfigured
	}
	key, info, err := s.resolve(ref)
	if err != nil {
		return false, err
	}
	if s.s3 != nil {
		return s.s3.exists(ctx, s.bucket(info), key)
	}
	path, err := s.localPathRef(ref, key)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Delete removes ref; a missing file is not an error. Full http URLs (not ours) are ignored.
func (s *Store) Delete(ctx context.Context, ref string) error {
	if isExternal(ref) || strings.TrimSpace(ref) == "" {
		return nil
	}
	if !s.Enabled() {
		return ErrNotConfigured
	}
	key, info, err := s.resolve(ref)
	if err != nil {
		return err
	}
	if s.s3 != nil {
		return s.s3.del(ctx, s.bucket(info), key)
	}
	path, err := s.localPathRef(ref, key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// URL turns a stored reference into a link: a key of a public type gives the public URL, a private
// one a signed link valid for PresignTTL; a legacy "/media/..." path resolves to its copy; a full
// http(s) URL is returned as is; "" stays "".
func (s *Store) URL(ref string) string {
	u, _ := s.link(ref, "", 0)
	return u
}

// DownloadURL is a signed link (any type) that downloads as name, valid for ttl (0: PresignTTL).
func (s *Store) DownloadURL(ref, name string, ttl time.Duration) (string, error) {
	return s.link(ref, name, ttl)
}

func (s *Store) link(ref, downloadName string, ttl time.Duration) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || isExternal(ref) {
		return ref, nil
	}
	if s == nil || !s.Enabled() {
		return "", ErrNotConfigured
	}
	key, info, err := s.resolve(ref)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = s.cfg.PresignTTL
	}
	if s.s3 != nil {
		if info.public && downloadName == "" && s.cfg.PublicBaseURL != "" {
			return s.cfg.PublicBaseURL + "/" + awsURIEncode(key, true), nil
		}
		return s.s3.presignGet(s.bucket(info), key, downloadName, ttl), nil
	}
	// Local: the service serves LocalRoot at <LocalURLBase>/media (httpware.StaticMedia).
	u := s.cfg.LocalURLBase + LegacyPrefix + awsURIEncode(key, true)
	if strings.HasPrefix(ref, LegacyPrefix) {
		u = s.cfg.LocalURLBase + ref
	}
	if !info.public && s.signer != nil {
		u = s.signer.Sign(u)
	}
	return u, nil
}

// LegacyKey is where a file stored at the old local path "/media/<rel>" lives in object storage.
func (s *Store) LegacyKey(rel string) (string, error) {
	rel = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(rel), LegacyPrefix), "/")
	if rel == "" {
		return "", fmt.Errorf("objectstore: empty legacy path")
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("objectstore: invalid legacy path %q", rel)
		}
	}
	vis := "public"
	if s.legacyPrivate(rel) {
		vis = "private"
	}
	return s.service + "/" + ownerLegacy + "/" + vis + "/" + rel, nil
}

// resolve maps a reference to its key and kind. Legacy paths, and keys of this or any service, are
// accepted, so a service can read a file another one stored when handed its key.
func (s *Store) resolve(ref string) (string, keyInfo, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, LegacyPrefix) {
		rel := strings.SplitN(strings.TrimPrefix(ref, LegacyPrefix), "?", 2)[0]
		if dec, err := url.PathUnescape(rel); err == nil {
			rel = dec
		}
		key, err := s.LegacyKey(rel)
		if err != nil {
			return "", keyInfo{}, err
		}
		info, err := parseKey(key)
		return key, info, err
	}
	info, err := parseKey(ref)
	if err != nil && s.legacyKeys && !strings.HasPrefix(ref, s.service+"/") {
		return s.resolve(LegacyPrefix + strings.TrimPrefix(ref, "/"))
	}
	return ref, info, err
}

func (s *Store) bucket(info keyInfo) string {
	if info.public {
		return s.cfg.PublicBucket
	}
	return s.cfg.PrivateBucket
}

// localPath is where key lives in local mode: LocalRoot/<key>.
func (s *Store) localPath(key string) (string, error) {
	root, err := filepath.Abs(s.cfg.LocalRoot)
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, filepath.FromSlash(key))
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", fmt.Errorf("objectstore: key escapes the media root")
	}
	return p, nil
}

// localPathRef is the local file of ref: a legacy path stays where it always was.
func (s *Store) localPathRef(ref, key string) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(ref), LegacyPrefix) {
		rel := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(ref), LegacyPrefix), "?", 2)[0]
		return s.localPath(rel)
	}
	return s.localPath(key)
}

func isExternal(ref string) bool {
	l := strings.ToLower(strings.TrimSpace(ref))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "data:")
}
