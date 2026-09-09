// Package generator owns the superschematic code-generation surface: the shadow
// output layout and the dispatch that runs each generator against the
// Schema IR. The typed interpretation of the schema.config outputs block
// lives in internal/registry and is aliased here.
package generator

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
)

// Target languages accepted in the outputs block (the TargetLanguage enum in
// @superschematic/schema-config).
const (
	LangGo         = registry.LangGo
	LangTypeScript = registry.LangTypeScript
	LangPython     = registry.LangPython
	LangRust       = registry.LangRust
)

// API server language and protocol values.
const (
	APILanguageGo   = registry.APILanguageGo
	APILanguageRust = registry.APILanguageRust
	APIProtocolREST = registry.APIProtocolREST
)

// The outputs block types, aliased from internal/registry.
type (
	Outputs            = registry.Outputs
	TargetOutputConfig = registry.TargetOutputConfig
	APIOutputConfig    = registry.APIOutputConfig
)

// ParseOutputs decodes the raw outputs block from schema.config against the
// core registry, for callers that have no registry of their own (buildplan,
// tests). Run parses against Options.Registry.
func ParseOutputs(raw map[string]any) (*Outputs, error) {
	return registry.ParseOutputs(raw, CoreRegistry(naming.Default()))
}

// ExpectedOutputDirs returns the service-scoped output directories that may be
// materialized for cfg. Cache orchestration uses these as the per-service
// restore/store surface; store operations still ignore directories that a
// generator legitimately skipped. serviceDir is the service source directory:
// every registered document whose sidecar File exists there contributes its
// Dirs (the install targets documents write outside the output root are
// committed, not cache surface).
//
// The generator directories come from the registry: every generator in the
// kind's pipeline whose Enabled check passes (or that has none) contributes
// its Dirs, so the cache surface and the run agree by construction. A nil reg
// means the core registry.
func ExpectedOutputDirs(root string, cfg *schemaconfig.SchemaConfig, serviceDir string, reg *registry.Registry) ([]string, error) {
	if reg == nil {
		reg = CoreRegistry(naming.Default())
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, reg)
	if err != nil {
		return nil, fmt.Errorf("schema config for %s: %w", cfg.Name, err)
	}

	gc := registry.GenerateContext{
		Config:   cfg,
		Outputs:  outputs,
		Options:  registry.Options{OutputRoot: root},
		Registry: reg,
	}
	var dirs []string
	if serviceDir != "" {
		for _, spec := range reg.Documents() {
			if spec.File == "" || spec.Dirs == nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(serviceDir, spec.File)); err == nil {
				dirs = append(dirs, spec.Dirs(gc)...)
			}
		}
	}
	for _, spec := range reg.Pipeline(string(cfg.Kind)) {
		if spec.Dirs == nil {
			continue
		}
		if spec.Enabled != nil {
			if ok, _ := spec.Enabled(gc); !ok {
				continue
			}
		}
		dirs = append(dirs, spec.Dirs(gc)...)
	}

	seen := make(map[string]bool, len(dirs))
	var unique []string
	for _, dir := range dirs {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		unique = append(unique, dir)
	}
	return unique, nil
}
