package tasks

import (
	"bufio"
	"bytes"
	"crypto/sha1" //nolint:gosec // git's object id of a blob in a SHA-1 repository, not a signature
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/parable-work/superschematic/internal/registry"
)

// ManifestLimit is the largest tracked file a resolver reads, in bytes.
// A resolver parses manifests somebody else may have written, so each has
// a bound.
const ManifestLimit = 1 << 20

// The modes git gives a tree's entries.
const (
	ModeFile       = "100644"
	ModeExecutable = "100755"
	ModeSymlink    = "120000"
	ModeSubmodule  = "160000"
)

// The object formats a repository may have.
const (
	FormatSHA1   = "sha1"
	FormatSHA256 = "sha256"
)

// TooLargeError is a tree's refusal to read a file over its limit.
type TooLargeError struct {
	Path  string
	Size  int64
	Limit int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("%s is %d bytes, over the %d a resolver reads of one file", e.Path, e.Size, e.Limit)
}

// tree holds a commit's entries; its read gives an entry's bytes.
type tree struct {
	entries []registry.TaskTreeEntry
	index   map[string]int
	limit   int64
	read    func(entry registry.TaskTreeEntry, limit int64) ([]byte, error)
}

func newTree(entries []registry.TaskTreeEntry, read func(registry.TaskTreeEntry, int64) ([]byte, error)) *tree {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	index := make(map[string]int, len(entries))
	for i, entry := range entries {
		index[entry.Path] = i
	}
	return &tree{entries: entries, index: index, limit: ManifestLimit, read: read}
}

func (t *tree) Entries() []registry.TaskTreeEntry { return t.entries }

func (t *tree) Entry(path string) (registry.TaskTreeEntry, bool) {
	i, ok := t.index[path]
	if !ok {
		return registry.TaskTreeEntry{}, false
	}
	return t.entries[i], true
}

func (t *tree) Read(path string) ([]byte, error) {
	entry, ok := t.Entry(path)
	if !ok {
		return nil, fmt.Errorf("%s is not a tracked file of the commit", path)
	}
	if entry.Mode == ModeSubmodule {
		return nil, fmt.Errorf("%s is a submodule, whose files are another repository's", path)
	}
	return t.read(entry, t.limit)
}

// MemoryFile is one file of a MemoryTree: its bytes and its mode, a plain
// file's when Mode is empty.
type MemoryFile struct {
	Data []byte
	Mode string
}

// MemoryTree is a tree held in memory, for a test or a reader that has a
// commit's files and no repository. Each file's id is the id git would
// give its blob in a repository of the format, so a memory tree and a git
// tree of the same files give the same digests.
func MemoryTree(format string, files map[string]MemoryFile) registry.TaskTree {
	entries := make([]registry.TaskTreeEntry, 0, len(files))
	for name, file := range files {
		mode := file.Mode
		if mode == "" {
			mode = ModeFile
		}
		entries = append(entries, registry.TaskTreeEntry{Path: name, Mode: mode, ID: BlobID(format, file.Data)})
	}
	return newTree(entries, func(entry registry.TaskTreeEntry, limit int64) ([]byte, error) {
		data := files[entry.Path].Data
		if int64(len(data)) > limit {
			return nil, &TooLargeError{Path: entry.Path, Size: int64(len(data)), Limit: limit}
		}
		return bytes.Clone(data), nil
	})
}

// BlobID is the object id git gives a blob of data in a repository of
// the format.
func BlobID(format string, data []byte) string {
	var h hash.Hash
	if format == FormatSHA256 {
		h = sha256.New()
	} else {
		h = sha1.New() //nolint:gosec // see the import
	}
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// Repository reads commits of a git repository. It starts only git, with
// replace objects off, so an id names the bytes it was made from, and it
// reads objects only: no working tree, no index, no hook and no filter.
type Repository struct {
	dir    string
	format string

	mu    sync.Mutex
	batch *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader

	// blobs holds the blobs read so far by id, once CacheBlobs asked for
	// it: a walk of many commits reads the same blob at each.
	blobs map[string][]byte
}

// CacheBlobs keeps each blob a tree of the repository reads, up to the
// limit a resolver reads of one file, so a second commit that holds the
// same blob reads it from memory.
func (r *Repository) CacheBlobs() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blobs == nil {
		r.blobs = map[string][]byte{}
	}
}

// Root is the repository's top directory.
func (r *Repository) Root() (string, error) {
	out, err := r.git("rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// History is one commit of a walk: its id and its first parent's, "" for
// a commit that has none.
type History struct {
	Commit string
	Parent string
}

// Log returns up to n commits, newest first, that changed a tracked file
// under dir, walking first parents back from revision: the commits a
// branch took one by one.
func (r *Repository) Log(revision, dir string, n int) ([]History, error) {
	start, err := r.Commit(revision)
	if err != nil {
		return nil, err
	}
	args := []string{"log", "--first-parent", "--format=%H %P", "-n", strconv.Itoa(n), start, "--"}
	if dir != "" {
		// From the repository's top, whatever directory of it the
		// repository was opened in, and read as a path, not a pattern.
		args = append(args, ":(top,literal)"+dir)
	}
	out, err := r.git(args...)
	if err != nil {
		return nil, err
	}
	var history []History
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		entry := History{Commit: fields[0]}
		if len(fields) > 1 {
			entry.Parent = fields[1]
		}
		history = append(history, entry)
	}
	return history, nil
}

// OpenRepository opens the repository that holds dir.
func OpenRepository(dir string) (*Repository, error) {
	r := &Repository{dir: dir}
	format, err := r.git("rev-parse", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf("%s is in no git repository a plan can read: %w", dir, err)
	}
	r.format = strings.TrimSpace(string(format))
	if r.format != FormatSHA1 && r.format != FormatSHA256 {
		return nil, fmt.Errorf("repository at %s has object format %q; a plan reads sha1 and sha256", dir, r.format)
	}
	return r, nil
}

// Format is the repository's object format, sha1 or sha256.
func (r *Repository) Format() string { return r.format }

// Close ends the process the repository reads blobs through.
func (r *Repository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.batch == nil {
		return nil
	}
	_ = r.in.Close()
	err := r.batch.Wait()
	r.batch = nil
	return err
}

// git runs one git command in the repository and returns what it wrote.
func (r *Repository) git(args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--no-replace-objects", "-C", r.dir}, args...)...) //nolint:gosec // the arguments are this package's own
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Commit resolves a revision to its commit's id.
func (r *Repository) Commit(revision string) (string, error) {
	if revision == "" || strings.HasPrefix(revision, "-") {
		return "", fmt.Errorf("%q names no commit", revision)
	}
	out, err := r.git("rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s names no commit of the repository: %w", revision, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Tree reads the tree of a commit, by the id Commit gives: every tracked
// entry with its mode and object id.
func (r *Repository) Tree(commit string) (registry.TaskTree, error) {
	out, err := r.git("ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, err
	}
	var entries []registry.TaskTreeEntry
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		head, name, ok := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(head))
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("git ls-tree wrote a record this reader does not know: %q", record)
		}
		entries = append(entries, registry.TaskTreeEntry{Path: string(name), Mode: fields[0], ID: fields[2]})
	}
	return newTree(entries, r.blob), nil
}

// File reads one tracked file of a commit whole, with no limit: the task
// file the plan is made from.
func (r *Repository) File(commit, path string) ([]byte, error) {
	out, err := r.git("ls-tree", "-z", "--full-tree", commit, "--", path)
	if err != nil {
		return nil, err
	}
	head, _, ok := bytes.Cut(bytes.TrimRight(out, "\x00"), []byte{'\t'})
	fields := strings.Fields(string(head))
	if !ok || len(fields) != 3 || fields[1] != "blob" {
		return nil, fmt.Errorf("commit %s holds no file %s", commit, path)
	}
	return r.blob(registry.TaskTreeEntry{Path: path, Mode: fields[0], ID: fields[2]}, -1)
}

// Start starts the process the repository reads blobs through, which a
// read would start otherwise. A program that takes the sandbox calls it
// first: the sandbox starts no process.
func (r *Repository) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.start()
}

// start starts `git cat-file --batch` once. The caller holds the lock.
func (r *Repository) start() error {
	if r.batch != nil {
		return nil
	}
	cmd := exec.Command("git", "--no-replace-objects", "-C", r.dir, "cat-file", "--batch") //nolint:gosec // the arguments are this package's own
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git cat-file: %w", err)
	}
	r.batch, r.in, r.out = cmd, in, bufio.NewReader(out)
	return nil
}

// blob reads an entry's blob through one long-lived `git cat-file
// --batch`. A negative limit reads a blob of any size.
func (r *Repository) blob(entry registry.TaskTreeEntry, limit int64) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if data, ok := r.blobs[entry.ID]; ok && (limit < 0 || int64(len(data)) <= limit) {
		return bytes.Clone(data), nil
	}
	if err := r.start(); err != nil {
		return nil, err
	}
	if _, err := io.WriteString(r.in, entry.ID+"\n"); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	header, err := r.out.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	fields := strings.Fields(header)
	if len(fields) != 3 || fields[1] != "blob" {
		return nil, fmt.Errorf("%s: object %s is no blob (%s)", entry.Path, entry.ID, strings.TrimSpace(header))
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	if limit >= 0 && size > limit {
		// The blob and its newline are still read, so the next request
		// starts at a header.
		if _, err := io.CopyN(io.Discard, r.out, size+1); err != nil {
			return nil, fmt.Errorf("git cat-file: %w", err)
		}
		return nil, &TooLargeError{Path: entry.Path, Size: size, Limit: limit}
	}
	data := make([]byte, size+1)
	if _, err := io.ReadFull(r.out, data); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	if r.blobs != nil && size <= ManifestLimit {
		r.blobs[entry.ID] = bytes.Clone(data[:size])
	}
	return data[:size], nil
}
