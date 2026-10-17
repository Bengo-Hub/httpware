// Package objectstore is the platform's one place for stored files: user and tenant media, documents,
// exports and backups. Every service uses the same buckets, key layout and media rules, configured
// once through OBJECT_STORAGE_* (one Kubernetes Secret per namespace). Without object storage it
// falls back to a local directory (MEDIA_ROOT), so development needs no cloud account.
//
// Keys: <service>/<tenant_id>/<media_type>/<yyyy>/<mm>/<uuid>.<ext>, or
// <service>/users/<user_id>/<media_type>/... for media that belongs to a person rather than a
// tenant. The media type decides the bucket: public types are served from PublicBaseURL (a
// Cloudflare cached custom domain), private ones only through short-lived signed links.
// Databases store the key; read paths call Store.URL to turn it into a link.
package objectstore

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the storage configuration shared by every service.
type Config struct {
	// Endpoint is the S3 API endpoint, e.g. https://<account>.r2.cloudflarestorage.com.
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	// Region is the signing region: "auto" for R2 (the default).
	Region        string
	PublicBucket  string
	PrivateBucket string
	// PublicBaseURL serves the public bucket, e.g. https://media.codevertexafrica.com.
	PublicBaseURL string
	// PresignTTL is the lifetime of private links (default 15 minutes).
	PresignTTL time.Duration
	// AutoCreateBuckets creates missing buckets on first write (local MinIO only).
	AutoCreateBuckets bool

	// LocalRoot and LocalURLBase are the development fallback when no Endpoint is set: files
	// under LocalRoot/<key>, served by the service at LocalURLBase/<key> (httpware.StaticMedia).
	LocalRoot    string
	LocalURLBase string
}

// FromEnv reads the shared configuration:
//
//	OBJECT_STORAGE_ENDPOINT, OBJECT_STORAGE_ACCESS_KEY_ID, OBJECT_STORAGE_SECRET_ACCESS_KEY,
//	OBJECT_STORAGE_REGION, OBJECT_STORAGE_PUBLIC_BUCKET, OBJECT_STORAGE_PRIVATE_BUCKET,
//	OBJECT_STORAGE_PUBLIC_BASE_URL, OBJECT_STORAGE_PRESIGN_TTL (Go duration),
//	OBJECT_STORAGE_AUTO_CREATE_BUCKETS, and for the local fallback MEDIA_ROOT and MEDIA_URL_BASE.
func FromEnv() Config {
	cfg := Config{
		Endpoint:        env("OBJECT_STORAGE_ENDPOINT"),
		AccessKeyID:     env("OBJECT_STORAGE_ACCESS_KEY_ID"),
		SecretAccessKey: env("OBJECT_STORAGE_SECRET_ACCESS_KEY"),
		Region:          env("OBJECT_STORAGE_REGION"),
		PublicBucket:    env("OBJECT_STORAGE_PUBLIC_BUCKET"),
		PrivateBucket:   env("OBJECT_STORAGE_PRIVATE_BUCKET"),
		PublicBaseURL:   env("OBJECT_STORAGE_PUBLIC_BASE_URL"),
		LocalRoot:       env("MEDIA_ROOT"),
		LocalURLBase:    env("MEDIA_URL_BASE"),
	}
	if d, err := time.ParseDuration(env("OBJECT_STORAGE_PRESIGN_TTL")); err == nil {
		cfg.PresignTTL = d
	}
	cfg.AutoCreateBuckets, _ = strconv.ParseBool(env("OBJECT_STORAGE_AUTO_CREATE_BUCKETS"))
	return cfg
}

// Remote reports whether object storage (rather than the local fallback) is configured.
func (c Config) Remote() bool {
	return c.Endpoint != "" && c.AccessKeyID != "" && c.SecretAccessKey != "" &&
		c.PublicBucket != "" && c.PrivateBucket != ""
}

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }
