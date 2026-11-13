package objectstore

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gen2brain/webp"
)

// eicar is the standard antivirus test string, assembled at run time so the source file itself
// is not flagged by a local scanner.
var eicar = []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$` + "EICAR-STANDARD-" + "ANTIVIRUS-TEST-FILE!$H+H*")

const fakeEngine = "ClamAV 1.4.3/27820/Thu Nov 12 08:24:01 2026"

// fakeClamd speaks enough of the clamd protocol (zVERSION, zINSTREAM) for the client: it
// reports the EICAR string as infected and anything else as clean.
type fakeClamd struct {
	addr   string
	scans  atomic.Int32
	chunks atomic.Int32
}

func newFakeClamd(t *testing.T) *fakeClamd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &fakeClamd{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(c)
		}
	}()
	return f
}

func (f *fakeClamd) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(c)
	cmd, err := r.ReadString(0)
	if err != nil {
		return
	}
	switch cmd {
	case "zVERSION\x00":
		_, _ = io.WriteString(c, fakeEngine+"\x00")
	case "zINSTREAM\x00":
		f.scans.Add(1)
		var data []byte
		for {
			var hdr [4]byte
			if _, err := io.ReadFull(r, hdr[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(hdr[:])
			if n == 0 {
				break
			}
			f.chunks.Add(1)
			chunk := make([]byte, n)
			if _, err := io.ReadFull(r, chunk); err != nil {
				return
			}
			data = append(data, chunk...)
		}
		if bytes.Contains(data, eicar) {
			_, _ = io.WriteString(c, "stream: Eicar-Test-Signature FOUND\x00")
		} else {
			_, _ = io.WriteString(c, "stream: OK\x00")
		}
	default:
		_, _ = io.WriteString(c, "UNKNOWN COMMAND\x00")
	}
}

// downAddr is a local address nothing listens on.
func downAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func useScanner(t *testing.T, s Scanner) {
	t.Helper()
	prev := SetScanner(s)
	t.Cleanup(func() { SetScanner(prev) })
}

func captureScanLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	SetScanLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { SetScanLogger(nil) })
	return &buf
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{200, 30, 30, 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func webpConfig(t *testing.T, data []byte) image.Config {
	t.Helper()
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Fatalf("not a WebP file: % x", data[:min(len(data), 12)])
	}
	cfg, err := webp.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestScanClean(t *testing.T) {
	f := newFakeClamd(t)
	useScanner(t, &ClamdScanner{Addr: f.addr})
	up, err := Prepare([]byte("an ordinary note"), TypeAttachments)
	if err != nil {
		t.Fatal(err)
	}
	if up.Scan.Status != ScanStatusClean || up.Scan.Engine != fakeEngine || up.Scan.ScannedAt.IsZero() {
		t.Errorf("scan result: %+v", up.Scan)
	}
	if f.scans.Load() != 1 {
		t.Errorf("scans = %d, want 1", f.scans.Load())
	}
}

func TestScanInfected(t *testing.T) {
	f := newFakeClamd(t)
	useScanner(t, &ClamdScanner{Addr: f.addr})
	captureScanLog(t)
	if _, err := Prepare(append([]byte("note "), eicar...), TypeAttachments); !IsUploadError(err) {
		t.Errorf("infected attachment must be refused as an upload error: %v", err)
	}
	// A public (fail open) type is still refused when the scanner finds something; the scan sees
	// the bytes as uploaded, before the image is re-encoded.
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	withVirus := append(pngBytes(t, img), eicar...)
	if _, err := Prepare(withVirus, TypeAvatars); !IsUploadError(err) {
		t.Errorf("infected image must be refused: %v", err)
	}
}

func TestScanDownFailClosed(t *testing.T) {
	useScanner(t, &ClamdScanner{Addr: downAddr(t), DialTimeout: time.Second})
	logs := captureScanLog(t)
	sig := pngBytes(t, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	cases := map[string][]byte{
		TypeAttachments: []byte("plain text"),
		TypeDocuments:   []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n"),
		TypeKYC:         []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n"),
		TypeSignatures:  sig,
	}
	for mt, data := range cases {
		_, err := Prepare(data, mt)
		if !errors.Is(err, ErrScanUnavailable) || IsUploadError(err) {
			t.Errorf("%s: scanner down must refuse with ErrScanUnavailable (not a 400): %v", mt, err)
		}
	}
	if !strings.Contains(logs.String(), "clamd_unavailable") {
		t.Errorf("an ops alert must be logged: %s", logs.String())
	}
}

func TestScanDownFailOpenForPublicImages(t *testing.T) {
	useScanner(t, &ClamdScanner{Addr: downAddr(t), DialTimeout: time.Second})
	logs := captureScanLog(t)
	up, err := Prepare(jpegBytes(t, 40, 20), TypeItemImages)
	if err != nil {
		t.Fatalf("public image must be accepted while clamd is down: %v", err)
	}
	if up.Scan.Status != ScanStatusUnavailable || !up.Scan.ScannedAt.IsZero() {
		t.Errorf("scan result: %+v", up.Scan)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "clamd_unavailable") {
		t.Errorf("fail open must log an ops level error: %s", logs.String())
	}
}

func TestScanSkippedTypes(t *testing.T) {
	f := newFakeClamd(t)
	useScanner(t, &ClamdScanner{Addr: f.addr})
	up, err := Prepare([]byte("id,total\n1,2\n"), TypeExports)
	if err != nil || up.Scan.Status != ScanStatusSkipped || f.scans.Load() != 0 {
		t.Errorf("exports are not scanned: %+v %v (scans %d)", up, err, f.scans.Load())
	}
	useScanner(t, nil)
	up, err = Prepare([]byte("plain text"), TypeAttachments)
	if err != nil || up.Scan.Status != ScanStatusSkipped {
		t.Errorf("no scanner configured means skipped: %+v %v", up, err)
	}
}

func TestClamdChunksAndLimit(t *testing.T) {
	f := newFakeClamd(t)
	s := &ClamdScanner{Addr: f.addr, ChunkSize: 1000, MaxBytes: 10_000}
	data := append(bytes.Repeat([]byte("a"), 2500), eicar...)
	v, err := s.Scan(t.Context(), data)
	if err != nil || !v.Infected || v.Signature != "Eicar-Test-Signature" {
		t.Errorf("verdict %+v %v", v, err)
	}
	if f.chunks.Load() != 3 {
		t.Errorf("chunks = %d, want 3", f.chunks.Load())
	}
	if _, err := s.Scan(t.Context(), make([]byte, 10_001)); !errors.Is(err, ErrScanUnavailable) {
		t.Errorf("above MaxBytes must not be sent: %v", err)
	}
}

func TestClamdFromEnv(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("CLAMD_ADDR", "")
	if ClamdFromEnv() != nil {
		t.Error("outside Kubernetes with no CLAMD_ADDR scanning is off")
	}
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	if s := ClamdFromEnv(); s == nil || s.Addr != DefaultClamdAddr {
		t.Errorf("in Kubernetes the platform clamd is the default: %+v", s)
	}
	t.Setenv("CLAMD_ADDR", "off")
	if ClamdFromEnv() != nil {
		t.Error("CLAMD_ADDR=off turns scanning off")
	}
	t.Setenv("CLAMD_ADDR", "clamd.local:3310")
	t.Setenv("CLAMD_TIMEOUT", "20s")
	t.Setenv("CLAMD_MAX_BYTES", "1048576")
	s := ClamdFromEnv()
	if s == nil || s.Addr != "clamd.local:3310" || s.Timeout != 20*time.Second || s.MaxBytes != 1<<20 {
		t.Errorf("env config: %+v", s)
	}
}

func TestImagesDownscaledToWebP(t *testing.T) {
	useScanner(t, nil)
	up, err := Prepare(jpegBytes(t, 3000, 1500), TypeItemImages)
	if err != nil {
		t.Fatal(err)
	}
	if up.ContentType != "image/webp" || up.Ext != ".webp" {
		t.Fatalf("item image: %s %s", up.ContentType, up.Ext)
	}
	if cfg := webpConfig(t, up.Data); cfg.Width != 2048 || cfg.Height != 1024 {
		t.Errorf("photo downscaled to %dx%d, want 2048x1024", cfg.Width, cfg.Height)
	}

	logo := image.NewNRGBA(image.Rect(0, 0, 800, 1600))
	logo.Set(5, 5, color.NRGBA{0, 0, 0, 0}) // transparent corner: a logo stays WebP (alpha kept)
	up, err = Prepare(pngBytes(t, logo), TypeLogos)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := webpConfig(t, up.Data); cfg.Width != 600 || cfg.Height != 1200 {
		t.Errorf("logo downscaled to %dx%d, want 600x1200", cfg.Width, cfg.Height)
	}

	small := jpegBytes(t, 300, 200)
	up, err = Prepare(small, TypeAvatars)
	if err != nil {
		t.Fatal(err)
	}
	if cfg := webpConfig(t, up.Data); cfg.Width != 300 || cfg.Height != 200 {
		t.Errorf("a small image keeps its size: %dx%d", cfg.Width, cfg.Height)
	}
}

func TestWebPUploadReencoded(t *testing.T) {
	useScanner(t, nil)
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	img.Set(3, 3, color.RGBA{0, 200, 0, 255})
	var b bytes.Buffer
	if err := webp.Encode(&b, img, webp.Options{Quality: 90, Method: 4}); err != nil {
		t.Fatal(err)
	}
	withTail := append(b.Bytes(), []byte("<script>x</script>")...)
	up, err := Prepare(withTail, TypeImages)
	if err != nil {
		t.Fatal(err)
	}
	if up.ContentType != "image/webp" || bytes.Contains(up.Data, []byte("script")) {
		t.Errorf("webp upload must be re-encoded: %s", up.ContentType)
	}
	if cfg := webpConfig(t, up.Data); cfg.Width != 64 || cfg.Height != 32 {
		t.Errorf("size %dx%d", cfg.Width, cfg.Height)
	}
}

func TestSignaturePNGKeptForTransparency(t *testing.T) {
	useScanner(t, nil)
	sig := image.NewNRGBA(image.Rect(0, 0, 1500, 500)) // fully transparent background
	for x := 100; x < 1400; x++ {
		sig.Set(x, 250, color.NRGBA{0, 0, 120, 255})
	}
	up, err := Prepare(pngBytes(t, sig), TypeSignatures)
	if err != nil {
		t.Fatal(err)
	}
	if up.ContentType != "image/png" || up.Ext != ".png" {
		t.Fatalf("a transparent signature stays PNG: %s", up.ContentType)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(up.Data))
	if err != nil || cfg.Width != 1200 || cfg.Height != 400 {
		t.Errorf("signature downscaled to %dx%d (%v), want 1200x400", cfg.Width, cfg.Height, err)
	}

	// A service that registers its own signatures type (no KeepTransparentPNG, no MaxSide) keeps
	// both platform defaults.
	prev, _ := Lookup(TypeSignatures)
	Register(MediaType{Name: TypeSignatures, MaxBytes: 2 * mb, Allowed: []string{"image/jpeg", "image/png"}, Image: true})
	t.Cleanup(func() { Register(prev) })
	up, err = Prepare(pngBytes(t, sig), TypeSignatures)
	if err != nil || up.ContentType != "image/png" {
		t.Errorf("re-registered signatures keep PNG for transparency: %v", err)
	}

	// An opaque signature (a photographed one) is stored as WebP.
	up, err = Prepare(jpegBytes(t, 900, 300), TypeSignatures)
	if err != nil || up.ContentType != "image/webp" {
		t.Errorf("an opaque signature is WebP: %v", err)
	}
}

func flate(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zlib.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func TestPDFActiveContentRefused(t *testing.T) {
	useScanner(t, nil)
	head := "%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R "
	tail := ">>\nendobj\ntrailer << /Root 1 0 R >>\n%%EOF\n"
	refused := map[string]string{
		"javascript":      head + "/Names << /JavaScript 3 0 R >> " + tail,
		"open action js":  head + "/OpenAction << /S /JavaScript /JS (app.alert\\(1\\)) >> " + tail,
		"js only":         head + "/AA << /O << /JS 4 0 R >> >> " + tail,
		"launch":          head + "/OpenAction << /S /Launch /F (cmd.exe) >> " + tail,
		"embedded file":   head + "/Names << /EmbeddedFiles 5 0 R >> " + tail,
		"embedded stream": head + tail + "6 0 obj << /Type /EmbeddedFile /Length 3 >>\nstream\nabc\nendstream\nendobj\n",
		"escaped name":    head + "/OpenAction << /S /J#61vaScript /J#53 (x) >> " + tail,
	}
	for name, pdf := range refused {
		for _, mt := range []string{TypeDocuments, TypeAttachments, TypeKYC} {
			if _, err := Prepare([]byte(pdf), mt); !IsUploadError(err) {
				t.Errorf("%s (%s): must be refused: %v", name, mt, err)
			}
		}
	}

	// JavaScript hidden in a compressed object stream.
	objStm := flate(t, "7 0 << /S /JavaScript /JS (x) >>")
	hidden := head + tail + "8 0 obj\n<< /Type /ObjStm /N 1 /First 4 /Filter /FlateDecode >>\nstream\n" +
		string(objStm) + "\nendstream\nendobj\n"
	if _, err := Prepare([]byte(hidden), TypeDocuments); !IsUploadError(err) {
		t.Errorf("JavaScript in an object stream must be refused: %v", err)
	}

	accepted := map[string]string{
		"plain":          head + tail,
		"open action go": head + "/OpenAction [3 0 R /Fit] " + tail,
		// Image data is not read as PDF syntax, so "/JS" in pixel bytes is no reason to refuse.
		"image stream": head + tail + "9 0 obj << /Type /XObject /Subtype /Image /Length 8 >>\nstream\n\x00\x01/JS /Launch\x02\nendstream\nendobj\n",
		// A compressed content stream (not an object stream) is not inflated.
		"content stream": head + tail + "10 0 obj << /Filter /FlateDecode >>\nstream\n" + string(flate(t, "/JS ")) + "\nendstream\nendobj\n",
	}
	for name, pdf := range accepted {
		up, err := Prepare([]byte(pdf), TypeDocuments)
		if err != nil || up.ContentType != "application/pdf" || !bytes.Equal(up.Data, []byte(pdf)) {
			t.Errorf("%s: must be stored as uploaded: %v", name, err)
		}
	}
}

func TestAllowListRefusals(t *testing.T) {
	useScanner(t, nil)
	refused := map[string][]byte{
		"html":       []byte("<!DOCTYPE html><html><script>alert(1)</script></html>"),
		"executable": append([]byte("MZ\x90\x00\x03\x00\x00\x00"), make([]byte, 64)...),
		"plain zip":  append([]byte("PK\x03\x04"), []byte("payload.exe and more bytes")...),
		"svg":        []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><script>x</script></svg>`),
	}
	for _, mt := range []string{TypeAttachments, TypeDocuments} {
		for name, data := range refused {
			if _, err := Prepare(data, mt); !IsUploadError(err) {
				t.Errorf("%s: %s must be refused: %v", mt, name, err)
			}
		}
	}
	docx := append([]byte("PK\x03\x04"), []byte("[Content_Types].xml ... word/document.xml ...")...)
	up, err := Prepare(docx, TypeDocuments)
	if err != nil || up.Ext != ".docx" {
		t.Errorf("a Word file is a document: %+v %v", up, err)
	}
	xlsx := append([]byte("PK\x03\x04"), []byte("[Content_Types].xml ... xl/workbook.xml ...")...)
	if up, err := Prepare(xlsx, TypeAttachments); err != nil || up.Ext != ".xlsx" {
		t.Errorf("an Excel file is an attachment: %v", err)
	}
	if up, err := Prepare([]byte("date,amount\n2026-11-12,500\n"), TypeAttachments); err != nil || up.Ext != ".txt" {
		t.Errorf("CSV (sniffed as text) is an attachment: %v", err)
	}
	if _, err := Prepare(jpegBytes(t, 20, 10), TypeAttachments); err != nil {
		t.Errorf("a photo is an attachment: %v", err)
	}
}
