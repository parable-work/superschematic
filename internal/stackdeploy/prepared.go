package stackdeploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// `stack prepare` runs, with no credentials, all the project code a
// deploy runs (D47, amended): it evaluates the sidecar documents, writes
// each server's, job's and worker's build context, and builds each site
// and each bundle, into a directory. A stack command given the directory
// with --prepared reads them from it and runs none of that code, so a
// dependency's install script, a build plugin or a sidecar document never
// runs where the deploy's credentials are. The generated workflow runs it
// in a job of its own, with no identity token and no secret, and hands
// the directory to the cloud jobs as an artifact.
//
// The directory holds PreparedFile, contexts/<deployable>.tar.gz,
// sites/<site>.tar and bundles/<deployable>.tar. A site's or a bundle's
// archive is the tar stream its digest is of, so an upload that drops
// file modes cannot change it, and a reader checks the archive against
// its digest before it extracts it.

// PreparedFile names what a prepared directory holds.
const PreparedFile = "prepared.json"

// Prepared is a directory `stack prepare` wrote.
type Prepared struct {
	// Dir is the directory; Stack the stack it was prepared for, and
	// Environments the environments.
	Dir          string   `json:"-"`
	Stack        string   `json:"stack"`
	Environments []string `json:"environments"`

	// Built is false for a directory prepared with --no-build, which
	// holds the documents alone.
	Built bool `json:"built"`

	// Provenance is where the directory was prepared, in CI: the commit
	// and the workflow run. A stack command in CI refuses a directory of
	// another commit or run (CheckProvenance).
	Provenance Provenance `json:"provenance,omitzero"`

	// Documents holds each sidecar document a module of a service runs,
	// by DocumentKey.
	Documents map[string]*PreparedDocument `json:"documents,omitempty"`

	// Contexts holds each server's, job's and worker's build context, and
	// Sites and Bundles each site's files and each bundle, by deployable.
	Contexts map[string]*PreparedContext  `json:"contexts,omitempty"`
	Sites    map[string]*PreparedArtifact `json:"sites,omitempty"`
	Bundles  map[string]*PreparedArtifact `json:"bundles,omitempty"`
}

// Provenance is the commit and the CI run a directory was prepared in:
// GitHub Actions' GITHUB_SHA and GITHUB_RUN_ID. Empty outside CI.
//
// The digests in a prepared directory are checked against prepared.json,
// which the same prepare wrote: they catch a corrupted or half-uploaded
// directory, not a forged one, since code that runs in the prepare job,
// a dependency's install script, writes prepared.json too. What a cloud
// job deploys is as trustworthy as the prepare job of the same run, which
// runs the same pull request's code a deploy without --prepared would
// have run; the plan of a pull request, and the hazard gate, are what
// review it (D47, amended). Provenance ties a directory to its run, so an
// artifact of another run or commit cannot be deployed by mistake.
type Provenance struct {
	Commit string `json:"commit,omitempty"`
	Run    string `json:"run,omitempty"`
}

// CIProvenance is this process's provenance: GitHub Actions' commit and
// run, or none outside it.
func CIProvenance() Provenance {
	return Provenance{Commit: os.Getenv("GITHUB_SHA"), Run: os.Getenv("GITHUB_RUN_ID")}
}

// CheckProvenance refuses, when current names a commit (in CI), a
// directory prepared at another commit or in another run, or outside CI.
func (p *Prepared) CheckProvenance(current Provenance) error {
	if current.Commit == "" {
		return nil
	}
	if p.Provenance.Commit != current.Commit || (current.Run != "" && p.Provenance.Run != current.Run) {
		return fmt.Errorf("--prepared %s was prepared at commit %q in run %q, and this is commit %s in run %s: download the artifact of this run's prepare job", p.Dir, p.Provenance.Commit, p.Provenance.Run, current.Commit, current.Run)
	}
	return nil
}

// PreparedDocument is a sidecar document's module, run.
type PreparedDocument struct {
	// Document is its default export.
	Document json.RawMessage `json:"document"`

	// Imports are the files the module imported, the module itself
	// among them: each under the repository root relative to it, slash
	// separated, and any other absolute.
	Imports []string `json:"imports,omitempty"`

	// Sources holds the hex SHA-256 of each import under the repository
	// root outside a node_modules directory, by its relative path: a
	// replay refuses a document whose sources changed since.
	Sources map[string]string `json:"sources"`
}

// PreparedContext is an image's build context, as WriteContext wrote it.
type PreparedContext struct {
	Archive    string `json:"archive"`
	Digest     string `json:"digest"`
	Dockerfile string `json:"dockerfile"`
	Files      int    `json:"files"`
	Size       int64  `json:"size"`
}

// PreparedArtifact is a site's files or a bundle: Archive is the tar
// stream whose SHA-256 Digest is.
type PreparedArtifact struct {
	Archive string `json:"archive"`
	Digest  string `json:"digest"`
	Files   int    `json:"files"`
	Size    int64  `json:"size"`
}

// DocumentKey names the document file of the service at servicePath in
// Prepared.Documents: its path relative to the repository root, slash
// separated.
func DocumentKey(root, servicePath, file string) (string, error) {
	rel, err := filepath.Rel(root, filepath.Join(servicePath, filepath.FromSlash(file)))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s of %s lies outside the repository root %s", file, servicePath, root)
	}
	return filepath.ToSlash(rel), nil
}

// RecordDocument returns what a prepared directory keeps of a document a
// module exported, doc, with the files it imported, absolute: the imports
// under root become relative to it, and each one outside node_modules has
// its content digested.
func RecordDocument(root string, doc json.RawMessage, imports []string) (*PreparedDocument, error) {
	out := &PreparedDocument{Document: doc, Sources: map[string]string{}}
	for _, imp := range imports {
		rel, ok := underRoot(root, imp)
		if !ok {
			out.Imports = append(out.Imports, imp)
			continue
		}
		out.Imports = append(out.Imports, rel)
		if inNodeModules(rel) {
			continue
		}
		sum, err := fileSum(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("the document's import %s: %w", rel, err)
		}
		out.Sources[rel] = sum
	}
	slices.Sort(out.Imports)
	out.Imports = slices.Compact(out.Imports)
	return out, nil
}

// Document returns the document the module file of the service at
// servicePath exported when the directory was prepared, with the files it
// imported, absolute. It refuses a document the directory lacks, and one
// whose module or imports under root changed since.
func (p *Prepared) Document(root, servicePath, file string) (json.RawMessage, []string, error) {
	key, err := DocumentKey(root, servicePath, file)
	if err != nil {
		return nil, nil, err
	}
	d := p.Documents[key]
	if d == nil {
		return nil, nil, fmt.Errorf("%s: %s holds no document of it, and a stack command given --prepared runs no module; run `superschematic stack prepare` again", key, p.Dir)
	}
	for _, rel := range sortedKeys(d.Sources) {
		sum, err := fileSum(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || sum != d.Sources[rel] {
			return nil, nil, fmt.Errorf("%s: %s changed since %s was prepared; run `superschematic stack prepare` again", key, rel, p.Dir)
		}
	}
	imports := make([]string, len(d.Imports))
	for i, imp := range d.Imports {
		if filepath.IsAbs(imp) {
			imports[i] = imp
		} else {
			imports[i] = filepath.Join(root, filepath.FromSlash(imp))
		}
	}
	return d.Document, imports, nil
}

// underRoot returns p relative to root, slash separated, when it lies
// under it.
//
// The real path comes first: a workspace package a module imports through
// a node_modules link is the package's own source, which the replay must
// see change, and not a file under node_modules.
func underRoot(root, p string) (string, bool) {
	for _, r := range []string{evalSymlinks(root), root} {
		for _, q := range []string{evalSymlinks(p), p} {
			rel, err := filepath.Rel(r, q)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return filepath.ToSlash(rel), true
			}
		}
	}
	return "", false
}

func evalSymlinks(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

func inNodeModules(rel string) bool {
	return slices.Contains(strings.Split(rel, "/"), "node_modules")
}

// ReadPrepared reads the directory `stack prepare` wrote at dir.
func ReadPrepared(dir string) (*Prepared, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(abs, PreparedFile))
	if err != nil {
		return nil, fmt.Errorf("--prepared %s: %w; run `superschematic stack prepare`, which writes it", dir, err)
	}
	p := &Prepared{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p); err != nil {
		return nil, fmt.Errorf("--prepared %s: %s: %w", dir, PreparedFile, err)
	}
	p.Dir = abs
	check := func(what, name, archive, digest string) error {
		if !digestPattern.MatchString(digest) {
			return fmt.Errorf("--prepared %s: the %s of %s has digest %q", dir, what, name, digest)
		}
		if archive == "" || path.IsAbs(archive) || path.Clean(archive) != archive || strings.HasPrefix(archive, "../") || archive == ".." {
			return fmt.Errorf("--prepared %s: the %s of %s is at %q, not a path in the directory", dir, what, name, archive)
		}
		return nil
	}
	for name, c := range p.Contexts {
		if err := check("build context", name, c.Archive, c.Digest); err != nil {
			return nil, err
		}
	}
	for name, a := range p.Sites {
		if err := check("files", name, a.Archive, a.Digest); err != nil {
			return nil, err
		}
	}
	for name, a := range p.Bundles {
		if err := check("bundle", name, a.Archive, a.Digest); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// CheckFor refuses a directory prepared for another stack or without
// environment, and, when build is set, one prepared with --no-build.
func (p *Prepared) CheckFor(stack, environment string, build bool) error {
	if p.Stack != stack {
		return fmt.Errorf("--prepared %s was prepared for stack %s, not %s", p.Dir, p.Stack, stack)
	}
	if build && !p.Built {
		return fmt.Errorf("--prepared %s holds the documents alone (stack prepare --no-build): prepare %s without --no-build, or build nothing with --no-build", p.Dir, environment)
	}
	if len(p.Environments) > 0 && !slices.Contains(p.Environments, environment) {
		return fmt.Errorf("--prepared %s was prepared for %s, not %s: run `superschematic stack prepare %s`", p.Dir, strings.Join(p.Environments, ", "), environment, environment)
	}
	return nil
}

// context returns the archive of a prepared build context, checked
// against its digest, and the context.
func (p *Prepared) context(pc *PreparedContext) (string, *Context, error) {
	archive := filepath.Join(p.Dir, filepath.FromSlash(pc.Archive))
	f, err := os.Open(archive)
	if err != nil {
		return "", nil, fmt.Errorf("the prepared build context: %w", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", nil, fmt.Errorf("the prepared build context %s: %w", archive, err)
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, gz); err != nil {
		return "", nil, fmt.Errorf("the prepared build context %s: %w", archive, err)
	}
	if got := "sha256:" + hex.EncodeToString(sum.Sum(nil)); got != pc.Digest {
		return "", nil, fmt.Errorf("the prepared build context %s has digest %s, not %s", archive, got, pc.Digest)
	}
	return archive, &Context{Digest: pc.Digest, Dockerfile: pc.Dockerfile, Files: pc.Files, Size: pc.Size}, nil
}

// extract checks the archive of a, a site's files or a bundle, what,
// against its digest and extracts it into dest.
func (p *Prepared) extract(a *PreparedArtifact, what, dest string) (*ArtifactFiles, error) {
	archive := filepath.Join(p.Dir, filepath.FromSlash(a.Archive))
	data, err := os.ReadFile(archive)
	if err != nil {
		return nil, fmt.Errorf("the prepared %s: %w", what, err)
	}
	sum := sha256.Sum256(data)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != a.Digest {
		return nil, fmt.Errorf("the prepared %s %s has digest %s, not %s", what, archive, got, a.Digest)
	}
	if err := untar(bytes.NewReader(data), dest); err != nil {
		return nil, fmt.Errorf("the prepared %s %s: %w", what, archive, err)
	}
	files, err := digestFiles(dest, what)
	if err != nil {
		return nil, err
	}
	if files.Digest != a.Digest {
		return nil, fmt.Errorf("the prepared %s %s extracted to files of digest %s, not %s", what, archive, files.Digest, a.Digest)
	}
	return files, nil
}

// untar extracts the directories and regular files of a tar stream
// digestFilesTo wrote into dest, each file executable when its mode says.
func untar(r io.Reader, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(hdr.Name, "/")
		// A name is slash-separated, relative and clean, holds no
		// backslash, which Windows reads as a separator and so as a way out
		// of dest, and is local on this platform (no drive, no reserved
		// name).
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") ||
			strings.Contains(name, `\`) || !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("entry %q is not a path in the archive", hdr.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			mode := fs.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("entry %s is neither a directory nor a file", name)
		}
	}
}

// PrepareOptions is one `stack prepare`.
type PrepareOptions struct {
	// Stack names the stack, and Environments are the environments to
	// prepare, resolved.
	Stack        string
	Environments []*ir.ResolvedEnvironment

	// Sources says where the stack's build wrote, and where the
	// repository and the sites' packages are. Nil builds nothing: the
	// directory holds the documents alone.
	Sources *Sources

	// Documents are the sidecar documents to keep, by DocumentKey.
	Documents map[string]*PreparedDocument

	// Dir is the directory to write; it is replaced when it holds an
	// earlier prepare, and refused when it holds anything else.
	Dir string

	// Provenance is recorded in the directory: CIProvenance in CI.
	Provenance Provenance

	// Log receives progress.
	Log io.Writer
}

// Prepare writes a prepared directory: the documents, and when Sources is
// set, the build context of each server, job and worker of the
// environments whose Dockerfile the build wrote, and each site and bundle,
// built (D47, amended). Like a deploy, it builds a site and a bundle after
// one frozen install of the workspace, each with no credential in its
// environment.
func Prepare(ctx context.Context, o PrepareOptions) (*Prepared, error) {
	s := &session{log: o.Log}
	if s.log == nil {
		s.log = io.Discard
	}
	if err := resetPrepared(o.Dir); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	p := &Prepared{Dir: abs, Stack: o.Stack, Built: o.Sources != nil, Provenance: o.Provenance, Documents: o.Documents}
	for _, env := range o.Environments {
		p.Environments = append(p.Environments, env.Environment)
	}
	if o.Sources != nil {
		if err := p.build(ctx, s, o.Sources, o.Environments); err != nil {
			return nil, err
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(abs, PreparedFile), append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	return p, nil
}

// build writes the contexts, sites and bundles of the environments.
func (p *Prepared) build(ctx context.Context, s *session, src *Sources, envs []*ir.ResolvedEnvironment) error {
	images, sites, bundles := map[string]*ir.ResolvedDeployable{}, map[string]*ir.ResolvedDeployable{}, map[string]*ir.ResolvedDeployable{}
	for _, env := range envs {
		for _, d := range env.Deployables {
			if d.HasImage() && images[d.Name] == nil {
				images[d.Name] = d
			}
			if d.Kind == ir.DeployableSite && sites[d.Name] == nil {
				sites[d.Name] = d
			}
			if d.HasBundle() && bundles[d.Name] == nil {
				bundles[d.Name] = d
			}
		}
	}
	// The contexts come first, as a deploy writes them before it builds
	// a site or a bundle, whose output may lie in the repository.
	for _, name := range sortedKeys(images) {
		d := images[name]
		what := string(d.Kind) + " " + name
		dockerfile := src.Dockerfile(p.Stack, name)
		if _, err := os.Stat(dockerfile); errors.Is(err, fs.ErrNotExist) {
			s.logf("%s: no Dockerfile at %s, so its image comes from --image or the manifest", what, dockerfile)
			continue
		} else if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(p.Dir, "contexts"), 0o755); err != nil {
			return err
		}
		rel := "contexts/" + name + ".tar.gz"
		c, err := writeContextFile(filepath.Join(p.Dir, filepath.FromSlash(rel)), src.RepositoryRoot, dockerfile)
		if err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		s.logf("%s: context %s, %d files, %s", what, shortDigest(c.Digest), c.Files, byteSize(c.Size))
		if p.Contexts == nil {
			p.Contexts = map[string]*PreparedContext{}
		}
		p.Contexts[name] = &PreparedContext{Archive: rel, Digest: c.Digest, Dockerfile: c.Dockerfile, Files: c.Files, Size: c.Size}
	}
	for _, name := range sortedKeys(sites) {
		s.env = envOf(envs, name)
		out, err := s.buildSite(ctx, src, sites[name])
		if err != nil {
			return err
		}
		a, err := p.keep(out, "site", "sites/"+name+".tar")
		if err != nil {
			return fmt.Errorf("site %s: %w", name, err)
		}
		s.logf("site %s: files %s, %d files, %s", name, shortDigest(a.Digest), a.Files, byteSize(a.Size))
		if p.Sites == nil {
			p.Sites = map[string]*PreparedArtifact{}
		}
		p.Sites[name] = a
	}
	for _, name := range sortedKeys(bundles) {
		s.env = envOf(envs, name)
		out, err := os.MkdirTemp("", "superschematic-bundle-")
		if err != nil {
			return err
		}
		built, err := s.buildBundle(ctx, src, bundles[name], nil, out)
		if err == nil && built {
			var a *PreparedArtifact
			if a, err = p.keep(out, "bundle", "bundles/"+name+".tar"); err == nil {
				s.logf("%s: bundle %s, %d files, %s", kindOf(s.env, name), shortDigest(a.Digest), a.Files, byteSize(a.Size))
				if p.Bundles == nil {
					p.Bundles = map[string]*PreparedArtifact{}
				}
				p.Bundles[name] = a
			}
		}
		_ = os.RemoveAll(out)
		if err != nil {
			return err
		}
	}
	return nil
}

// envOf returns the first of envs that has deployable name.
func envOf(envs []*ir.ResolvedEnvironment, name string) *ir.ResolvedEnvironment {
	for _, env := range envs {
		if env.Deployable(name) != nil {
			return env
		}
	}
	return nil
}

// keep writes the files under dir, a site's or a bundle's, what, as the
// tar stream their digest is of, to rel in the directory.
func (p *Prepared) keep(dir, what, rel string) (*PreparedArtifact, error) {
	target := filepath.Join(p.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(target)
	if err != nil {
		return nil, err
	}
	files, err := digestFilesTo(dir, what, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return &PreparedArtifact{Archive: rel, Digest: files.Digest, Files: files.Files, Size: files.Size}, nil
}

// resetPrepared empties dir for a prepare: a directory that does not
// exist, is empty or holds an earlier prepare. It refuses one that holds
// anything else, which a mistyped --dir would otherwise delete.
func resetPrepared(dir string) error {
	if dir == "" {
		return errors.New("stack prepare: no directory to write")
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return os.MkdirAll(dir, 0o755)
	case err != nil:
		return err
	case len(entries) == 0:
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, PreparedFile)); err != nil {
		return fmt.Errorf("%s holds files and no %s, so it is no directory stack prepare wrote: name an empty or new one", dir, PreparedFile)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// PreparedDir is where `stack prepare` writes unless told otherwise:
// <output-root>/prepared/<stack>.
func PreparedDir(outputRoot, stack string) string {
	return filepath.Join(outputRoot, "prepared", stack)
}
