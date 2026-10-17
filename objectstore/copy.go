package objectstore

import (
	"context"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

// CopyResult counts a CopyLegacyTree run.
type CopyResult struct {
	Copied  int
	Skipped int // already in object storage
	Failed  int
	Errors  []string // first few failures, for the log
}

// CopyLegacyTree copies every file under root (a service's old MEDIA_ROOT) to its legacy key, so
// stored "/media/<rel>" paths keep resolving after the move. It is idempotent: a file already
// copied is skipped, so an interrupted run can simply be started again. Dotfiles and temporary
// files are left out.
func (s *Store) CopyLegacyTree(ctx context.Context, root string) (CopyResult, error) {
	var res CopyResult
	if !s.Remote() {
		return res, fmt.Errorf("objectstore: copying needs object storage configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return res, err
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			res.fail(path, walkErr)
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			res.fail(path, err)
			return nil
		}
		key, err := s.LegacyKey(filepath.ToSlash(rel))
		if err != nil {
			res.fail(path, err)
			return nil
		}
		info, _ := parseKey(key)
		ok, err := s.s3.exists(ctx, s.bucket(info), key)
		if err != nil {
			res.fail(path, err)
			return nil
		}
		if ok {
			res.Skipped++
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			res.fail(path, err)
			return nil
		}
		ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if ct == "" {
			ct = sniff(data)
		}
		if err := s.Put(ctx, key, data, ct); err != nil {
			res.fail(path, err)
			return nil
		}
		res.Copied++
		return nil
	})
	return res, err
}

// RunLegacyCopy is the body of a service's one-time copy command (cmd/copy-media): it copies
// MEDIA_ROOT to object storage with the shared configuration and prints a summary. Run it inside
// the service's pod, where the media volume and the object-storage Secret are mounted. It returns
// an error when any file failed, so the run can be repeated until it is clean.
func RunLegacyCopy(ctx context.Context, service string, opts ...Option) error {
	cfg := FromEnv()
	if cfg.LocalRoot == "" {
		return fmt.Errorf("objectstore: MEDIA_ROOT is not set, nothing to copy")
	}
	st, err := New(service, cfg, opts...)
	if err != nil {
		return err
	}
	res, err := st.CopyLegacyTree(ctx, cfg.LocalRoot)
	fmt.Printf("%s: copied %d, already there %d, failed %d\n", service, res.Copied, res.Skipped, res.Failed)
	for _, e := range res.Errors {
		fmt.Println("  " + e)
	}
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		return fmt.Errorf("objectstore: %d files failed, run again", res.Failed)
	}
	return nil
}

func (r *CopyResult) fail(path string, err error) {
	r.Failed++
	if len(r.Errors) < 20 {
		r.Errors = append(r.Errors, path+": "+err.Error())
	}
}
