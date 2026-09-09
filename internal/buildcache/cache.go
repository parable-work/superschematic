// Package buildcache implements the cross-worktree cache for schema outputs.
package buildcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

const (
	CacheVersion    = "v1"
	CacheFormat     = "v2"
	RepoPlaceholder = "@@REPO_ROOT@@"
)

var hashSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "bin": true, "target": true,
	"__pycache__": true, ".venv": true, ".pytest_cache": true, ".ruff_cache": true,
}

var linkDirNames = map[string]bool{"node_modules": true}
var storeSkipDirs = map[string]bool{"target": true}
var precleanKeep = map[string]bool{"node_modules": true, "target": true}
var toolchainFingerprintOnce sync.Once
var toolchainFingerprintValue string

// DefaultRoot returns the schema build cache root when no --cache-root flag
// is given: the SUPERSCHEMATIC_BUILD_CACHE_DIR environment variable, then the
// [cache] root from superschematic.toml, then the XDG cache directory.
func DefaultRoot(configured string) string {
	if env := os.Getenv("SUPERSCHEMATIC_BUILD_CACHE_DIR"); env != "" {
		return filepath.Join(expandHome(env), CacheFormat)
	}
	if configured != "" {
		return filepath.Join(expandHome(configured), CacheFormat)
	}
	xdg := os.Getenv("XDG_CACHE_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		xdg = filepath.Join(home, ".cache")
	}
	return filepath.Join(xdg, "superschematic", "build", CacheFormat)
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// TreeDigest returns a deterministic content digest of root.
func TreeDigest(root string) string {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "missing"
	}
	var entries []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.IsDir() && hashSkipDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries = append(entries, "L "+rel+" "+target)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return err
		}
		entries = append(entries, "F "+rel+" "+sum)
		return nil
	})
	if err != nil {
		return "error"
	}
	sort.Strings(entries)
	h := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(h[:])
}

func fileSHA256(path string) (sum string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ToolchainFingerprint returns versions of external tools that shape outputs.
// SchemasDir is the schemas root's directory name under the repository root
// ("<repoRoot>/<SchemasDir>/services", "<repoRoot>/<SchemasDir>/dist").
// build-all sets it from the services root it is given; the default matches
// the layout the README describes.
var SchemasDir = "schemas"

// toolDigestOverride lets a binary that embeds its own source digest (or a
// test) pin the tool component of every cache key. Empty means the running
// executable's hash.
var toolDigestOverride string

// ToolDigest is the tool component of every cache key: a hash of the
// running executable, so a rebuilt binary invalidates entries built by the
// previous one. It is computed once per process.
func ToolDigest() string {
	if toolDigestOverride != "" {
		return toolDigestOverride
	}
	toolDigestOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			toolDigestValue = "unknown"
			return
		}
		toolDigestValue = fileHashOrMissing(exe)
	})
	return toolDigestValue
}

var (
	toolDigestOnce  sync.Once
	toolDigestValue string
)

func ToolchainFingerprint() string {
	toolchainFingerprintOnce.Do(func() {
		toolchainFingerprintValue = computeToolchainFingerprint()
	})
	return toolchainFingerprintValue
}

func computeToolchainFingerprint() string {
	commands := [][]string{
		{"go", "version"},
		{"bun", "--version"},
		{"rustc", "--version"},
		{"cargo", "--version"},
	}
	parts := make([]string, 0, len(commands))
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
		cancel()
		value := "absent"
		if err == nil {
			value = strings.TrimSpace(string(out))
		}
		parts = append(parts, args[0]+"="+value)
	}
	return strings.Join(parts, ";")
}

// InputHasher computes per-service input hashes. The expensive shared parts
// (toolchain digest, per-service tree digests) are computed once at
// construction; Recompute re-reads only the per-service volatile inputs (the
// authoring-import depfile and the files it lists), which is what changes
// during a build.
type InputHasher struct {
	base           string
	repoRoot       string
	serviceDigests map[string]string
	serviceDirs    map[string]string
}

// NewInputHasher digests the shared inputs for the given services. names is
// the resolved naming the build runs under (superschematic.toml or the
// --naming file): the key covers its values rather than the file bytes, so a
// missing file and one spelling out the defaults hash alike and a --naming
// override cannot reuse entries built under different names. Its [cache]
// inputs (repo-relative files generation reads from outside the schema
// tree, such as a permissions file) are hashed by path in the order
// declared.
func NewInputHasher(services []buildplan.Service, repoRoot string, names naming.Naming) *InputHasher {
	names = names.OrDefault()
	tool := ToolDigest()
	var extra strings.Builder
	for _, rel := range names.Cache.Inputs {
		_, _ = fmt.Fprintf(&extra, "%s=%s;", rel, fileHashOrMissing(filepath.Join(repoRoot, filepath.FromSlash(rel))))
	}
	workspace := fileHashOrMissing(filepath.Join(repoRoot, SchemasDir, "package.json"))
	lock := fileHashOrMissing(filepath.Join(repoRoot, SchemasDir, "bun.lock"))
	namesSum := sha256.Sum256([]byte(fmt.Sprintf("%+v", names)))
	base := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s", CacheVersion, tool, extra.String(), workspace, lock, hex.EncodeToString(namesSum[:]), ToolchainFingerprint())

	serviceDigests := make(map[string]string, len(services))
	serviceDirs := make(map[string]string, len(services))
	for _, service := range services {
		serviceDigests[service.Name] = TreeDigest(service.Dir)
		serviceDirs[service.Name] = service.Dir
	}
	return &InputHasher{base: base, repoRoot: repoRoot, serviceDigests: serviceDigests, serviceDirs: serviceDirs}
}

// HashAll computes hashes for topologically sorted services (dependencies
// must precede dependents so the Merkle chain resolves).
func (ih *InputHasher) HashAll(services []buildplan.Service) map[string]string {
	hashes := make(map[string]string, len(services))
	for _, service := range services {
		hashes[service.Name] = ih.Recompute(service, hashes)
	}
	return hashes
}

// Recompute hashes one service against already-computed dependency hashes.
// Called again after a build so the freshly written authoring-import depfile
// is part of the stored key (see authoring.go for why storing under the
// pre-build hash would be unsound).
func (ih *InputHasher) Recompute(service buildplan.Service, depHashes map[string]string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(ih.base))
	_, _ = h.Write([]byte("|service:" + ih.serviceDigests[service.Name]))
	if service.Config.AuthDB != "" {
		authDir := ih.serviceDirs[service.Config.AuthDB]
		if authDir == "" {
			authDir = filepath.Join(ih.repoRoot, SchemasDir, "services", service.Config.AuthDB)
		}
		_, _ = fmt.Fprintf(h, "|authdb:%s:%s", service.Config.AuthDB, TreeDigest(authDir))
	}
	for _, rel := range ReadAuthoringImports(ih.repoRoot, service.Name) {
		_, _ = fmt.Fprintf(h, "|authoring:%s:%s", rel, fileHashOrMissing(filepath.Join(ih.repoRoot, filepath.FromSlash(rel))))
	}
	var deps []string
	for _, dep := range service.Config.Dependencies {
		deps = append(deps, dep.Name)
	}
	sort.Strings(deps)
	for _, dep := range deps {
		depHash := depHashes[dep]
		if depHash == "" {
			depHash = "unknown"
		}
		_, _ = fmt.Fprintf(h, "|dep:%s:%s", dep, depHash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeInputHashes returns per-service Merkle hashes for topologically
// sorted services.
func ComputeInputHashes(services []buildplan.Service, repoRoot string, names naming.Naming) (map[string]string, error) {
	return NewInputHasher(services, repoRoot, names).HashAll(services), nil
}

func fileHashOrMissing(path string) string {
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "missing"
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return "missing"
	}
	return sum
}

func entryDir(cacheRoot, kind, name, inputHash string) string {
	prefix := inputHash
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return filepath.Join(cacheRoot, kind, name, prefix)
}

// FindEntry returns a cache entry path when its manifest exists.
func FindEntry(cacheRoot, kind, name, inputHash string) string {
	entry := entryDir(cacheRoot, kind, name, inputHash)
	if info, err := os.Stat(filepath.Join(entry, "manifest.json")); err == nil && !info.IsDir() {
		return entry
	}
	return ""
}

// EntryOutputRels returns the repo-relative output dirs recorded in an entry.
func EntryOutputRels(entry string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(entry, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	rels := make([]string, 0, len(m.Outputs))
	for _, out := range m.Outputs {
		rels = append(rels, out.Rel)
	}
	return rels, nil
}

type manifest struct {
	Kind      string           `json:"kind"`
	Name      string           `json:"name"`
	InputHash string           `json:"input_hash"`
	Outputs   []manifestOutput `json:"outputs"`
	Created   int64            `json:"created"`
}

type manifestOutput struct {
	Index int    `json:"index"`
	Rel   string `json:"rel"`
	Files int    `json:"files"`
}

// StoreEntry atomically stores existing output dirs under the cache key.
func StoreEntry(cacheRoot, kind, name, inputHash, repoRoot string, outputRels []string) error {
	final := entryDir(cacheRoot, kind, name, inputHash)
	if FindEntry(cacheRoot, kind, name, inputHash) != "" {
		return nil
	}
	staging := filepath.Join(cacheRoot, "tmp", fmt.Sprintf("%s-%s-%d", name, shortHash(inputHash), os.Getpid()))
	if err := os.RemoveAll(staging); err != nil {
		return err
	}

	var outputs []manifestOutput
	for i, rel := range outputRels {
		src := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			continue
		}
		count, err := transferTree(src, filepath.Join(staging, "outputs", fmt.Sprint(i)), repoRoot, true, false)
		if err != nil {
			_ = os.RemoveAll(staging)
			return err
		}
		outputs = append(outputs, manifestOutput{Index: i, Rel: rel, Files: count})
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest{
		Kind:      kind,
		Name:      name,
		InputHash: inputHash,
		Outputs:   outputs,
		Created:   time.Now().Unix(),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), data, 0o644); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return nil
	}
	return nil
}

func shortHash(inputHash string) string {
	if len(inputHash) <= 16 {
		return inputHash
	}
	return inputHash[:16]
}

// RestoreEntry restores a cache entry into repoRoot.
func RestoreEntry(entry, repoRoot string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(entry, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	var restored []string
	for _, out := range m.Outputs {
		dest := filepath.Join(repoRoot, filepath.FromSlash(out.Rel))
		if err := CleanOutputDirWithKeep(dest, map[string]bool{"target": true}); err != nil {
			return nil, err
		}
		count, err := transferTree(filepath.Join(entry, "outputs", fmt.Sprint(out.Index)), dest, repoRoot, false, false)
		if err != nil {
			return nil, err
		}
		if count != out.Files {
			return nil, fmt.Errorf("cache entry %s is damaged: %s restored %d files, expected %d", filepath.Base(entry), out.Rel, count, out.Files)
		}
		restored = append(restored, out.Rel)
	}
	return restored, nil
}

// DropEntry deletes a cache entry.
func DropEntry(cacheRoot, kind, name, inputHash string) error {
	return os.RemoveAll(entryDir(cacheRoot, kind, name, inputHash))
}

// PruneEntries keeps the newest entries for a name.
func PruneEntries(cacheRoot, kind, name string, keep int) error {
	parent := filepath.Join(cacheRoot, kind, name)
	entries, err := os.ReadDir(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type entryInfo struct {
		path    string
		modTime time.Time
	}
	var infos []entryInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		infos = append(infos, entryInfo{path: filepath.Join(parent, entry.Name()), modTime: info.ModTime()})
	}
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].modTime.After(infos[j].modTime)
	})
	if len(infos) > keep {
		for _, info := range infos[keep:] {
			if err := os.RemoveAll(info.path); err != nil {
				return err
			}
		}
	}
	return pruneTmp(filepath.Join(cacheRoot, "tmp"))
}

func pruneTmp(tmp string) error {
	entries, err := os.ReadDir(tmp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.RemoveAll(filepath.Join(tmp, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// CleanOutputDir removes dest contents except node_modules and target.
func CleanOutputDir(dest string) error {
	return CleanOutputDirWithKeep(dest, precleanKeep)
}

// CleanOutputDirWithKeep removes dest contents except keep entries.
func CleanOutputDirWithKeep(dest string, keep map[string]bool) error {
	entries, err := os.ReadDir(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if keep[entry.Name()] {
			continue
		}
		path := filepath.Join(dest, entry.Name())
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func transferTree(src, dst, repoRoot string, storing bool, inLinkZone bool) (int, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			if err := transferSymlink(srcPath, dstPath, repoRoot, storing); err != nil {
				return count, err
			}
			count++
			continue
		}
		if entry.IsDir() {
			if storing && storeSkipDirs[entry.Name()] && fileExists(filepath.Join(src, "Cargo.toml")) {
				continue
			}
			zone := inLinkZone || linkDirNames[entry.Name()]
			n, err := transferTree(srcPath, dstPath, repoRoot, storing, zone)
			count += n
			if err != nil {
				return count, err
			}
			continue
		}
		if inLinkZone {
			if err := linkOrCopy(srcPath, dstPath); err != nil {
				return count, err
			}
		} else if err := copyFile(srcPath, dstPath); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func transferSymlink(src, dst, repoRoot string, storing bool) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	repoPrefix := repoRoot + string(os.PathSeparator)
	if storing && strings.HasPrefix(target, repoPrefix) {
		target = RepoPlaceholder + string(os.PathSeparator) + strings.TrimPrefix(target, repoPrefix)
	}
	if !storing && strings.HasPrefix(target, RepoPlaceholder) {
		suffix := strings.TrimPrefix(target, RepoPlaceholder)
		suffix = strings.TrimPrefix(suffix, "/")
		target = filepath.Join(repoRoot, filepath.FromSlash(suffix))
	}
	return os.Symlink(target, dst)
}

func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) (err error) {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		if closeErr := out.Close(); closeErr != nil {
			return fmt.Errorf("copying %s to %s: %w (closing destination: %v)", src, dst, err, closeErr)
		}
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func stampPath(repoRoot, name string) string {
	return filepath.Join(repoRoot, SchemasDir, "dist", ".build-stamps", name)
}

// ReadStamp reads the worktree-local stamp for a schema.
func ReadStamp(repoRoot, name string) string {
	data, err := os.ReadFile(stampPath(repoRoot, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteStamp writes the worktree-local stamp for a schema.
func WriteStamp(repoRoot, name, inputHash string) error {
	path := stampPath(repoRoot, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(inputHash+"\n"), 0o644)
}
