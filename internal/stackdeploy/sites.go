package stackdeploy

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// A deploy publishes each site's files (docs/stack-model.md, section 8.10,
// D55). It builds the site, `bun run <build>` in its package after one
// frozen install of the Bun workspace, and digests the directory the build
// wrote as it digests a build context: every file in path order, at the
// epoch, owned by root. The target's SitePublisher puts the files under
// that digest unless they are there already, and writes the site's config
// for the run beside them, each API it calls with its public address,
// read from the run's outputs once the servers it calls rolled out. The
// site's platform writes where it serves its files from into the graph
// with ir.SiteDigestToken, and the deploy pins it to the digest
// (PinSites), so the provisioner's apply of the site's wave switches it to
// the new files at once. The manifest records each site's digest, and a
// deploy given an earlier one with --site rolls the site back with no
// build.

// ParseSites reads `--site` values, `<site>=sha256:<digest>`, into a map
// from a site to the digest of the files it serves.
func ParseSites(values []string) (map[string]string, error) {
	sites := map[string]string{}
	for _, value := range values {
		site, digest, ok := strings.Cut(value, "=")
		if !ok || site == "" || !digestPattern.MatchString(digest) {
			return nil, fmt.Errorf("--site %q: want <site>=sha256:<64 hex digits>, the digest a deploy or a build of the site printed", value)
		}
		if _, dup := sites[site]; dup {
			return nil, fmt.Errorf("--site names %s twice", site)
		}
		sites[site] = digest
	}
	return sites, nil
}

// ArtifactFiles is the output of a site's or a bundle's build, digested,
// or the files a deploy fetched under a digest (D55, D64).
type ArtifactFiles struct {
	// Dir is the directory that holds the files.
	Dir string

	// Digest is `sha256:` and the hex SHA-256 of the files' tar stream.
	Digest string

	// Files counts the files, and Size the bytes of the tar stream.
	Files int
	Size  int64

	// Sums holds the hex SHA-256 of each file's content, by its
	// slash-separated path below Dir: what a bundle's platform pins with
	// ir.BundleFileToken (D64).
	Sums map[string]string
}

// DigestSite digests the files under dir as WriteContext digests a build
// context: a tar stream of every directory and regular file, in path
// order, at the epoch, owned by root, with mode 0644, or 0755 for a
// directory or an executable file. It records each file's SHA-256 too. It
// refuses a directory that holds no file, and a symbolic link, which a
// site's publisher could not upload as the file it points at.
func DigestSite(dir string) (*ArtifactFiles, error) { return digestFiles(dir, "site") }

// DigestBundle digests the files a bundle's build wrote under dir as
// DigestSite digests a site's (D64).
func DigestBundle(dir string) (*ArtifactFiles, error) { return digestFiles(dir, "bundle") }

// digestFiles digests the files of a site's or a bundle's build, what.
func digestFiles(dir, what string) (*ArtifactFiles, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no directory %s, which the %s's build writes", root, what)
	}
	sum := sha256.New()
	counter := &countWriter{}
	tw := tar.NewWriter(io.MultiWriter(sum, counter))
	out := &ArtifactFiles{Dir: root, Sums: map[string]string{}}
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link; a %s's files are files", name, what)
		case entry.IsDir():
		case entry.Type().IsRegular():
			out.Files++
			if out.Sums[name], err = fileSum(p); err != nil {
				return err
			}
		default:
			return nil
		}
		return writeEntry(tw, p, name, entry)
	})
	if err != nil {
		return nil, fmt.Errorf("digest the %s at %s: %w", what, root, err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if out.Files == 0 {
		return nil, fmt.Errorf("the %s's build wrote no file into %s", what, root)
	}
	out.Digest = "sha256:" + hex.EncodeToString(sum.Sum(nil))
	out.Size = counter.n
	return out, nil
}

// fileSum returns the hex SHA-256 of a file's content.
func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// RunCommand runs name with args in dir, writing its output to log.
type RunCommand func(ctx context.Context, dir string, log io.Writer, name string, args ...string) error

// run returns src.Run, or execCommand when it is nil.
func (src *Sources) run() RunCommand {
	if src.Run == nil {
		return execCommand
	}
	return src.Run
}

// install runs one frozen install of the Bun workspace at the output root,
// the first time a site or a bundle builds in the operation, what naming
// the first (D51, amended).
func (s *session) install(ctx context.Context, src *Sources, what string) error {
	if s.installed {
		return nil
	}
	s.logf("install the TypeScript workspace: bun install --frozen-lockfile in %s", src.OutputRoot)
	if err := src.run()(ctx, src.OutputRoot, s.log, "bun", "install", "--frozen-lockfile"); err != nil {
		return fmt.Errorf("install the TypeScript workspace, whose lockfile the %s's build installs from (D51, amended): %w", what, err)
	}
	s.installed = true
	return nil
}

// execCommand is RunCommand with os/exec.
func execCommand(ctx context.Context, dir string, log io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s in %s: %w", name, strings.Join(args, " "), dir, err)
	}
	return nil
}

// sitePlan is where each site's files come from in one deploy or build:
// the digests decided by --site or the manifest, and the builds.
type sitePlan struct {
	// digests holds, by site, the digest of the files it serves.
	digests map[string]string

	// files holds, by site, the files a build wrote, or the deploy fetched
	// to place beside the program; built and unchanged name the sites
	// whose build's files differ from and match the manifest's.
	files     map[string]*ArtifactFiles
	built     []string
	unchanged []string

	// dir holds the files fetched.
	dir string
}

// cleanup removes the files the plan fetched.
func (p *sitePlan) cleanup() {
	if p.dir != "" {
		_ = os.RemoveAll(p.dir)
	}
}

// placesSites reports whether the target's site publisher fetches a site's
// files, so the deploy places them beside the program (D64).
func (s *session) placesSites() bool {
	_, ok := s.target.Sites.(registry.ArtifactFetcher)
	return ok
}

// fetchSites fetches, when the target places sites' files, the files of
// each site the plan gives a digest and holds no files of, and checks them
// against the digest.
func (s *session) fetchSites(ctx context.Context, p *sitePlan) error {
	if !s.placesSites() {
		return nil
	}
	for _, site := range sortedKeys(p.digests) {
		if p.files[site] != nil {
			continue
		}
		if p.dir == "" {
			dir, err := os.MkdirTemp("", "superschematic-sites-")
			if err != nil {
				return err
			}
			p.dir = dir
		}
		dest := filepath.Join(p.dir, site)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return err
		}
		files, err := s.fetchArtifact(ctx, s.target.Sites, "site", site, p.digests[site], dest)
		if err != nil {
			return err
		}
		p.files[site] = files
	}
	return nil
}

// placed returns the files of each site the deploy places beside the
// program, when the target places them, and of each bundle, by deployable.
func (s *session) placed(sites *sitePlan, bundles *bundlePlan) map[string]*ArtifactFiles {
	out := map[string]*ArtifactFiles{}
	if sites != nil && s.placesSites() {
		for site, digest := range sites.digests {
			if files := sites.files[site]; files != nil && files.Digest == digest {
				out[site] = files
			}
		}
	}
	if bundles != nil {
		for name, digest := range bundles.digests {
			if files := bundles.files[name]; files != nil && files.Digest == digest {
				out[name] = files
			}
		}
	}
	return out
}

// planSites decides the files of each site: given, by --site, first; then,
// when src is set and the target publishes sites, a build of each site in
// only (every site when only is empty); then the manifest's digest. A site
// is built on every deploy, which is quick, and its files uploaded only
// when their digest is new.
func (s *session) planSites(ctx context.Context, prev *Manifest, given map[string]string, src *Sources, only []string) (*sitePlan, error) {
	p := &sitePlan{digests: map[string]string{}, files: map[string]*ArtifactFiles{}}
	var sites []*ir.ResolvedDeployable
	for _, d := range s.env.Deployables {
		if d.Kind == ir.DeployableSite {
			sites = append(sites, d)
		}
	}
	for site := range given {
		if d := s.env.Deployable(site); d == nil || d.Kind != ir.DeployableSite {
			return nil, fmt.Errorf("--site names %s, which is no site of environment %s", site, s.env.Environment)
		}
	}
	if prev != nil {
		for site, digest := range prev.Sites {
			if d := s.env.Deployable(site); d != nil && d.Kind == ir.DeployableSite {
				p.digests[site] = digest
			}
		}
	}
	maps.Copy(p.digests, given)
	if src == nil || s.target.Sites == nil || len(sites) == 0 {
		return p, nil
	}
	for _, d := range sites {
		if _, ok := given[d.Name]; ok || (len(only) > 0 && !slices.Contains(only, d.Name)) {
			continue
		}
		if d.Site == nil {
			return nil, fmt.Errorf("site %s says nothing of how it builds", d.Name)
		}
		if src.ImplementationRoot == "" {
			return nil, fmt.Errorf("site %s: the deploy names no repository root, under which its package lies", d.Name)
		}
		if err := s.install(ctx, src, "site"); err != nil {
			return nil, err
		}
		dir := filepath.Join(src.ImplementationRoot, filepath.FromSlash(d.Site.Dir))
		s.logf("site %s: bun run %s in %s", d.Name, d.Site.Build, dir)
		if err := src.run()(ctx, dir, s.log, "bun", "run", d.Site.Build); err != nil {
			return nil, fmt.Errorf("build site %s: %w", d.Name, err)
		}
		files, err := DigestSite(filepath.Join(dir, filepath.FromSlash(d.Site.Output)))
		if err != nil {
			return nil, fmt.Errorf("site %s: %w", d.Name, err)
		}
		if p.digests[d.Name] == files.Digest {
			s.logf("site %s: files %s, the ones it serves", d.Name, shortDigest(files.Digest))
			p.unchanged = append(p.unchanged, d.Name)
		} else {
			s.logf("site %s: files %s, %d files, %s", d.Name, shortDigest(files.Digest), files.Files, byteSize(files.Size))
			p.built = append(p.built, d.Name)
		}
		p.digests[d.Name] = files.Digest
		p.files[d.Name] = files
	}
	return p, nil
}

// planSitesFor plans the sites of a deploy: it refuses an environment
// with a site on a target that publishes none, and a site the plan gives
// no files.
func (s *session) planSitesFor(ctx context.Context, prev *Manifest, given map[string]string, src *Sources) (*sitePlan, error) {
	hasSites := slices.ContainsFunc(s.env.Deployables, func(d *ir.ResolvedDeployable) bool { return d.Kind == ir.DeployableSite })
	if hasSites && s.target.Sites == nil {
		return nil, fmt.Errorf("environment %s has sites, and target %s publishes none", s.env.Environment, s.target.Name)
	}
	p, err := s.planSites(ctx, prev, given, src, nil)
	if err != nil {
		return nil, err
	}
	if missing := p.missing(s.env); len(missing) > 0 {
		if src != nil {
			return nil, fmt.Errorf("no files for site %s: the manifest records none and no build made them; give the digest of files a build published with --site <site>=sha256:<digest>", strings.Join(missing, ", "))
		}
		return nil, fmt.Errorf("no files for site %s: the manifest records none, so build them (leave out --no-build) or pass --site <site>=sha256:<digest> for each", strings.Join(missing, ", "))
	}
	return p, nil
}

// missing returns the sites of env the plan gives no files, sorted.
func (p *sitePlan) missing(env *ir.ResolvedEnvironment) []string {
	var out []string
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableSite && p.digests[d.Name] == "" {
			out = append(out, d.Name)
		}
	}
	return out
}

// PinSites returns a copy of env in which each site's own nodes hold the
// hex digest of the files digests names for it in place of
// ir.SiteDigestToken. It refuses a digest for a site whose nodes hold no
// token.
func PinSites(env *ir.ResolvedEnvironment, digests map[string]string) (*ir.ResolvedEnvironment, error) {
	pinned, err := copyEnvironment(env)
	if err != nil {
		return nil, err
	}
	for _, site := range sortedKeys(digests) {
		hex, ok := strings.CutPrefix(digests[site], "sha256:")
		if !ok || !digestPattern.MatchString(digests[site]) {
			return nil, fmt.Errorf("site %s: digest %q is not sha256:<64 hex digits>", site, digests[site])
		}
		count := 0
		for _, res := range pinned.Resources.Resources {
			if res.Inherited || !slices.Contains(res.Owners, site) {
				continue
			}
			props, n := replaceToken(map[string]any(res.Properties), ir.SiteDigestToken, hex)
			res.Properties = props.(map[string]any)
			count += n
		}
		if count == 0 {
			return nil, fmt.Errorf("site %s: no property of its nodes holds %s, where its platform writes the digest of the files it serves", site, ir.SiteDigestToken)
		}
	}
	return pinned, nil
}

// replaceToken replaces token in every string in v with value, and returns
// how many strings it changed.
func replaceToken(v any, token, value string) (any, int) {
	return rewriteStrings(v, func(s string) (string, bool) {
		if !strings.Contains(s, token) {
			return s, false
		}
		return strings.ReplaceAll(s, token, value), true
	})
}

// rewriteStrings replaces every string in v, in place, with what rewrite
// returns for it, and returns how many strings rewrite said it changed.
func rewriteStrings(v any, rewrite func(string) (string, bool)) (any, int) {
	switch v := v.(type) {
	case string:
		if s, changed := rewrite(v); changed {
			return s, 1
		}
	case ir.Concat:
		n := 0
		for i, inner := range v {
			replaced, count := rewriteStrings(inner, rewrite)
			v[i] = replaced
			n += count
		}
		return v, n
	case map[string]any:
		n := 0
		for key, inner := range v {
			replaced, count := rewriteStrings(inner, rewrite)
			v[key] = replaced
			n += count
		}
		return v, n
	case []any:
		n := 0
		for i, inner := range v {
			replaced, count := rewriteStrings(inner, rewrite)
			v[i] = replaced
			n += count
		}
		return v, n
	}
	return v, 0
}

// SiteConfig is the config a site reads at ir.SiteConfigPath: each API it
// calls, by name, with its public address.
type SiteConfig struct {
	APIs map[string]SiteAPI `json:"apis"`
}

// SiteAPI is an API in a site's config.
type SiteAPI struct {
	URL string `json:"url"`
}

// SiteConfigOf returns the config of site in env for a run with
// parameters: each binding's URL, its references resolved against
// outputs, the run's outputs by node and name.
func SiteConfigOf(env *ir.ResolvedEnvironment, site string, parameters map[string]string, outputs map[string]map[string]any) ([]byte, error) {
	d := env.Deployable(site)
	if d == nil || d.Kind != ir.DeployableSite {
		return nil, fmt.Errorf("environment %s has no site %s", env.Environment, site)
	}
	cfg := SiteConfig{APIs: map[string]SiteAPI{}}
	for _, b := range d.Bindings {
		value, _ := b.Value.(map[string]any)
		url, err := resolveString(value["url"], parameters, outputs)
		if err != nil {
			return nil, fmt.Errorf("site %s: the URL of %s: %w", site, b.Field, err)
		}
		if url == "" {
			return nil, fmt.Errorf("site %s: the URL of %s is empty", site, b.Field)
		}
		cfg.APIs[b.Field] = SiteAPI{URL: url}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// resolveString renders a value whose references resolve against a run:
// a string as it is, a parameter as the run's value, an output as the
// run's, and a concatenation joined.
func resolveString(v any, parameters map[string]string, outputs map[string]map[string]any) (string, error) {
	switch v := v.(type) {
	case string:
		return v, nil
	case ir.Parameter:
		value, ok := parameters[string(v)]
		if !ok {
			return "", fmt.Errorf("the run gives no value for parameter %s", v)
		}
		return value, nil
	case ir.Output:
		value, ok := outputs[v.Resource][v.Name]
		if !ok {
			return "", fmt.Errorf("resource %s has no output %s yet", v.Resource, v.Name)
		}
		switch value := value.(type) {
		case string:
			return value, nil
		case float64:
			return strconv.FormatFloat(value, 'f', -1, 64), nil
		case int:
			return strconv.Itoa(value), nil
		}
		return "", fmt.Errorf("output %s of resource %s is %v, not a string", v.Name, v.Resource, value)
	case ir.Concat:
		var b strings.Builder
		for _, part := range v {
			s, err := resolveString(part, parameters, outputs)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		}
		return b.String(), nil
	case nil:
		return "", errors.New("it has no value")
	}
	return "", fmt.Errorf("%v is not a string or a reference", v)
}

// publishSite publishes one site's files and its config for the run,
// whose servers it calls rolled out before it.
func (d *deploy) publishSite(ctx context.Context, site string) error {
	digest := d.sites.digests[site]
	outputs, err := d.prov.Outputs(ctx, d.req)
	if err != nil {
		return fmt.Errorf("read the outputs of %s for the config of site %s: %w", d.run.Name(), site, err)
	}
	config, err := SiteConfigOf(d.env, site, d.run.Parameters, outputs)
	if err != nil {
		return err
	}
	req := registry.SitePublishRequest{Run: d.run, Site: site, Digest: digest, Config: config, Log: d.log}
	if files := d.sites.files[site]; files != nil && slices.Contains(d.sites.built, site) {
		// Files the site does not serve yet go up; the ones it serves, or
		// an earlier deploy's that --site names, are there.
		req.Dir = files.Dir
	}
	if err := req.Check(); err != nil {
		return err
	}
	d.logf("  publish site %s: files %s", site, shortDigest(digest))
	if err := d.target.Sites.Publish(ctx, req); err != nil {
		return fmt.Errorf("publish site %s: %w", site, err)
	}
	return nil
}
