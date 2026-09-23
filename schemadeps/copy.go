package schemadeps

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// SyncCopy keeps copyPath byte-identical to the graph at distPath, the
// <dist>/.deps.json build-all wrote. With check set, a missing or different
// copy is an error that names both files and does not touch the copy; a CI
// gate uses this form. Without it, the copy is rewritten when it differs.
//
// build-all already writes the copy when [deps] copy or --deps-copy names
// it. SyncCopy is for a command that runs later against the built output
// and must not rebuild, such as an extension's pin command.
func SyncCopy(distPath, copyPath string, check bool) error {
	want, err := os.ReadFile(distPath)
	if err != nil {
		return fmt.Errorf("schemadeps: reading %s: %w", distPath, err)
	}
	got, err := os.ReadFile(copyPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("schemadeps: reading %s: %w", copyPath, err)
	}
	if err == nil && bytes.Equal(got, want) {
		return nil
	}
	if check {
		return fmt.Errorf("schemadeps: %s does not match %s; run build-all and commit %s",
			copyPath, distPath, filepath.Base(copyPath))
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o755); err != nil {
		return err
	}
	return WriteFileAtomic(copyPath, want, 0o644)
}
