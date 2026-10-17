package objectstore

import (
	"fmt"
	"sync"
)

// MediaType is a kind of stored file. It decides the bucket, the upload limits and how the file is
// cached. The name is the third key segment, so a key alone says how to serve it.
type MediaType struct {
	Name string
	// Public files are served from the public bucket's domain; the rest only by signed link.
	Public bool
	// MaxBytes caps an upload (ReadUpload).
	MaxBytes int64
	// Allowed lists the accepted content types (sniffed from the bytes). Empty accepts any.
	Allowed []string
	// Image re-encodes JPEG and PNG uploads, which strips EXIF (location, device) and anything
	// hidden in the file.
	Image bool
}

const (
	mb = 1 << 20

	TypeAvatars       = "avatars"
	TypeLogos         = "logos"
	TypeItemImages    = "item-images"
	TypeMenuImages    = "menu-images"
	TypeCovers        = "covers"
	TypeImages        = "images" // other public pictures (rooms, properties, catalogue)
	TypeAttachments   = "attachments"
	TypeDocuments     = "documents"
	TypeKYC           = "kyc"
	TypePatientPhotos = "patient-photos"
	TypeSignatures    = "signatures"
	TypeExports       = "exports"
	TypeBackups       = "backups"
	TypeEbooks        = "ebooks"
)

var imageTypes = []string{"image/jpeg", "image/png"}

var (
	registryMu sync.RWMutex
	registry   = map[string]MediaType{}
)

func init() {
	for _, t := range []MediaType{
		{Name: TypeAvatars, Public: true, MaxBytes: 2 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeLogos, Public: true, MaxBytes: 2 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeItemImages, Public: true, MaxBytes: 2 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeMenuImages, Public: true, MaxBytes: 2 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeCovers, Public: true, MaxBytes: 2 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeImages, Public: true, MaxBytes: 5 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeAttachments, MaxBytes: 10 * mb},
		{Name: TypeDocuments, MaxBytes: 20 * mb},
		{Name: TypeKYC, MaxBytes: 8 * mb, Allowed: append([]string{"application/pdf"}, imageTypes...), Image: true},
		{Name: TypePatientPhotos, MaxBytes: 5 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeSignatures, MaxBytes: 1 * mb, Allowed: imageTypes, Image: true},
		{Name: TypeExports, MaxBytes: 200 * mb},
		{Name: TypeBackups, MaxBytes: 2048 * mb},
		{Name: TypeEbooks, MaxBytes: 100 * mb, Allowed: []string{"application/pdf", "application/epub+zip", "application/zip"}},
	} {
		registry[t.Name] = t
	}
}

// Register adds or replaces a media type, for a service that needs one the platform list lacks.
// Call it at startup.
func Register(t MediaType) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[t.Name] = t
}

// Lookup returns a registered media type.
func Lookup(name string) (MediaType, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	t, ok := registry[name]
	return t, ok
}

func mustLookup(name string) (MediaType, error) {
	t, ok := Lookup(name)
	if !ok {
		return MediaType{}, fmt.Errorf("objectstore: unknown media type %q", name)
	}
	return t, nil
}
