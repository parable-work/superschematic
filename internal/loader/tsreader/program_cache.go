package tsreader

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/parable-work/superschematic/internal/profile"
)

// ProgramCache is a build-all scoped Corsa program cache. It lets a single
// TypeScript program serve multiple schema services whose tsconfig settings
// are compatible.
type ProgramCache struct {
	serviceDirs []string

	loadMu sync.Mutex
	mu     sync.Mutex
	corsa  *corsaProgram
	diags  []*astDiagnostic
}

// NewProgramCache creates a shared-program cache for TypeScript schema services.
func NewProgramCache(serviceDirs []string) *ProgramCache {
	dirs := make([]string, 0, len(serviceDirs))
	for _, dir := range serviceDirs {
		abs, err := filepath.Abs(dir)
		if err != nil {
			dirs = append(dirs, dir)
			continue
		}
		dirs = append(dirs, filepath.ToSlash(abs))
	}
	sort.Strings(dirs)
	return &ProgramCache{serviceDirs: dirs}
}

func (c *ProgramCache) lockLoad() func() {
	c.loadMu.Lock()
	return c.loadMu.Unlock
}

func (c *ProgramCache) corsaProgram(prof *profile.Profiler) (*corsaProgram, []*astDiagnostic, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.corsa != nil || len(c.diags) > 0 {
		return c.corsa, c.diags, nil
	}
	if len(c.serviceDirs) == 0 {
		return nil, nil, fmt.Errorf("shared TypeScript program has no service directories")
	}

	corsa, diags, err := newWorkspaceCorsaProgram(c.serviceDirs, prof)
	if err != nil {
		return nil, nil, err
	}
	c.corsa = corsa
	c.diags = diags
	return c.corsa, c.diags, nil
}

// Close releases the shared program checker.
func (c *ProgramCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.corsa != nil {
		c.corsa.close()
		c.corsa = nil
	}
}

func workspaceRootFileNames(serviceDirs []string) ([]string, error) {
	var files []string
	for _, dir := range serviceDirs {
		configPath := filepath.Join(dir, "schema.config.ts")
		if info, err := os.Stat(configPath); err == nil && !info.IsDir() {
			files = append(files, filepath.ToSlash(configPath))
		}
		srcDir := filepath.Join(dir, "src")
		if _, err := os.Stat(srcDir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if err := filepath.WalkDir(srcDir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".test.ts") {
				return nil
			}
			files = append(files, filepath.ToSlash(path))
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}
