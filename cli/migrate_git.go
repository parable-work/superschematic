package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/archive"
)

// extractSchemasRoot writes the schemas root as it is at ref into a new
// directory beside it and returns that directory. Beside it, the paths a
// tsconfig reaches outside the root (the authoring packages, node_modules)
// resolve as they do from the checkout. found is false when the schemas
// root does not exist at ref. cleanup removes the directory; it is never
// nil.
func extractSchemasRoot(ctx context.Context, schemasRoot, ref string) (dir string, found bool, cleanup func(), err error) {
	cleanup = func() {}
	commit, err := gitOutput(ctx, schemasRoot, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", false, cleanup, fmt.Errorf("--from-ref %s: not a commit of the git repository at %s: %w", ref, schemasRoot, err)
	}
	// git archive run in a subdirectory archives only that subdirectory of
	// the tree it is given, so it runs at the top level with the schemas
	// root's own tree.
	top, err := gitOutput(ctx, schemasRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false, cleanup, fmt.Errorf("--from-ref: %w", err)
	}
	prefix, err := gitOutput(ctx, schemasRoot, "rev-parse", "--show-prefix")
	if err != nil {
		return "", false, cleanup, fmt.Errorf("--from-ref: %w", err)
	}
	tree := commit
	if prefix = strings.TrimSuffix(prefix, "/"); prefix != "" {
		tree = commit + ":" + prefix
		if kind, err := gitOutput(ctx, schemasRoot, "cat-file", "-t", tree); err != nil || kind != "tree" {
			return "", false, cleanup, nil
		}
	}

	dir, err = os.MkdirTemp(filepath.Dir(schemasRoot), ".superschematic-migrate-")
	if err != nil {
		return "", false, cleanup, fmt.Errorf("--from-ref: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	gitArchive := exec.CommandContext(ctx, "git", "archive", "--format=tar", tree)
	gitArchive.Dir = top
	var stderr bytes.Buffer
	gitArchive.Stderr = &stderr
	stdout, err := gitArchive.StdoutPipe()
	if err == nil {
		err = gitArchive.Start()
	}
	if err != nil {
		cleanup()
		return "", false, func() {}, fmt.Errorf("--from-ref: git archive: %w", err)
	}
	extractErr := untar(stdout, dir)
	// Drain what untar left so git can exit.
	_, _ = io.Copy(io.Discard, stdout)
	if err := errors.Join(gitArchive.Wait(), extractErr); err != nil {
		cleanup()
		return "", false, func() {}, fmt.Errorf("--from-ref: extracting %s at %s: %w %s", schemasRoot, ref, err, strings.TrimSpace(stderr.String()))
	}
	return dir, true, cleanup, nil
}

// untar writes the directories, files and links of a tar stream
// under dir. An entry whose path leaves dir is an error, and so is a link
// that points outside dir: a later entry could otherwise write through it.
// Other entries, such as the pax header git writes with the commit id,
// carry nothing to extract.
func untar(r io.Reader, dir string) error {
	return archive.ExtractTar(r, dir, archive.Options{Links: true, SkipOther: true})
}

// gitOutput runs git in dir and returns its trimmed standard output.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}
