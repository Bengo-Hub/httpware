package objectstore

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
)

// Upload is a validated file ready to store.
type Upload struct {
	Data        []byte
	ContentType string
	Ext         string // with the dot, from the content type
	FileName    string // as the client sent it, for display only
}

// UploadError is a problem with the client's file; its message is safe to show (400).
type UploadError struct{ msg string }

func (e *UploadError) Error() string { return e.msg }

func uploadErr(format string, a ...any) error { return &UploadError{msg: fmt.Sprintf(format, a...)} }

// IsUploadError reports whether err is the client's fault (answer 400) rather than the server's.
func IsUploadError(err error) bool {
	var ue *UploadError
	return errors.As(err, &ue)
}

// ReadUpload reads the multipart file field, enforcing the media type's size limit and accepted
// types (sniffed from the bytes, never trusted from the client), and re-encodes images.
func ReadUpload(w http.ResponseWriter, r *http.Request, field, mediaType string) (*Upload, error) {
	t, err := mustLookup(mediaType)
	if err != nil {
		return nil, err
	}
	r.Body = http.MaxBytesReader(w, r.Body, t.MaxBytes+64<<10)
	if err := r.ParseMultipartForm(min64(t.MaxBytes, 32<<20)); err != nil {
		return nil, uploadErr("send a multipart form with a file of at most %s", humanSize(t.MaxBytes))
	}
	file, header, err := r.FormFile(field)
	if err != nil {
		return nil, uploadErr("no file in field %q", field)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, t.MaxBytes+1))
	if err != nil {
		return nil, uploadErr("the file could not be read")
	}
	up, err := Prepare(raw, mediaType)
	if err != nil {
		return nil, err
	}
	up.FileName = header.Filename
	return up, nil
}

// Prepare validates bytes from any source (an upload, an import, a URL fetch) against a media type
// and re-encodes images, the same rules ReadUpload applies.
func Prepare(raw []byte, mediaType string) (*Upload, error) {
	t, err := mustLookup(mediaType)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, uploadErr("the file is empty")
	}
	if int64(len(raw)) > t.MaxBytes {
		return nil, uploadErr("the file must be at most %s", humanSize(t.MaxBytes))
	}
	ct := sniff(raw)
	if len(t.Allowed) > 0 && !contains(t.Allowed, ct) {
		return nil, uploadErr("this file type is not accepted (%s)", ct)
	}
	if t.Image && (ct == "image/jpeg" || ct == "image/png") {
		clean, err := reencode(raw, ct)
		if err != nil {
			return nil, uploadErr("the image could not be read")
		}
		raw = clean
	}
	return &Upload{Data: raw, ContentType: ct, Ext: extFor(ct)}, nil
}

// reencode decodes and writes the image again, dropping metadata (EXIF location, device) and any
// payload appended to the file.
func reencode(raw []byte, ct string) ([]byte, error) {
	// Check the dimensions before decoding, so a small file claiming a huge canvas cannot
	// exhaust memory.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if cfg.Width > 10000 || cfg.Height > 10000 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, errors.New("image too large")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if ct == "image/png" {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	}
	return buf.Bytes(), err
}

func sniff(raw []byte) string {
	ct := http.DetectContentType(raw)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if ct == "application/zip" && bytes.Contains(raw[:min(len(raw), 128)], []byte("application/epub+zip")) {
		return "application/epub+zip"
	}
	// net/http knows MP4 but not QuickTime: a MOV file opens with an "ftyp" box of brand "qt  ".
	if ct == "application/octet-stream" && len(raw) >= 12 && string(raw[4:12]) == "ftypqt  " {
		return "video/quicktime"
	}
	return ct
}

func extFor(ct string) string {
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "application/epub+zip":
		return ".epub"
	case "application/zip":
		return ".zip"
	case "audio/mpeg":
		return ".mp3"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "application/gzip", "application/x-gzip":
		return ".gz"
	case "text/plain":
		return ".txt"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func humanSize(n int64) string {
	if n >= mb {
		return fmt.Sprintf("%d MB", n/mb)
	}
	return fmt.Sprintf("%d KB", n/1024)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
