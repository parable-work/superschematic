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

// SiteFiles is the output of a site's build, digested.
type SiteFiles struct {
	// Dir is the directory the build wrote.
	Dir string

	// Digest is `sha256:` and the hex SHA-256 of the files' tar stream.
	Digest string

	// Files counts the files, and Size the bytes of the tar stream.
	Files int
	Size  int64
}

// DigestSite digests the files under dir as WriteContext digests a build
// context: a tar stream of every directory and regular file, in path
// order, at the epoch, owned by root, with mode 0644, or 0755 for a
// directory or an executable file. It refuses a directory that holds no
// file, and a symbolic link, which a site's publisher could not upload as
// the file it points at.
func DigestSite(dir string) (*SiteFiles, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("no directory %s, which the site's build writes", root)
	}
	sum := sha256.New()
	counter := &countWriter{}
	tw := tar.NewWriter(io.MultiWriter(sum, counter))
	out := &SiteFiles{Dir: root}
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symbolic link; a site's files are files", filepath.ToSlash(rel))
		case entry.IsDir():
		case entry.Type().IsRegular():
			out.Files++
		default:
			return nil
		}
		return writeEntry(tw, p, filepath.ToSlash(rel), entry)
	})
	if err != nil {
		return nil, fmt.Errorf("digest the site at %s: %w", root, err)
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if out.Files == 0 {
		return nil, fmt.Errorf("the site's build wrote no file into %s", root)
	}
	out.Digest = "sha256:" + hex.EncodeToString(sum.Sum(nil))
	out.Size = counter.n
	return out, nil
}

// RunCommand runs name with args in dir, writing its output to log.
type RunCommand func(ctx context.Context, dir string, log io.Writer, name string, args ...string) error

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

	// built holds, by site, the files a build wrote; built and unchanged
	// name the sites whose files differ from and match the manifest's.
	files     map[string]*SiteFiles
	built     []string
	unchanged []string
}

// planSites decides the files of each site: given, by --site, first; then,
// when src is set and the target publishes sites, a build of each site in
// only (every site when only is empty); then the manifest's digest. A site
// is built on every deploy, which is quick, and its files uploaded only
// when their digest is new.
func (s *session) planSites(ctx context.Context, prev *Manifest, given map[string]string, src *Sources, only []string) (*sitePlan, error) {
	p := &sitePlan{digests: map[string]string{}, files: map[string]*SiteFiles{}}
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
	installed := false
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
		run := src.Run
		if run == nil {
			run = execCommand
		}
		if !installed {
			s.logf("install the TypeScript workspace: bun install --frozen-lockfile in %s", src.OutputRoot)
			if err := run(ctx, src.OutputRoot, s.log, "bun", "install", "--frozen-lockfile"); err != nil {
				return nil, fmt.Errorf("install the TypeScript workspace, whose lockfile the site's build installs from (D51, amended): %w", err)
			}
			installed = true
		}
		dir := filepath.Join(src.ImplementationRoot, filepath.FromSlash(d.Site.Dir))
		s.logf("site %s: bun run %s in %s", d.Name, d.Site.Build, dir)
		if err := run(ctx, dir, s.log, "bun", "run", d.Site.Build); err != nil {
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
	switch v := v.(type) {
	case string:
		if strings.Contains(v, token) {
			return strings.ReplaceAll(v, token, value), 1
		}
	case ir.Concat:
		n := 0
		for i, inner := range v {
			replaced, count := replaceToken(inner, token, value)
			v[i] = replaced
			n += count
		}
		return v, n
	case map[string]any:
		n := 0
		for key, inner := range v {
			replaced, count := replaceToken(inner, token, value)
			v[key] = replaced
			n += count
		}
		return v, n
	case []any:
		n := 0
		for i, inner := range v {
			replaced, count := replaceToken(inner, token, value)
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
