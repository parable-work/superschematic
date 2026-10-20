package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/parable-work/superschematic/registry"
)

// The bundle store (section 18.3, D64): the files of each bundle a deploy
// builds sit in the state bucket under BundlePrefix, at
// `<stack>/<deployable>/<hex>/<file>`, `<hex>` being the digest's. Each
// file carries, as metadata, the hex SHA-256 of its content and the mode it
// was published with, 644 or 755, since the digest covers the executable
// bit. A marker, filesMarker under the same prefix, is written after the
// last file, so a prefix that holds it holds every file, and a publish
// whose marker is there uploads nothing. A deploy that did not build a
// bundle, a plan in CI or a `--bundle` rollback, fetches its files back,
// each checked against its SHA-256, and the core checks the whole against
// the digest. Nothing is ever deleted: the files of every digest stay for
// a rollback.

// filesMarker is the object written after a prefix's last file. A file of
// that path is refused.
const filesMarker = "__superschematic/files"

// The metadata each file carries.
const (
	metaSHA256 = "sha256"
	metaMode   = "mode"
)

// fileModes are the modes a file is published and fetched with: a digest
// knows only whether a file is executable.
const (
	modeFile       = "644"
	modeExecutable = "755"
)

// transfers is how many files a publish or a fetch moves at once.
const transfers = 8

// bundleStore is the target's BundlePublisher, and the ArtifactFetcher
// that writes a bundle's files back.
type bundleStore struct{ ext Extension }

var (
	_ registry.BundlePublisher = bundleStore{}
	_ registry.ArtifactFetcher = bundleStore{}
)

// BundleObjects is the prefix in StateBucket that holds the files of a
// deployable's bundle under a digest, `sha256:<hex>`:
// `superschematic/bundles/<stack>/<deployable>/<hex>/`.
func BundleObjects(stack, deployable, digest string) string {
	return BundlePrefix + stack + "/" + deployable + "/" + strings.TrimPrefix(digest, "sha256:") + "/"
}

// open returns the Cloud and the account of a run.
func (s bundleStore) open(run registry.Run) (Cloud, string, error) {
	if err := run.Check(); err != nil {
		return nil, "", err
	}
	v, err := envValues(run.Environment)
	if err != nil {
		return nil, "", err
	}
	cloud, err := s.ext.cloud()
	return cloud, v.accountID, err
}

// Publish keeps the files of dir under digest, unless the marker says they
// are there.
func (s bundleStore) Publish(ctx context.Context, run registry.Run, deployable, dir, digest string) error {
	cloud, account, err := s.open(run)
	if err != nil {
		return err
	}
	d := run.Environment.Deployable(deployable)
	if d == nil || !d.HasBundle() {
		return fmt.Errorf("cloudflare: environment %s has no deployable %s that runs a bundle", run.Environment.Environment, deployable)
	}
	return putFiles(ctx, cloud, account, BundleObjects(run.Environment.Stack, deployable, digest), dir, digest)
}

// Fetch writes the files kept for a deployable's bundle under digest into
// dest, each checked against the SHA-256 it was published with and
// executable when it was published so.
func (s bundleStore) Fetch(ctx context.Context, run registry.Run, deployable, digest, dest string) error {
	cloud, account, err := s.open(run)
	if err != nil {
		return err
	}
	return getFiles(ctx, cloud, account, BundleObjects(run.Environment.Stack, deployable, digest), digest, dest)
}

// putFiles puts every file below dir under prefix in StateBucket, each with
// its SHA-256 and mode, then the marker; with the marker there already, it
// puts nothing. It refuses a symbolic link and a file at the marker's path.
func putFiles(ctx context.Context, cloud Cloud, account, prefix, dir, digest string) error {
	switch _, _, err := cloud.GetObject(ctx, account, StateBucket, prefix+filesMarker); {
	case err == nil:
		return nil
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	var files []string
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case entry.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("cloudflare: %s is a symbolic link; a bundle's files are files", p)
		case entry.IsDir():
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); rel == filesMarker {
			return fmt.Errorf("cloudflare: %s is the path of the marker the store writes after the last file", rel)
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("cloudflare: %s holds no file to publish", dir)
	}
	err = each(files, func(rel string) error {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		mode := modeFile
		if info.Mode()&0o111 != 0 {
			mode = modeExecutable
		}
		return cloud.PutObject(ctx, account, StateBucket, prefix+rel, data, map[string]string{metaSHA256: hex.EncodeToString(sum[:]), metaMode: mode})
	})
	if err != nil {
		return err
	}
	return cloud.PutObject(ctx, account, StateBucket, prefix+filesMarker, []byte(digest+"\n"), nil)
}

// getFiles writes the files under prefix in StateBucket into dest, each
// checked against its SHA-256 and with its mode. It refuses a prefix
// without the marker, whose files a publish never finished.
func getFiles(ctx context.Context, cloud Cloud, account, prefix, digest, dest string) error {
	where := "r2://" + StateBucket + "/" + prefix
	switch _, _, err := cloud.GetObject(ctx, account, StateBucket, prefix+filesMarker); {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("cloudflare: no files under %s at %s: no deploy or build published them, or one stopped before it finished", digest, where)
	case err != nil:
		return err
	}
	keys, err := cloud.ListObjects(ctx, account, StateBucket, prefix)
	if err != nil {
		return err
	}
	var files []string
	for _, key := range keys {
		rel := strings.TrimPrefix(key, prefix)
		if rel == filesMarker || rel == "" {
			continue
		}
		if clean := path.Clean(rel); clean != rel || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			return fmt.Errorf("cloudflare: %s holds %s, which leaves it", where, key)
		}
		files = append(files, rel)
	}
	if len(files) == 0 {
		return fmt.Errorf("cloudflare: %s holds no file beside its marker", where)
	}
	return each(files, func(rel string) error {
		data, meta, err := cloud.GetObject(ctx, account, StateBucket, prefix+rel)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if want := meta[metaSHA256]; want != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("cloudflare: %s%s has SHA-256 %x, and was published with %q", where, rel, sum, want)
		}
		mode := os.FileMode(0o644)
		switch meta[metaMode] {
		case modeExecutable:
			mode = 0o755
		case modeFile:
		default:
			return fmt.Errorf("cloudflare: %s%s was published with mode %q, not %s or %s", where, rel, meta[metaMode], modeFile, modeExecutable)
		}
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, mode); err != nil {
			return err
		}
		// WriteFile leaves the mode of a file that exists, and the umask
		// may have taken the executable bit.
		return os.Chmod(p, mode)
	})
}

// each calls do for every item, transfers at a time, and returns the first
// error.
func each(items []string, do func(string) error) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	work := make(chan string)
	for range min(transfers, len(items)) {
		wg.Go(func() {
			for item := range work {
				if err := do(item); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
				}
			}
		})
	}
	for _, item := range items {
		work <- item
	}
	close(work)
	wg.Wait()
	return first
}
