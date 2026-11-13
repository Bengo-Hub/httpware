package objectstore

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG decoder
	"image/png"
	"io"
	"net/http"
	"strings"

	"github.com/gen2brain/webp" // WebP encoder and decoder (libwebp transpiled to Go, no cgo)
	xdraw "golang.org/x/image/draw"
)

// Upload is a validated file ready to store.
type Upload struct {
	Data        []byte
	ContentType string
	Ext         string // with the dot, from the content type
	FileName    string // as the client sent it, for display only
	// Scan is the malware scan result. Record Scan.ScannedAt and Scan.Engine with the file
	// (scanned_at, scan_engine) when Scan.Status is ScanStatusClean.
	Scan ScanResult
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

// Prepare validates bytes from any source (an upload, an import, a URL fetch) against a media type,
// the same rules ReadUpload applies:
//   - size limit and allow-list, by the type sniffed from the bytes;
//   - a PDF carrying JavaScript, a launch action or an embedded file is refused;
//   - the bytes as uploaded go to the malware scanner (clamd) under the type's ScanPolicy; the
//     result is on Upload.Scan for the caller to record (scanned_at, engine);
//   - images are downscaled to the type's MaxSide and stored as WebP (PNG for a signature that
//     uses transparency), which also drops EXIF and anything appended to the file.
//
// An infected or refused file is an upload error (400). A fail closed type with the scanner down
// returns an error wrapping ErrScanUnavailable (answer 503).
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
	if ct == "application/pdf" {
		if name := pdfActiveContent(raw); name != "" {
			return nil, uploadErr("this PDF is not accepted: it carries scripts, launch actions or embedded files (/%s)", name)
		}
	}
	scan, err := scanUpload(t, raw)
	if err != nil {
		return nil, err
	}
	if t.Image && (ct == "image/jpeg" || ct == "image/png" || ct == "image/webp") {
		clean, outCT, err := reencode(raw, t)
		if err != nil {
			return nil, uploadErr("the image could not be read")
		}
		raw, ct = clean, outCT
	}
	return &Upload{Data: raw, ContentType: ct, Ext: extFor(ct), Scan: scan}, nil
}

// webpQuality is the lossy WebP quality images are stored at.
const webpQuality = 82

// reencode decodes the image, downscales it to the type's MaxSide and writes it again as WebP (or
// PNG for a transparent image of a KeepTransparentPNG type), dropping metadata (EXIF location,
// device) and any payload appended to the file. It returns the bytes and their content type.
func reencode(raw []byte, t MediaType) ([]byte, string, error) {
	// Check the dimensions before decoding, so a small file claiming a huge canvas cannot
	// exhaust memory.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 10000 || cfg.Height > 10000 || cfg.Width*cfg.Height > 40_000_000 {
		return nil, "", errors.New("image too large")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	img = downscale(img, maxSide(t))
	var buf bytes.Buffer
	if (t.KeepTransparentPNG || t.Name == TypeSignatures) && !opaque(img) {
		err = png.Encode(&buf, img)
		return buf.Bytes(), "image/png", err
	}
	err = webp.Encode(&buf, img, webp.Options{Quality: webpQuality, Method: 4})
	return buf.Bytes(), "image/webp", err
}

// downscale shrinks img so its longest edge is at most side, keeping the aspect ratio.
func downscale(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if side <= 0 || (w <= side && h <= side) {
		return img
	}
	nw, nh := side, side
	if w >= h {
		nh = max(1, int(int64(h)*int64(side)/int64(w)))
	} else {
		nw = max(1, int(int64(w)*int64(side)/int64(h)))
	}
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// opaque reports whether every pixel of img is fully opaque.
func opaque(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

func sniff(raw []byte) string {
	ct := http.DetectContentType(raw)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	if ct == "application/zip" && bytes.Contains(raw[:min(len(raw), 128)], []byte("application/epub+zip")) {
		return "application/epub+zip"
	}
	// Word and Excel files are zips; their part names (in the central directory at the end)
	// say which. A partial read (VerifyUpload's first bytes) stays application/zip.
	if ct == "application/zip" && bytes.Contains(raw, []byte("[Content_Types].xml")) {
		switch {
		case bytes.Contains(raw, []byte("word/document.xml")):
			return officeTypes[0]
		case bytes.Contains(raw, []byte("xl/workbook.xml")):
			return officeTypes[1]
		}
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
	case officeTypes[0]:
		return ".docx"
	case officeTypes[1]:
		return ".xlsx"
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
