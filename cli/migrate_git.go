package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	archive := exec.CommandContext(ctx, "git", "archive", "--format=tar", tree)
	archive.Dir = top
	var stderr bytes.Buffer
	archive.Stderr = &stderr
	stdout, err := archive.StdoutPipe()
	if err == nil {
		err = archive.Start()
	}
	if err != nil {
		cleanup()
		return "", false, func() {}, fmt.Errorf("--from-ref: git archive: %w", err)
	}
	extractErr := untar(stdout, dir)
	// Drain what untar left so git can exit.
	_, _ = io.Copy(io.Discard, stdout)
	if err := errors.Join(archive.Wait(), extractErr); err != nil {
		cleanup()
		return "", false, func() {}, fmt.Errorf("--from-ref: extracting %s at %s: %w %s", schemasRoot, ref, err, strings.TrimSpace(stderr.String()))
	}
	return dir, true, cleanup, nil
}

// untar writes the directories, files and symbolic links of a tar stream
// under dir. An entry whose path leaves dir is an error.
func untar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(header.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("archive entry %q is outside the archive", header.Name)
		}
		target := filepath.Join(dir, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			_, err = io.Copy(file, tr)
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
		// Other entries, such as the pax header git writes with the commit
		// id, carry nothing to extract.
	}
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
