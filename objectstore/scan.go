package objectstore

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScanPolicy says what Prepare does with a media type's files and the malware scanner.
type ScanPolicy int

const (
	// ScanAuto is the default: public types fail open, private types fail closed.
	ScanAuto ScanPolicy = iota
	// ScanFailClosed refuses the file while the scanner cannot be reached.
	ScanFailClosed
	// ScanFailOpen accepts the file while the scanner cannot be reached and logs an ops alert.
	ScanFailOpen
	// ScanSkip never scans (files the platform generates itself: exports, backups).
	ScanSkip
)

// Scan statuses recorded on Upload.Scan.
const (
	ScanStatusClean       = "clean"       // the scanner checked the file and found nothing
	ScanStatusUnavailable = "unavailable" // fail open: stored unscanned while the scanner was down
	ScanStatusSkipped     = "skipped"     // no scanner configured, or the type is not scanned
)

// ScanResult is what the malware scan said about an upload. Callers keep ScannedAt and Engine in
// the file's metadata (scanned_at, scan_engine).
type ScanResult struct {
	Status    string
	ScannedAt time.Time // zero unless Status is clean
	// Engine is the clamd version line: engine, signature database version and its date, e.g.
	// "ClamAV 1.4.3/27820/Thu Nov 12 08:24:01 2026". Empty when it could not be read.
	Engine string
}

// ScanVerdict is one scanner answer.
type ScanVerdict struct {
	Infected  bool
	Signature string // the matched signature when Infected
	Engine    string
}

// Scanner checks bytes for malware. ClamdScanner is the platform one.
type Scanner interface {
	Scan(ctx context.Context, data []byte) (ScanVerdict, error)
}

// ErrScanUnavailable wraps every failure to get an answer from the scanner. For a fail closed
// type Prepare returns it (not an upload error): answer 503 and let the client retry.
var ErrScanUnavailable = errors.New("objectstore: malware scanner unavailable")

// DefaultClamdAddr is the platform clamd (devops-k8s manifests/email/stalwart-clamav.yaml, the
// same scanner the mail server uses), used when CLAMD_ADDR is unset and the process runs in
// Kubernetes.
const DefaultClamdAddr = "stalwart-clamav.email.svc.cluster.local:3310"

// ClamdScanner talks to clamd over TCP with the INSTREAM command.
type ClamdScanner struct {
	Addr string
	// DialTimeout caps the connection (default 3s); Timeout caps the whole scan (default 60s).
	DialTimeout time.Duration
	Timeout     time.Duration
	// MaxBytes is the largest file sent to clamd (default 100 MB, clamd's StreamMaxLength).
	MaxBytes int64
	// ChunkSize is the INSTREAM chunk length (default 64 KB).
	ChunkSize int

	mu          sync.Mutex
	version     string
	versionTime time.Time
}

// ClamdFromEnv builds the scanner from CLAMD_ADDR, CLAMD_DIAL_TIMEOUT, CLAMD_TIMEOUT and
// CLAMD_MAX_BYTES. It returns nil (scanning off) when CLAMD_ADDR is "off", or when it is unset
// outside Kubernetes (development and unit tests); inside Kubernetes an unset CLAMD_ADDR means
// DefaultClamdAddr, so every service is covered with no configuration.
func ClamdFromEnv() *ClamdScanner {
	addr := env("CLAMD_ADDR")
	switch {
	case strings.EqualFold(addr, "off"):
		return nil
	case addr == "" && env("KUBERNETES_SERVICE_HOST") != "":
		addr = DefaultClamdAddr
	case addr == "":
		return nil
	}
	s := &ClamdScanner{Addr: addr}
	if d, err := time.ParseDuration(env("CLAMD_DIAL_TIMEOUT")); err == nil && d > 0 {
		s.DialTimeout = d
	}
	if d, err := time.ParseDuration(env("CLAMD_TIMEOUT")); err == nil && d > 0 {
		s.Timeout = d
	}
	if n, err := strconv.ParseInt(env("CLAMD_MAX_BYTES"), 10, 64); err == nil && n > 0 {
		s.MaxBytes = n
	}
	return s
}

func (s *ClamdScanner) dialTimeout() time.Duration {
	if s.DialTimeout > 0 {
		return s.DialTimeout
	}
	return 3 * time.Second
}

func (s *ClamdScanner) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 60 * time.Second
}

func (s *ClamdScanner) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return 100 * mb
}

func (s *ClamdScanner) chunkSize() int {
	if s.ChunkSize > 0 {
		return s.ChunkSize
	}
	return 64 << 10
}

// Scan streams data to clamd. An infected file is a verdict, not an error; every error wraps
// ErrScanUnavailable.
func (s *ClamdScanner) Scan(ctx context.Context, data []byte) (ScanVerdict, error) {
	if int64(len(data)) > s.maxBytes() {
		return ScanVerdict{}, fmt.Errorf("%w: file of %d bytes is above the scan limit", ErrScanUnavailable, len(data))
	}
	reply, err := s.command(ctx, "zINSTREAM\x00", func(c net.Conn) error {
		var hdr [4]byte
		for off := 0; off < len(data); off += s.chunkSize() {
			end := min(off+s.chunkSize(), len(data))
			binary.BigEndian.PutUint32(hdr[:], uint32(end-off))
			if _, err := c.Write(hdr[:]); err != nil {
				return err
			}
			if _, err := c.Write(data[off:end]); err != nil {
				return err
			}
		}
		binary.BigEndian.PutUint32(hdr[:], 0)
		_, err := c.Write(hdr[:])
		return err
	})
	if err != nil {
		return ScanVerdict{}, err
	}
	v := ScanVerdict{Engine: s.Version(ctx)}
	// Replies: "stream: OK", "stream: <signature> FOUND", "<reason> ERROR".
	body := strings.TrimPrefix(reply, "stream: ")
	switch {
	case body == "OK":
		return v, nil
	case strings.HasSuffix(body, " FOUND"):
		v.Infected = true
		v.Signature = strings.TrimSuffix(body, " FOUND")
		return v, nil
	default:
		return ScanVerdict{}, fmt.Errorf("%w: clamd answered %q", ErrScanUnavailable, reply)
	}
}

// Version returns clamd's version line, cached for ten minutes; empty when it cannot be read.
func (s *ClamdScanner) Version(ctx context.Context) string {
	s.mu.Lock()
	if s.version != "" && time.Since(s.versionTime) < 10*time.Minute {
		v := s.version
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()
	reply, err := s.command(ctx, "zVERSION\x00", nil)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	s.version, s.versionTime = reply, time.Now()
	s.mu.Unlock()
	return reply
}

// command sends one null-terminated clamd command (z prefix), lets send write a payload, and
// reads the null-terminated reply.
func (s *ClamdScanner) command(ctx context.Context, cmd string, send func(net.Conn) error) (string, error) {
	d := net.Dialer{Timeout: s.dialTimeout()}
	c, err := d.DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrScanUnavailable, err)
	}
	defer c.Close()
	deadline := time.Now().Add(s.timeout())
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = c.SetDeadline(deadline)
	if _, err := io.WriteString(c, cmd); err != nil {
		return "", fmt.Errorf("%w: %v", ErrScanUnavailable, err)
	}
	if send != nil {
		if err := send(c); err != nil {
			return "", fmt.Errorf("%w: %v", ErrScanUnavailable, err)
		}
	}
	// The reply ends with a null byte (z commands); stop there rather than wait for the close.
	var reply []byte
	buf := make([]byte, 512)
	for len(reply) < 4096 {
		n, err := c.Read(buf)
		reply = append(reply, buf[:n]...)
		if i := bytes.IndexByte(reply, 0); i >= 0 {
			reply = reply[:i]
			break
		}
		if err != nil {
			if len(reply) == 0 || !errors.Is(err, io.EOF) {
				return "", fmt.Errorf("%w: %v", ErrScanUnavailable, err)
			}
			break
		}
	}
	out := strings.TrimSpace(string(reply))
	if out == "" {
		return "", fmt.Errorf("%w: empty reply", ErrScanUnavailable)
	}
	return out, nil
}

var (
	scannerMu   sync.RWMutex
	scanner     Scanner
	scannerInit sync.Once
	scanLogger  *slog.Logger
)

// SetScanner replaces the scanner Prepare uses (nil turns scanning off) and returns the previous
// one. Without a call the scanner comes from ClamdFromEnv on first use.
func SetScanner(s Scanner) Scanner {
	scannerInit.Do(func() {}) // a later first use must not overwrite this choice from the env
	scannerMu.Lock()
	defer scannerMu.Unlock()
	prev := scanner
	scanner = s
	return prev
}

// SetScanLogger sets where scan alerts go (default slog.Default, which writes to stderr).
func SetScanLogger(l *slog.Logger) {
	scannerMu.Lock()
	defer scannerMu.Unlock()
	scanLogger = l
}

func currentScanner() (Scanner, *slog.Logger) {
	scannerInit.Do(func() {
		if c := ClamdFromEnv(); c != nil {
			scannerMu.Lock()
			scanner = c
			scannerMu.Unlock()
		}
	})
	scannerMu.RLock()
	defer scannerMu.RUnlock()
	l := scanLogger
	if l == nil {
		l = slog.Default()
	}
	return scanner, l
}

// effectiveScan resolves ScanAuto for a type.
func effectiveScan(t MediaType) ScanPolicy {
	if t.Scan != ScanAuto {
		return t.Scan
	}
	if t.Public {
		return ScanFailOpen
	}
	return ScanFailClosed
}

// scanUpload applies the type's scan policy to the bytes as uploaded.
func scanUpload(t MediaType, raw []byte) (ScanResult, error) {
	policy := effectiveScan(t)
	if policy == ScanSkip {
		return ScanResult{Status: ScanStatusSkipped}, nil
	}
	sc, log := currentScanner()
	if sc == nil {
		return ScanResult{Status: ScanStatusSkipped}, nil
	}
	v, err := sc.Scan(context.Background(), raw)
	if err != nil {
		if policy == ScanFailOpen {
			log.Error("malware scanner unavailable: public upload stored unscanned",
				"alert", "clamd_unavailable", "media_type", t.Name, "bytes", len(raw), "error", err.Error())
			return ScanResult{Status: ScanStatusUnavailable}, nil
		}
		log.Error("malware scanner unavailable: upload refused",
			"alert", "clamd_unavailable", "media_type", t.Name, "bytes", len(raw), "error", err.Error())
		if !errors.Is(err, ErrScanUnavailable) {
			err = fmt.Errorf("%w: %v", ErrScanUnavailable, err)
		}
		return ScanResult{}, err
	}
	if v.Infected {
		log.Warn("upload refused by the malware scan", "media_type", t.Name, "signature", v.Signature, "engine", v.Engine)
		return ScanResult{}, uploadErr("the file was refused by the malware scan")
	}
	return ScanResult{Status: ScanStatusClean, ScannedAt: time.Now().UTC(), Engine: v.Engine}, nil
}
