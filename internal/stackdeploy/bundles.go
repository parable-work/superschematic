package stackdeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A deploy builds the bundle of each server, job or worker whose
// platform's artifact is a bundle (docs/stack-model.md, section 18.5,
// D64). After the frozen install of the Bun workspace that a site's build
// also runs, it runs `bun bundle.ts <dir>` in the deployable's package,
// server/<stack>/<deployable>/ under the output root, and digests the
// files bundle.ts wrote into dir as it digests a site's, recording each
// file's SHA-256. When the digest is not the one the manifest records, the
// target's BundlePublisher keeps the files under it, before the deploy
// changes anything. The platform writes where the files are, and each
// file's hash, into the deployable's nodes with ir.BundleDigestToken and
// ir.BundleFileToken; the deploy pins both (PinBundles), and after it
// renders the program it places the files at
// artifacts/<deployable>/<hex>/ beside it (placeArtifacts): the files it
// built, or for a bundle it did not build, the ones it fetched from the
// store (registry.ArtifactFetcher) and checked against the digest. The
// manifest records each bundle, and a deploy given an earlier digest with
// --bundle rolls the deployable back with no build.

// BundleScript is the script in a deployable's package that writes its
// bundle: `bun bundle.ts <dir>` writes the files into dir (D64).
const BundleScript = "bundle.ts"

// ParseBundles reads `--bundle` values, `<deployable>=sha256:<digest>`,
// into a map from a server, a job or a worker to the digest of its
// bundle's files.
func ParseBundles(values []string) (map[string]string, error) {
	bundles := map[string]string{}
	for _, value := range values {
		name, digest, ok := strings.Cut(value, "=")
		if !ok || name == "" || !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("--bundle %q: want <deployable>=sha256:<64 hex digits>, the digest a deploy or a build of the bundle printed", value)
		}
		if _, dup := bundles[name]; dup {
			return nil, fmt.Errorf("--bundle names %s twice", name)
		}
		bundles[name] = digest
	}
	return bundles, nil
}

// Bundle is the bundle a deployable runs, as a deploy pins it and the
// manifest records it (D64).
type Bundle struct {
	// Digest is `sha256:` and the hex SHA-256 of the files' tar stream, as
	// DigestBundle digests them.
	Digest string `json:"digest"`

	// Files holds the hex SHA-256 of each file's content, by its
	// slash-separated path in the bundle: what ir.BundleFileToken pins.
	Files map[string]string `json:"files"`
}

// bundlePlan is where each bundle comes from in one operation: the
// digests --bundle or the manifest decided, and the builds; and the files
// of each that this machine holds, built or fetched.
type bundlePlan struct {
	// digests holds, by deployable, the digest of the bundle it runs.
	digests map[string]string

	// files holds, by deployable, the files of its bundle here: the ones
	// its build wrote, or the ones the deploy fetched under its digest.
	files map[string]*ArtifactFiles

	// built and unchanged name the deployables whose build wrote files
	// that differ from and match the ones the manifest records.
	built     []string
	unchanged []string

	dir string
}

func newBundlePlan() *bundlePlan {
	return &bundlePlan{digests: map[string]string{}, files: map[string]*ArtifactFiles{}}
}

// cleanup removes the files the plan built and fetched.
func (p *bundlePlan) cleanup() {
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// path returns a new directory under the plan's own, named name.
func (p *bundlePlan) path(name string) (string, error) {
	if p.dir == "" {
		dir, err := os.MkdirTemp("", "superschematic-bundles-")
		if err != nil {
			return "", err
		}
		p.dir = dir
	}
	dir := filepath.Join(p.dir, name)
	return dir, os.MkdirAll(dir, 0o755)
}

// missing returns the deployables of env that run a bundle the plan gives
// no digest, sorted.
func (p *bundlePlan) missing(env *ir.ResolvedEnvironment) []string {
	var out []string
	for _, d := range env.Deployables {
		if d.HasBundle() && p.digests[d.Name] == "" {
			out = append(out, d.Name)
		}
	}
	return out
}

// pins returns the bundle of each deployable whose files the plan holds,
// as PinBundles takes it.
func (p *bundlePlan) pins() map[string]Bundle {
	out := map[string]Bundle{}
	for name, digest := range p.digests {
		if files := p.files[name]; files != nil && files.Digest == digest {
			out[name] = Bundle{Digest: digest, Files: maps.Clone(files.Sums)}
		}
	}
	return out
}

// checkBundles refuses an environment with a deployable that runs a bundle
// on a target that keeps none.
func (s *session) checkBundles() error {
	if s.target.Bundles != nil {
		return nil
	}
	for _, d := range s.env.Deployables {
		if d.HasBundle() {
			return fmt.Errorf("%s runs a bundle, as platform %s builds one, and target %s keeps no bundles", kindOf(s.env, d.Name), d.Platform, s.target.Name)
		}
	}
	return nil
}

// planBundles decides the bundle of each deployable that runs one: given,
// by --bundle, first; then, when src is set and the target keeps bundles,
// a build of each such deployable in only (every one when only is empty)
// whose package has a bundle.ts; then the manifest's digest. A bundle is
// built on every deploy, as a site is, and the store keeps its files when
// their digest is new. It returns the plan also with an error, for its
// cleanup.
func (s *session) planBundles(ctx context.Context, prev *Manifest, given map[string]string, src *Sources, only []string) (*bundlePlan, error) {
	p := newBundlePlan()
	var bundled []*ir.ResolvedDeployable
	for _, d := range s.env.Deployables {
		if d.HasBundle() {
			bundled = append(bundled, d)
		}
	}
	for _, name := range sortedKeys(given) {
		if d := s.env.Deployable(name); d == nil || !d.HasBundle() {
			return p, fmt.Errorf("--bundle names %s, which is no server, job or worker of environment %s that runs a bundle", name, s.env.Environment)
		}
	}
	if prev != nil {
		for name, b := range prev.Bundles {
			if d := s.env.Deployable(name); d != nil && d.HasBundle() && b != nil {
				p.digests[name] = b.Digest
			}
		}
	}
	maps.Copy(p.digests, given)
	if src == nil || s.target.Bundles == nil {
		return p, nil
	}
	for _, d := range bundled {
		if _, ok := given[d.Name]; ok || (len(only) > 0 && !slices.Contains(only, d.Name)) {
			continue
		}
		what := kindOf(s.env, d.Name)
		var files *ArtifactFiles
		if src.Prepared != nil {
			// The bundle `stack prepare` built, with no credentials: the
			// deploy builds nothing (D47, amended).
			a := src.Prepared.Bundles[d.Name]
			if a == nil {
				if len(only) > 0 {
					return p, fmt.Errorf("%s: %s holds no bundle of it; run `superschematic stack prepare` for its environment", what, src.Prepared.Dir)
				}
				s.logf("%s: %s holds no bundle of it, so its bundle comes from --bundle or the manifest", what, src.Prepared.Dir)
				continue
			}
			dir, err := p.path(d.Name)
			if err != nil {
				return p, err
			}
			if files, err = src.Prepared.extract(a, "bundle", dir); err != nil {
				return p, fmt.Errorf("%s: %w", what, err)
			}
		} else {
			out, err := p.path(d.Name)
			if err != nil {
				return p, err
			}
			built, err := s.buildBundle(ctx, src, d, only, out)
			if err != nil || !built {
				if err != nil {
					return p, err
				}
				continue
			}
			if files, err = DigestBundle(out); err != nil {
				return p, fmt.Errorf("%s: %w", what, err)
			}
		}
		if p.digests[d.Name] == files.Digest {
			s.logf("%s: bundle %s, the one it runs", what, shortDigest(files.Digest))
			p.unchanged = append(p.unchanged, d.Name)
		} else {
			s.logf("%s: bundle %s, %d files, %s", what, shortDigest(files.Digest), files.Files, byteSize(files.Size))
			p.built = append(p.built, d.Name)
		}
		p.digests[d.Name] = files.Digest
		p.files[d.Name] = files
	}
	return p, nil
}

// buildBundle builds the bundle of d with `bun bundle.ts out` in its
// package, after the frozen install of the workspace. It reports false for
// a package with no bundle.ts, which a build of every deployable passes
// over and one of only refuses.
func (s *session) buildBundle(ctx context.Context, src *Sources, d *ir.ResolvedDeployable, only []string, out string) (bool, error) {
	what := kindOf(s.env, d.Name)
	pkg := servergen.ServerDir(src.OutputRoot, s.env.Stack, d.Name)
	script := filepath.Join(pkg, BundleScript)
	if _, err := os.Stat(script); errors.Is(err, fs.ErrNotExist) {
		if len(only) > 0 {
			return false, fmt.Errorf("%s has no %s at %s: run `superschematic build-all` first, or give its bundle with --bundle", what, BundleScript, script)
		}
		s.logf("%s: no %s at %s, so its bundle comes from --bundle or the manifest", what, BundleScript, script)
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := s.install(ctx, src, "bundle"); err != nil {
		return false, err
	}
	s.logf("%s: bun %s %s in %s", what, BundleScript, out, pkg)
	if err := src.run()(ctx, pkg, s.log, "bun", BundleScript, out); err != nil {
		return false, fmt.Errorf("bundle %s: %w", what, err)
	}
	return true, nil
}

// planBundlesFor plans the bundles of a deploy: it refuses a deployable
// that runs a bundle on a target that keeps none, and one the plan gives
// no bundle.
func (s *session) planBundlesFor(ctx context.Context, prev *Manifest, given map[string]string, src *Sources) (*bundlePlan, error) {
	if err := s.checkBundles(); err != nil {
		return newBundlePlan(), err
	}
	p, err := s.planBundles(ctx, prev, given, src, nil)
	if err != nil {
		return p, err
	}
	if missing := p.missing(s.env); len(missing) > 0 {
		named := make([]string, len(missing))
		for i, name := range missing {
			named[i] = kindOf(s.env, name)
		}
		if src != nil {
			return p, fmt.Errorf("no bundle for %s: the manifest records none and no build made one; run `superschematic build-all`, or give the digest of a bundle a build published with --bundle <deployable>=sha256:<digest>", strings.Join(named, ", "))
		}
		return p, fmt.Errorf("no bundle for %s: the manifest records none, so build it (leave out --no-build) or pass --bundle <deployable>=sha256:<digest> for each", strings.Join(named, ", "))
	}
	return p, nil
}

// publishBundles keeps, through the target's store, the files of each
// bundle the plan built that the manifest does not record, and with force
// each unchanged one too.
func (s *session) publishBundles(ctx context.Context, p *bundlePlan, force bool) error {
	names := slices.Clone(p.built)
	if force {
		names = append(names, p.unchanged...)
	}
	slices.Sort(names)
	for _, name := range names {
		files := p.files[name]
		s.logf("publish the bundle of %s: %s", kindOf(s.env, name), shortDigest(files.Digest))
		if err := s.target.Bundles.Publish(ctx, s.run, name, files.Dir, files.Digest); err != nil {
			return fmt.Errorf("publish the bundle of %s: %w", kindOf(s.env, name), err)
		}
	}
	return nil
}

// fetchBundles fetches, through the target's store, the files of each
// bundle the plan gives a digest and holds no files of, and checks them
// against the digest.
func (s *session) fetchBundles(ctx context.Context, p *bundlePlan) error {
	for _, name := range sortedKeys(p.digests) {
		if p.files[name] != nil {
			continue
		}
		if err := s.checkBundles(); err != nil {
			return err
		}
		dest, err := p.path(name + ".fetched")
		if err != nil {
			return err
		}
		files, err := s.fetchArtifact(ctx, s.target.Bundles, "bundle", name, p.digests[name], dest)
		if err != nil {
			return err
		}
		p.files[name] = files
	}
	return nil
}

// fetchArtifact fetches the files store keeps for a bundle or a site, what,
// of deployable under digest into dest, and checks they are those files.
func (s *session) fetchArtifact(ctx context.Context, store any, what, deployable, digest, dest string) (*ArtifactFiles, error) {
	fetcher, ok := store.(registry.ArtifactFetcher)
	if !ok {
		return nil, fmt.Errorf("the %s of %s is %s, which target %s cannot fetch to place beside the program: its store is no registry.ArtifactFetcher", what, kindOf(s.env, deployable), shortDigest(digest), s.target.Name)
	}
	s.logf("fetch the %s of %s: %s", what, kindOf(s.env, deployable), shortDigest(digest))
	if err := fetcher.Fetch(ctx, s.run, deployable, digest, dest); err != nil {
		return nil, fmt.Errorf("fetch the %s of %s at %s: %w", what, kindOf(s.env, deployable), digest, err)
	}
	files, err := digestFiles(dest, what)
	if err != nil {
		return nil, fmt.Errorf("the %s of %s that target %s fetched: %w", what, kindOf(s.env, deployable), s.target.Name, err)
	}
	if files.Digest != digest {
		return nil, fmt.Errorf("the %s of %s that target %s fetched under %s has digest %s", what, kindOf(s.env, deployable), s.target.Name, digest, files.Digest)
	}
	return files, nil
}

// bundleFileToken matches an ir.BundleFileToken, its file in group 1.
var bundleFileToken = regexp.MustCompile(regexp.QuoteMeta(ir.BundleFileTokenPrefix) + `([^{}]+)\}`)

// PinBundles returns a copy of env in which each deployable's own nodes
// hold, for the bundle bundles names for it, the hex digest of its files in
// place of ir.BundleDigestToken, and each file's hex SHA-256 in place of
// its ir.BundleFileToken (D64). It refuses a digest for a deployable whose
// nodes hold no ir.BundleDigestToken, and a token of a file the bundle
// does not hold.
func PinBundles(env *ir.ResolvedEnvironment, bundles map[string]Bundle) (*ir.ResolvedEnvironment, error) {
	pinned, err := copyEnvironment(env)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(bundles) {
		b := bundles[name]
		hex, ok := strings.CutPrefix(b.Digest, "sha256:")
		if !ok || !digestPattern.MatchString(b.Digest) {
			return nil, fmt.Errorf("%s: bundle digest %q is not sha256:<64 hex digits>", kindOf(env, name), b.Digest)
		}
		count := 0
		var unknown []string
		for _, res := range pinned.Resources.Resources {
			if res.Inherited || !slices.Contains(res.Owners, name) {
				continue
			}
			props, n := rewriteStrings(map[string]any(res.Properties), func(s string) (string, bool) {
				changed := strings.Contains(s, ir.BundleDigestToken)
				s = strings.ReplaceAll(s, ir.BundleDigestToken, hex)
				return s, changed
			})
			count += n
			props, _ = rewriteStrings(props, func(s string) (string, bool) {
				if !strings.Contains(s, ir.BundleFileTokenPrefix) {
					return s, false
				}
				return bundleFileToken.ReplaceAllStringFunc(s, func(token string) string {
					file := bundleFileToken.FindStringSubmatch(token)[1]
					sum, ok := b.Files[file]
					if !ok {
						unknown = append(unknown, file)
						return token
					}
					return sum
				}), true
			})
			res.Properties = props.(map[string]any)
		}
		if len(unknown) > 0 {
			slices.Sort(unknown)
			return nil, fmt.Errorf("%s: its nodes name file %s of its bundle %s, which holds %s", kindOf(env, name), strings.Join(slices.Compact(unknown), ", "), shortDigest(b.Digest), strings.Join(sortedKeys(b.Files), ", "))
		}
		if count == 0 {
			return nil, fmt.Errorf("%s: no property of its nodes holds %s, where its platform writes the digest of its bundle's files", kindOf(env, name), ir.BundleDigestToken)
		}
	}
	return pinned, nil
}

// placeArtifacts places the files of each bundle and site of files beside
// the program in dir, at artifacts/<deployable>/<hex digest>/, in place of
// what artifacts/ held, so the program finds the files its pinned nodes
// name (ir.ArtifactsDir, D64). With none, it leaves the directory alone.
func placeArtifacts(dir string, files map[string]*ArtifactFiles) error {
	if len(files) == 0 {
		return nil
	}
	root := filepath.Join(dir, ir.ArtifactsDir)
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	for _, name := range sortedKeys(files) {
		f := files[name]
		dest := filepath.Join(root, name, strings.TrimPrefix(f.Digest, "sha256:"))
		if err := copyFiles(f.Dir, dest); err != nil {
			return fmt.Errorf("place the files of %s at %s: %w", name, dest, err)
		}
	}
	return nil
}

// copyFiles copies the directories and regular files under src to dest,
// each file executable when it was.
func copyFiles(src, dest string) error {
	return filepath.WalkDir(src, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, 0o755)
		case !entry.Type().IsRegular():
			return fmt.Errorf("%s is not a file", filepath.ToSlash(rel))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

// BundleFlags returns the bundles' digests as `--bundle` values, in
// deployable order.
func (r *BuildResult) BundleFlags() []string {
	var out []string
	for _, name := range sortedKeys(r.Bundles) {
		out = append(out, name+"="+r.Bundles[name])
	}
	return out
}
