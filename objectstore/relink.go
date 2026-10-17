package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"
	"sync"
)

// Relinker moves files that rows still reference by an old media volume path ("/media/..." or a
// full URL on the service's media route) to object storage, so each row can be updated to the
// file's key. Files no row references are never moved; they go away with the volume.
//
// Each file is stored under its owner and media type with a content hash name, so the same file
// referenced by several rows of one owner is stored once, and running a relink again (after an
// interruption) uploads nothing twice. Safe for concurrent use.
type Relinker struct {
	s  *Store
	mu sync.Mutex
	// done maps "<owner key prefix>|<old path>" to the key it was moved to in this run.
	done map[string]string

	Moved   int // files uploaded
	Reused  int // rows pointed at a file already uploaded (this run or an earlier one)
	Missing int // rows whose old file is gone from the volume
}

// NewRelinker starts a relink run.
func (s *Store) NewRelinker() *Relinker { return &Relinker{s: s, done: map[string]string{}} }

// Relink returns the value a row should store for ref and whether it changed. A value that is not
// an old media volume path (already a key, an external URL, empty) is returned unchanged. An old
// path whose file is missing returns ErrNotFound; leave that row as it is and report it.
func (r *Relinker) Relink(ctx context.Context, ref string, owner Owner, mediaType string) (string, bool, error) {
	s := r.s
	norm := s.Normalize(ref)
	if !strings.HasPrefix(norm, LegacyPrefix) {
		return ref, false, nil
	}
	if !s.Enabled() {
		return ref, false, ErrNotConfigured
	}
	prefix, err := s.Key(owner, mediaType, "x")
	if err != nil {
		return ref, false, err
	}
	memo := strings.TrimSuffix(prefix, "x") + "|" + norm
	r.mu.Lock()
	if key, ok := r.done[memo]; ok {
		r.Reused++
		r.mu.Unlock()
		return key, true, nil
	}
	r.mu.Unlock()

	data, _, err := s.Get(ctx, norm)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			r.mu.Lock()
			r.Missing++
			r.mu.Unlock()
		}
		return ref, false, err
	}
	ct := sniff(data)
	ext := strings.ToLower(path.Ext(strings.SplitN(norm, "?", 2)[0]))
	if ext == "" || !validSegment("x"+ext) {
		ext = extFor(ct)
	}
	sum := sha256.Sum256(data)
	key, err := s.Key(owner, mediaType, hex.EncodeToString(sum[:16])+ext)
	if err != nil {
		return ref, false, err
	}
	exists, err := s.Exists(ctx, key)
	if err != nil {
		return ref, false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if exists {
		r.Reused++
	} else {
		if err := s.Put(ctx, key, data, ct); err != nil {
			return ref, false, err
		}
		r.Moved++
	}
	r.done[memo] = key
	return key, true, nil
}
