package objectstore

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ownerUsers  = "users"
	ownerLegacy = "legacy"
)

var segmentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validSegment(s string) bool { return segmentRe.MatchString(s) && !strings.Contains(s, "..") }

// Owner is who a file belongs to: a tenant (TenantID) or, for personal media such as an avatar,
// a user (UserID). Exactly one is set.
type Owner struct {
	TenantID string
	UserID   string
}

func (o Owner) segments() ([]string, error) {
	switch {
	case o.TenantID != "" && o.UserID == "":
		if !validSegment(o.TenantID) {
			return nil, fmt.Errorf("objectstore: invalid tenant id %q", o.TenantID)
		}
		return []string{o.TenantID}, nil
	case o.UserID != "" && o.TenantID == "":
		if !validSegment(o.UserID) {
			return nil, fmt.Errorf("objectstore: invalid user id %q", o.UserID)
		}
		return []string{ownerUsers, o.UserID}, nil
	default:
		return nil, fmt.Errorf("objectstore: an owner needs exactly one of tenant or user")
	}
}

// buildKey joins service, owner, media type and name parts into a key, validating every segment so
// a key can never climb out of its prefix.
func buildKey(service string, owner Owner, mediaType string, parts ...string) (string, error) {
	if !validSegment(service) {
		return "", fmt.Errorf("objectstore: invalid service %q", service)
	}
	if _, err := mustLookup(mediaType); err != nil {
		return "", err
	}
	segs, err := owner.segments()
	if err != nil {
		return "", err
	}
	out := append([]string{service}, segs...)
	out = append(out, mediaType)
	for _, p := range parts {
		for _, s := range strings.Split(p, "/") {
			if !validSegment(s) {
				return "", fmt.Errorf("objectstore: invalid key part %q", p)
			}
			out = append(out, s)
		}
	}
	return strings.Join(out, "/"), nil
}

// newObjectName is a fresh, unguessable name for an upload: <yyyy>/<mm>/<uuid><ext>.
func newObjectName(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if !validSegment("x" + ext) {
		ext = ""
	}
	now := time.Now().UTC()
	return fmt.Sprintf("%04d/%02d/%s%s", now.Year(), now.Month(), uuid.NewString(), ext)
}

// keyInfo describes a key: whether it is a legacy copy, and its media type and visibility.
type keyInfo struct {
	mediaType string
	public    bool
}

// parseKey reads the media type (and so the bucket) from a key built by this package, or from a
// legacy key <service>/legacy/<public|private>/<old path>.
func parseKey(key string) (keyInfo, error) {
	segs := strings.Split(key, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return keyInfo{}, fmt.Errorf("objectstore: invalid key %q", key)
		}
	}
	if len(segs) >= 4 && segs[1] == ownerLegacy {
		switch segs[2] {
		case "public":
			return keyInfo{public: true}, nil
		case "private":
			return keyInfo{}, nil
		}
		return keyInfo{}, fmt.Errorf("objectstore: invalid legacy key %q", key)
	}
	typeAt := 2
	if len(segs) > 1 && segs[1] == ownerUsers {
		typeAt = 3
	}
	if len(segs) <= typeAt+1 {
		return keyInfo{}, fmt.Errorf("objectstore: invalid key %q", key)
	}
	t, err := mustLookup(segs[typeAt])
	if err != nil {
		return keyInfo{}, err
	}
	return keyInfo{mediaType: t.Name, public: t.Public}, nil
}
