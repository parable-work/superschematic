package tsreader

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/profile"
)

// serviceProgram is one Corsa program for one service: the service's
// tsconfig.json drives file inclusion and module resolution, the program is
// created once and reused across all of the service's files, and the checker
// resolves types lazily as the walk asks for them.
type serviceProgram struct {
	corsa *corsaProgram
	own   bool

	// servicePath is the absolute, slash-normalized service directory.
	servicePath string

	// configFile is the schema.config.ts source file, when the service uses
	// the TypeScript config form.
	configFile *astSourceFile

	// schemaFiles lists the service's *.schema.ts files in sorted order.
	schemaFiles []*astSourceFile
}

// newServiceProgram creates the program and classifies its source files.
func newServiceProgram(servicePath string, prof *profile.Profiler, cache *ProgramCache) (*serviceProgram, error) {
	abs, err := filepath.Abs(servicePath)
	if err != nil {
		return nil, fmt.Errorf("resolving service path %s: %w", servicePath, err)
	}
	dir := filepath.ToSlash(abs)

	var corsa *corsaProgram
	var configDiags []*astDiagnostic
	if cache != nil {
		corsa, configDiags, err = cache.corsaProgram(prof)
	} else {
		corsa, configDiags, err = newCorsaProgram(dir, prof)
	}
	if err != nil {
		return nil, err
	}
	if len(configDiags) > 0 {
		return nil, fromDiagnostics(configDiags)
	}

	sp := &serviceProgram{corsa: corsa, own: cache == nil, servicePath: dir}
	if err := prof.Measure("tsreader.program.source-classify", func() error {
		for _, file := range corsa.sourceFiles() {
			name := file.FileName()
			if !strings.HasPrefix(name, dir+"/") {
				continue
			}
			switch {
			case name == dir+"/schema.config.ts":
				sp.configFile = file
			case strings.HasSuffix(name, ".schema.ts") && strings.HasPrefix(name, dir+"/src/"):
				sp.schemaFiles = append(sp.schemaFiles, file)
			}
		}
		sort.Slice(sp.schemaFiles, func(i, j int) bool {
			return sp.schemaFiles[i].FileName() < sp.schemaFiles[j].FileName()
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return sp, nil
}

// close releases the program's checker.
func (sp *serviceProgram) close() {
	if sp.own {
		sp.corsa.close()
	}
}

// checker returns the program's type checker.
func (sp *serviceProgram) checker() *typeChecker {
	return sp.corsa.checker
}

// diagnostics runs syntactic and semantic diagnostics over the service's own
// files (config + schema files) and converts them through the diagnostics
// contract: a type error in a schema file IS a schema error.
func (sp *serviceProgram) diagnostics() SchemaErrorList {
	var errs SchemaErrorList
	files := sp.schemaFiles
	if sp.configFile != nil {
		files = append([]*astSourceFile{sp.configFile}, files...)
	}
	for _, file := range files {
		errs = append(errs, fromDiagnostics(sp.corsa.syntacticDiagnostics(file))...)
	}
	if len(errs) > 0 {
		// Semantic checking on files that do not parse produces noise.
		return errs
	}
	for _, file := range files {
		errs = append(errs, fromDiagnostics(sp.corsa.semanticDiagnostics(file))...)
	}
	return errs
}

// relPath renders a program file path relative to the service directory for
// stable Owner fields in the IR.
func (sp *serviceProgram) relPath(fileName string) string {
	if strings.HasPrefix(fileName, sp.servicePath+"/") {
		return strings.TrimPrefix(fileName, sp.servicePath+"/")
	}
	return path.Base(fileName)
}
