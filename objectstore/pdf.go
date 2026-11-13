package objectstore

import (
	"bytes"
	"compress/zlib"
	"io"
)

// PDF names that make a file run something or carry another file: scripts (/JavaScript, /JS,
// which also covers an /OpenAction or /AA that runs a script), program launches and embedded
// files. A PDF carrying any of them is refused; everything else is stored as uploaded.
var pdfActiveNames = map[string]bool{
	"JavaScript":    true,
	"JS":            true,
	"Launch":        true,
	"EmbeddedFile":  true,
	"EmbeddedFiles": true,
}

var pdfObjStm = map[string]bool{"ObjStm": true}

// Bounds of the scan: object streams (which can hide the names above in compressed form) are
// inflated up to these limits, so a crafted file cannot make the check expensive.
const (
	pdfMaxStreams        = 4096
	pdfMaxStreamInflated = 8 * mb
	pdfMaxTotalInflated  = 64 * mb
)

// pdfActiveContent returns the first active name found in the PDF, or "". It reads the object
// syntax between streams and the inflated content of object streams (/Type /ObjStm). Other
// stream bodies (images, fonts, page content) are skipped: their bytes are data, and a byte scan
// over compressed pixels would find "/JS" by chance in large files.
func pdfActiveContent(raw []byte) string {
	total, streams, pos := 0, 0, 0
	for pos < len(raw) {
		at := pdfNextStream(raw, pos)
		if at < 0 {
			return pdfFindName(raw[pos:], pdfActiveNames)
		}
		dict := raw[pos:at]
		if n := pdfFindName(dict, pdfActiveNames); n != "" {
			return n
		}
		start := at + len("stream")
		if start < len(raw) && raw[start] == '\r' {
			start++
		}
		if start < len(raw) && raw[start] == '\n' {
			start++
		}
		end := len(raw)
		if e := bytes.Index(raw[start:], []byte("endstream")); e >= 0 {
			end = start + e
		}
		// The stream's own dictionary follows the last "obj" keyword before it.
		if o := bytes.LastIndex(dict, []byte("obj")); o >= 0 {
			dict = dict[o:]
		}
		if pdfFindName(dict, pdfObjStm) != "" && streams < pdfMaxStreams && total < pdfMaxTotalInflated &&
			end-start > 2 && raw[start] == 0x78 {
			streams++
			if zr, err := zlib.NewReader(bytes.NewReader(raw[start:end])); err == nil {
				out, _ := io.ReadAll(io.LimitReader(zr, int64(min(pdfMaxStreamInflated, pdfMaxTotalInflated-total))))
				_ = zr.Close()
				total += len(out)
				if n := pdfFindName(out, pdfActiveNames); n != "" {
					return n
				}
			}
		}
		pos = end + len("endstream")
	}
	return ""
}

// pdfNextStream finds the next "stream" keyword (not the end of "endstream") at or after pos.
func pdfNextStream(raw []byte, pos int) int {
	for pos < len(raw) {
		i := bytes.Index(raw[pos:], []byte("stream"))
		if i < 0 {
			return -1
		}
		at := pos + i
		if at >= 3 && string(raw[at-3:at]) == "end" {
			pos = at + len("stream")
			continue
		}
		return at
	}
	return -1
}

// pdfFindName walks the PDF names (/Name) in b, decoding #xx escapes (/J#61vaScript is
// /JavaScript), and returns the first one in set.
func pdfFindName(b []byte, set map[string]bool) string {
	for i := 0; i < len(b); i++ {
		if b[i] != '/' {
			continue
		}
		j := i + 1
		for j < len(b) && !pdfDelimiter(b[j]) && j-i < 128 {
			j++
		}
		if name := pdfDecodeName(b[i+1 : j]); set[name] {
			return name
		}
		i = j - 1
	}
	return ""
}

func pdfDelimiter(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\f', 0, '/', '(', ')', '<', '>', '[', ']', '{', '}', '%':
		return true
	}
	return false
}

func pdfDecodeName(n []byte) string {
	if bytes.IndexByte(n, '#') < 0 {
		return string(n)
	}
	out := make([]byte, 0, len(n))
	for k := 0; k < len(n); k++ {
		if n[k] == '#' && k+2 < len(n) {
			h, ok1 := unhex(n[k+1])
			l, ok2 := unhex(n[k+2])
			if ok1 && ok2 {
				out = append(out, h<<4|l)
				k += 2
				continue
			}
		}
		out = append(out, n[k])
	}
	return string(out)
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
