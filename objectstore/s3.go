package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when the requested key does not exist.
var ErrNotFound = errors.New("object not found")

// ErrNotConfigured is returned by every operation of a store that has neither object storage nor a
// local media root configured.
var ErrNotConfigured = errors.New("object storage is not configured (set OBJECT_STORAGE_* or MEDIA_ROOT)")

// s3Client is a minimal S3-compatible client (Cloudflare R2, MinIO, AWS) signing with AWS
// Signature Version 4 on net/http, so no SDK dependency tree is pulled into every service. Path
// style addressing (endpoint/bucket/key) works on R2, MinIO and AWS.
type s3Client struct {
	http       *http.Client
	endpoint   *url.URL // scheme://host[:port]
	accessKey  string
	secretKey  string
	region     string
	autoCreate bool

	ensureMu sync.Mutex
	ensured  map[string]bool
}

func newS3Client(cfg Config) (*s3Client, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("objectstore: invalid endpoint %q", cfg.Endpoint)
	}
	u.Path, u.RawQuery, u.Fragment = "", "", ""
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto" // R2's signing region; MinIO and AWS take their own
	}
	return &s3Client{
		http:       &http.Client{Timeout: 60 * time.Second},
		endpoint:   u,
		accessKey:  strings.TrimSpace(cfg.AccessKeyID),
		secretKey:  strings.TrimSpace(cfg.SecretAccessKey),
		region:     region,
		autoCreate: cfg.AutoCreateBuckets,
		ensured:    map[string]bool{},
	}, nil
}

func (c *s3Client) put(ctx context.Context, bucket, key string, body []byte, contentType, cacheControl string) error {
	if err := c.ensureBucket(ctx, bucket); err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req, err := c.newRequest(ctx, http.MethodPut, bucket, key, nil, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if cacheControl != "" {
		req.Header.Set("Cache-Control", cacheControl)
	}
	c.sign(req, body)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore put %s: %w", key, err)
	}
	defer drain(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("objectstore put %s: %s", key, statusDetail(resp))
	}
	return nil
}

// putStream uploads size bytes read from body without holding them in memory (a single PUT,
// up to 5 GB).
func (c *s3Client) putStream(ctx context.Context, bucket, key string, body io.Reader, size int64, contentType, cacheControl string) error {
	if err := c.ensureBucket(ctx, bucket); err != nil {
		return err
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req, err := c.newRequest(ctx, http.MethodPut, bucket, key, nil, nil)
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(body)
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	if cacheControl != "" {
		req.Header.Set("Cache-Control", cacheControl)
	}
	c.signHash(req, unsignedPayload)
	// No client timeout: a large file may take longer than the request timeout; ctx bounds it.
	resp, err := (&http.Client{Transport: c.http.Transport}).Do(req)
	if err != nil {
		return fmt.Errorf("objectstore put %s: %w", key, err)
	}
	defer drain(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("objectstore put %s: %s", key, statusDetail(resp))
	}
	return nil
}

func (c *s3Client) get(ctx context.Context, bucket, key string) ([]byte, string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, bucket, key, nil, nil)
	if err != nil {
		return nil, "", err
	}
	c.sign(req, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("objectstore get %s: %w", key, err)
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("objectstore get %s: %s", key, statusDetail(resp))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("objectstore get %s: read body: %w", key, err)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// open starts a streaming GET; the caller closes the body. size is -1 when unknown.
func (c *s3Client) open(ctx context.Context, bucket, key string) (io.ReadCloser, int64, string, error) {
	resp, err := c.fetch(ctx, bucket, key, "")
	if err != nil {
		return nil, 0, "", err
	}
	return resp.Body, resp.ContentLength, resp.Header.Get("Content-Type"), nil
}

// fetch starts a streaming GET, forwarding an optional Range header ("bytes=0-1023"). The caller
// closes the body of a 200 or 206 answer; a 416 comes back as is (body drained) so the caller
// can pass it on.
func (c *s3Client) fetch(ctx context.Context, bucket, key, rng string) (*http.Response, error) {
	req, err := c.newRequest(ctx, http.MethodGet, bucket, key, nil, nil)
	if err != nil {
		return nil, err
	}
	c.sign(req, nil)
	// Range is not part of the signature, so it is set after signing.
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	// No client timeout here: a large file may take longer than the request timeout to stream;
	// the caller's context bounds it.
	resp, err := (&http.Client{Transport: c.http.Transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("objectstore get %s: %w", key, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		drain(resp)
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		drain(resp)
		return resp, nil
	case resp.StatusCode >= 300:
		defer drain(resp)
		return nil, fmt.Errorf("objectstore get %s: %s", key, statusDetail(resp))
	}
	return resp, nil
}

func (c *s3Client) exists(ctx context.Context, bucket, key string) (bool, error) {
	req, err := c.newRequest(ctx, http.MethodHead, bucket, key, nil, nil)
	if err != nil {
		return false, err
	}
	c.sign(req, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("objectstore head %s: %w", key, err)
	}
	defer drain(resp)
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	case resp.StatusCode >= 300:
		return false, fmt.Errorf("objectstore head %s: %s", key, resp.Status)
	}
	return true, nil
}

// del removes key; a missing key is not an error (S3 semantics), so deletes are idempotent.
func (c *s3Client) del(ctx context.Context, bucket, key string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, bucket, key, nil, nil)
	if err != nil {
		return err
	}
	c.sign(req, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore delete %s: %w", key, err)
	}
	defer drain(resp)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("objectstore delete %s: %s", key, statusDetail(resp))
	}
	return nil
}

// presignGet returns a time-limited download link the browser fetches directly. downloadName, when
// set, makes the response an attachment with that file name.
func (c *s3Client) presignGet(bucket, key, downloadName string, ttl time.Duration) string {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if ttl > 7*24*time.Hour {
		ttl = 7 * 24 * time.Hour // SigV4 maximum
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	scope := dateStamp + "/" + c.region + "/s3/aws4_request"
	canonicalURI := canonicalPath(bucket, key)
	host := c.endpoint.Host

	q := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    c.accessKey + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.Itoa(int(ttl.Seconds())),
		"X-Amz-SignedHeaders": "host",
	}
	if downloadName != "" {
		q["response-content-disposition"] = fmt.Sprintf("attachment; filename=%q", downloadName)
	}
	canonicalQuery := encodeQuery(q)
	canonicalRequest := strings.Join([]string{
		http.MethodGet, canonicalURI, canonicalQuery, "host:" + host + "\n", "host", "UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hashHex([]byte(canonicalRequest))}, "\n")
	signature := hex.EncodeToString(hmacSHA256(c.signingKey(dateStamp), []byte(stringToSign)))
	return c.endpoint.Scheme + "://" + host + canonicalURI + "?" + canonicalQuery + "&X-Amz-Signature=" + signature
}

// headBucket checks the bucket is reachable with these credentials.
func (c *s3Client) headBucket(ctx context.Context, bucket string) error {
	req, err := c.newRequest(ctx, http.MethodHead, bucket, "", nil, nil)
	if err != nil {
		return err
	}
	c.sign(req, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore: bucket %s unreachable: %w", bucket, err)
	}
	drain(resp)
	switch {
	case resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("objectstore: bucket %s does not exist", bucket)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("objectstore: access to bucket %s denied (%s)", bucket, resp.Status)
	}
	return fmt.Errorf("objectstore: bucket %s: %s", bucket, resp.Status)
}

// ensureBucket creates a missing bucket once per process when AutoCreateBuckets is on (local
// MinIO). Production buckets are created by infrastructure, and the R2 token cannot create them.
func (c *s3Client) ensureBucket(ctx context.Context, bucket string) error {
	if !c.autoCreate {
		return nil
	}
	c.ensureMu.Lock()
	defer c.ensureMu.Unlock()
	if c.ensured[bucket] {
		return nil
	}
	head, err := c.newRequest(ctx, http.MethodHead, bucket, "", nil, nil)
	if err != nil {
		return err
	}
	c.sign(head, nil)
	resp, err := c.http.Do(head)
	if err != nil {
		return fmt.Errorf("objectstore head bucket %s: %w", bucket, err)
	}
	drain(resp)
	if resp.StatusCode == http.StatusNotFound {
		put, err := c.newRequest(ctx, http.MethodPut, bucket, "", nil, nil)
		if err != nil {
			return err
		}
		c.sign(put, nil)
		cresp, err := c.http.Do(put)
		if err != nil {
			return fmt.Errorf("objectstore create bucket %s: %w", bucket, err)
		}
		defer drain(cresp)
		// 409: another replica created it first.
		if cresp.StatusCode >= 300 && cresp.StatusCode != http.StatusConflict {
			return fmt.Errorf("objectstore create bucket %s: %s", bucket, statusDetail(cresp))
		}
	} else if resp.StatusCode >= 300 {
		return fmt.Errorf("objectstore head bucket %s: %s", bucket, resp.Status)
	}
	c.ensured[bucket] = true
	return nil
}

func canonicalPath(bucket, key string) string {
	p := "/" + awsURIEncode(bucket, false)
	key = strings.TrimPrefix(strings.TrimSpace(key), "/")
	if key != "" {
		p += "/" + awsURIEncode(key, true)
	}
	return p
}

func (c *s3Client) newRequest(ctx context.Context, method, bucket, key string, query map[string]string, body []byte) (*http.Request, error) {
	raw := c.endpoint.Scheme + "://" + c.endpoint.Host + canonicalPath(bucket, key)
	if len(query) > 0 {
		raw += "?" + encodeQuery(query)
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, rdr)
	if err != nil {
		return nil, fmt.Errorf("objectstore request: %w", err)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	return req, nil
}

// sign sets the SigV4 Authorization header over host, content-type and every x-amz-* header.
func (c *s3Client) sign(req *http.Request, payload []byte) {
	c.signHash(req, hashHex(payload))
}

// unsignedPayload lets a streamed body go out without hashing it first (S3 and R2 accept it over
// TLS; the headers are still signed).
const unsignedPayload = "UNSIGNED-PAYLOAD"

func (c *s3Client) signHash(req *http.Request, payloadHash string) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Host = c.endpoint.Host
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signed := []string{"host"}
	values := map[string]string{"host": c.endpoint.Host}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		signed = append(signed, "content-type")
		values["content-type"] = ct
	}
	for name := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") {
			signed = append(signed, lower)
			values[lower] = strings.TrimSpace(req.Header.Get(name))
		}
	}
	sort.Strings(signed)
	var canonicalHeaders strings.Builder
	for _, h := range signed {
		canonicalHeaders.WriteString(h + ":" + values[h] + "\n")
	}
	signedHeaders := strings.Join(signed, ";")
	canonicalRequest := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), req.URL.RawQuery, canonicalHeaders.String(), signedHeaders, payloadHash,
	}, "\n")
	scope := dateStamp + "/" + c.region + "/s3/aws4_request"
	stringToSign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope, hashHex([]byte(canonicalRequest))}, "\n")
	signature := hex.EncodeToString(hmacSHA256(c.signingKey(dateStamp), []byte(stringToSign)))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.accessKey, scope, signedHeaders, signature))
}

func (c *s3Client) signingKey(dateStamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+c.secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(c.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// encodeQuery builds a canonical (sorted, RFC 3986 encoded) query string.
func encodeQuery(q map[string]string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, awsURIEncode(k, false)+"="+awsURIEncode(q[k], false))
	}
	return strings.Join(parts, "&")
}

// awsURIEncode is the AWS canonical URI encoding: unreserved characters pass through, the rest is
// upper-case percent encoded, and "/" is kept only in paths. net/url's encoders differ ("+" for
// space, "/" handling), which breaks signatures.
func awsURIEncode(s string, keepSlash bool) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9'),
			ch == '-', ch == '_', ch == '.', ch == '~':
			b.WriteByte(ch)
		case ch == '/' && keepSlash:
			b.WriteByte('/')
		default:
			b.WriteString(fmt.Sprintf("%%%02X", ch))
		}
	}
	return b.String()
}

// statusDetail formats an error response without credentials (S3 errors never echo them).
func statusDetail(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return resp.Status + ": " + msg
	}
	return resp.Status
}

func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}
