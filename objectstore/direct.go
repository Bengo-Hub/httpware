package objectstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Direct uploads let a browser send a large file (footage, generated video) straight to the
// bucket, so it never passes through the API, the ingress or Cloudflare's request size limit. The
// flow: mint a key with NewKey, hand out UploadURL (or a multipart upload for big files), and once
// the client reports completion call VerifyUpload, which checks the size and the real file type
// and deletes a file that breaks the media type's rules.

// ErrRemoteOnly is returned by direct uploads on a store without object storage (local mode):
// there the service takes the file itself (ReadUpload, PutStream).
var ErrRemoteOnly = errors.New("objectstore: direct uploads need object storage")

// ErrInvalidRange is returned by OpenRange when offset is at or past the end of the file.
var ErrInvalidRange = errors.New("objectstore: range not satisfiable")

// MaxParts is the most parts a multipart upload may have (S3 and R2).
const MaxParts = 10000

// ObjectInfo describes a stored file. ContentType is the one it was stored with ("" for local
// files).
type ObjectInfo struct {
	Size        int64
	ContentType string
}

// DirectUpload is a presigned PUT: the client sends the file as the body of a PUT to URL with
// exactly these headers (they are part of the signature), before ExpiresAt.
type DirectUpload struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// Part is one uploaded part of a multipart upload: its number (1 to MaxParts) and the ETag
// header the bucket answered the part's PUT with.
type Part struct {
	Number int    `json:"number"`
	ETag   string `json:"etag"`
}

// UploadURL is a presigned PUT for key, valid for ttl (0: PresignTTL). A single PUT takes up to
// 5 GB; use a multipart upload above about 100 MB so an interrupted upload resumes per part. The
// size cannot be enforced by the link, so call VerifyUpload once the client is done.
func (s *Store) UploadURL(ctx context.Context, key, contentType string, ttl time.Duration) (*DirectUpload, error) {
	info, err := s.directKey(ctx, key)
	if err != nil {
		return nil, err
	}
	ttl = s.ttl(ttl)
	headers := map[string]string{"Content-Type": contentTypeOr(contentType), "Cache-Control": cacheControl(info)}
	return &DirectUpload{
		URL:       s.s3.presign(http.MethodPut, s.bucket(info), key, nil, headers, ttl),
		Method:    http.MethodPut,
		Headers:   headers,
		ExpiresAt: time.Now().Add(ttl),
	}, nil
}

// CreateMultipart starts a multipart upload of key and returns its upload id. The client then
// PUTs each part (5 MB minimum except the last) to a PartURL and reports the parts' ETags, and
// the service calls CompleteMultipart, or AbortMultipart for an upload given up on (an
// unfinished upload keeps its parts in the bucket until aborted).
func (s *Store) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	info, err := s.directKey(ctx, key)
	if err != nil {
		return "", err
	}
	return s.s3.createMultipart(ctx, s.bucket(info), key, contentTypeOr(contentType), cacheControl(info))
}

// PartURL is a presigned PUT for one part of a multipart upload, valid for ttl (0: PresignTTL).
// The bucket's CORS rules must expose the ETag header so a browser can read it.
func (s *Store) PartURL(key, uploadID string, part int, ttl time.Duration) (string, error) {
	if part < 1 || part > MaxParts {
		return "", fmt.Errorf("objectstore: part number %d out of range 1..%d", part, MaxParts)
	}
	if strings.TrimSpace(uploadID) == "" {
		return "", errors.New("objectstore: missing upload id")
	}
	info, err := s.remoteKey(key)
	if err != nil {
		return "", err
	}
	q := map[string]string{"partNumber": strconv.Itoa(part), "uploadId": uploadID}
	return s.s3.presign(http.MethodPut, s.bucket(info), key, q, nil, s.ttl(ttl)), nil
}

// CompleteMultipart joins the parts (any order; sorted by number) into the object.
func (s *Store) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error {
	info, err := s.remoteKey(key)
	if err != nil {
		return err
	}
	if len(parts) == 0 || len(parts) > MaxParts {
		return fmt.Errorf("objectstore: a multipart upload needs 1 to %d parts, got %d", MaxParts, len(parts))
	}
	sorted := append([]Part(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	for i, p := range sorted {
		if p.Number < 1 || p.Number > MaxParts || strings.TrimSpace(p.ETag) == "" {
			return fmt.Errorf("objectstore: invalid part %d", p.Number)
		}
		if i > 0 && sorted[i-1].Number == p.Number {
			return fmt.Errorf("objectstore: part %d listed twice", p.Number)
		}
	}
	return s.s3.completeMultipart(ctx, s.bucket(info), key, uploadID, sorted)
}

// AbortMultipart discards an unfinished upload and its parts. An upload already completed or
// aborted is not an error.
func (s *Store) AbortMultipart(ctx context.Context, key, uploadID string) error {
	info, err := s.remoteKey(key)
	if err != nil {
		return err
	}
	return s.s3.abortMultipart(ctx, s.bucket(info), key, uploadID)
}

// Stat returns the size and stored content type of ref, or ErrNotFound.
func (s *Store) Stat(ctx context.Context, ref string) (ObjectInfo, error) {
	if !s.Enabled() {
		return ObjectInfo{}, ErrNotConfigured
	}
	r, err := s.resolve(ref)
	if err != nil {
		return ObjectInfo{}, err
	}
	if r.remote() {
		return s.s3.head(ctx, s.bucket(r.info), r.key)
	}
	st, err := os.Stat(r.file)
	if errors.Is(err, os.ErrNotExist) {
		return ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Size: st.Size()}, nil
}

// OpenRange streams length bytes of ref from offset (length <= 0: to the end), so a worker can
// send a stored video to a platform in chunks without buffering it. The caller closes the reader.
// An offset at or past the end gives ErrInvalidRange.
func (s *Store) OpenRange(ctx context.Context, ref string, offset, length int64) (io.ReadCloser, error) {
	if offset < 0 {
		return nil, fmt.Errorf("objectstore: negative offset %d", offset)
	}
	if !s.Enabled() {
		return nil, ErrNotConfigured
	}
	r, err := s.resolve(ref)
	if err != nil {
		return nil, err
	}
	if r.remote() {
		rng := "bytes=" + strconv.FormatInt(offset, 10) + "-"
		if length > 0 {
			rng += strconv.FormatInt(offset+length-1, 10)
		}
		resp, err := s.s3.fetch(ctx, s.bucket(r.info), r.key, rng)
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusRequestedRangeNotSatisfiable:
			return nil, ErrInvalidRange
		case http.StatusPartialContent:
			return resp.Body, nil
		}
		// A store that ignored the Range header sent the whole file: skip to offset.
		if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
			_ = resp.Body.Close()
			if errors.Is(err, io.EOF) {
				return nil, ErrInvalidRange
			}
			return nil, err
		}
		return limitBody(resp.Body, length), nil
	}
	f, err := os.Open(r.file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil && offset >= st.Size() {
		err = ErrInvalidRange
	}
	if err == nil {
		_, err = f.Seek(offset, io.SeekStart)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return limitBody(f, length), nil
}

// VerifyUpload checks a directly uploaded file against its media type: it must exist, be at most
// MaxBytes and, when the type lists Allowed content types, have one of them by its first bytes
// (the content type the client declared is not trusted). A file that breaks a rule is deleted
// and an upload error (IsUploadError) returned, for the service to pass on as a 400.
func (s *Store) VerifyUpload(ctx context.Context, key string) (ObjectInfo, error) {
	info, err := parseKey(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	t, err := mustLookup(info.mediaType)
	if err != nil {
		return ObjectInfo{}, err
	}
	obj, err := s.Stat(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return ObjectInfo{}, uploadErr("the upload did not arrive")
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	reject := func(format string, args ...any) (ObjectInfo, error) {
		if err := s.Delete(ctx, key); err != nil {
			return ObjectInfo{}, fmt.Errorf("objectstore: delete rejected upload %s: %w", key, err)
		}
		return ObjectInfo{}, uploadErr(format, args...)
	}
	if obj.Size <= 0 {
		return reject("the file is empty")
	}
	if obj.Size > t.MaxBytes {
		return reject("the file must be at most %s", humanSize(t.MaxBytes))
	}
	rc, err := s.OpenRange(ctx, key, 0, 512)
	if err != nil {
		return ObjectInfo{}, err
	}
	head, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return ObjectInfo{}, err
	}
	ct := sniff(head)
	if len(t.Allowed) > 0 && !contains(t.Allowed, ct) {
		return reject("this file type is not accepted (%s)", ct)
	}
	obj.ContentType = ct
	return obj, nil
}

// directKey checks key for a direct upload and creates a missing bucket where that is allowed.
func (s *Store) directKey(ctx context.Context, key string) (keyInfo, error) {
	info, err := s.remoteKey(key)
	if err != nil {
		return keyInfo{}, err
	}
	return info, s.s3.ensureBucket(ctx, s.bucket(info))
}

func (s *Store) remoteKey(key string) (keyInfo, error) {
	if !s.Enabled() {
		return keyInfo{}, ErrNotConfigured
	}
	if !s.Remote() {
		return keyInfo{}, ErrRemoteOnly
	}
	return parseKey(key)
}

func (s *Store) ttl(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return s.cfg.PresignTTL
	}
	return ttl
}

func contentTypeOr(ct string) string {
	if ct = strings.TrimSpace(ct); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// limitBody caps a body at length bytes (length <= 0: no cap) and still closes it.
func limitBody(rc io.ReadCloser, length int64) io.ReadCloser {
	if length <= 0 {
		return rc
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(rc, length), rc}
}

func (c *s3Client) createMultipart(ctx context.Context, bucket, key, contentType, cacheControl string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodPost, bucket, key, map[string]string{"uploads": ""}, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Cache-Control", cacheControl)
	c.sign(req, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("objectstore create multipart %s: %w", key, err)
	}
	defer drain(resp)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("objectstore create multipart %s: %s", key, statusDetail(resp))
	}
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil || out.UploadID == "" {
		return "", fmt.Errorf("objectstore create multipart %s: no upload id in the answer", key)
	}
	return out.UploadID, nil
}

type completeMultipartUpload struct {
	XMLName xml.Name       `xml:"CompleteMultipartUpload"`
	Parts   []completePart `xml:"Part"`
}

type completePart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

func (c *s3Client) completeMultipart(ctx context.Context, bucket, key, uploadID string, parts []Part) error {
	doc := completeMultipartUpload{Parts: make([]completePart, len(parts))}
	for i, p := range parts {
		doc.Parts[i] = completePart{PartNumber: p.Number, ETag: p.ETag}
	}
	body, err := xml.Marshal(doc)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, bucket, key, map[string]string{"uploadId": uploadID}, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/xml")
	c.sign(req, body)
	// Joining many parts can take a while; ctx bounds it.
	resp, err := (&http.Client{Transport: c.http.Transport}).Do(req)
	if err != nil {
		return fmt.Errorf("objectstore complete multipart %s: %w", key, err)
	}
	defer drain(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("objectstore complete multipart %s: %s", key, statusDetail(resp))
	}
	// S3 may answer 200 and still fail, with an Error document in the body.
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("objectstore complete multipart %s: read answer: %w", key, err)
	}
	if bytes.Contains(answer, []byte("<Error>")) {
		return fmt.Errorf("objectstore complete multipart %s: %s", key, strings.TrimSpace(string(answer)))
	}
	return nil
}

func (c *s3Client) abortMultipart(ctx context.Context, bucket, key, uploadID string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, bucket, key, map[string]string{"uploadId": uploadID}, nil)
	if err != nil {
		return err
	}
	c.sign(req, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore abort multipart %s: %w", key, err)
	}
	defer drain(resp)
	// 404 NoSuchUpload: already completed or aborted.
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("objectstore abort multipart %s: %s", key, statusDetail(resp))
	}
	return nil
}
