package tsreader

import (
	"fmt"
	"path"
	"strings"
)

// declarationRoot is the directory a DeclarationProgram mounts its files
// under. Nothing below it exists on disk; the file names callers see and
// pass are relative to it.
const declarationRoot = "/__declarations__"

// DeclarationInput is the source of a DeclarationProgram.
type DeclarationInput struct {
	// Files maps a relative, slash-separated path to the file's content. A
	// relative import resolves between these files, and a bare import
	// ("pkg", "pkg/sub") resolves under node_modules/ in them the way a
	// package resolves on disk. Nothing else is read: the compiler's lib
	// files come from the bundle it embeds. "tsconfig.json" at the top is
	// reserved.
	Files map[string]string
	// Roots are the files the program starts from, each a key of Files.
	// The files they import join the program; the rest of Files is loaded
	// only when imported.
	Roots []string
	// Lib names the compiler lib files, as tsconfig's "lib" spells them
	// ("ES2023", "DOM"). Empty means ES2023.
	Lib []string
}

// DeclarationProgram is a type-checked TypeScript program over in-memory
// files, built with the compiler, bundled lib files and module resolution
// the schema loader uses. It evaluates types only: nothing is emitted or
// run. The compiler options are fixed (strict, skipLibCheck off, ES2022,
// ESNext modules with bundler resolution, no automatic @types), and no
// tsconfig.json is read, so a caller's repository cannot loosen them.
//
// Checker, SourceFile and ErrorAt hand out the pinned compiler's own types
// (github.com/microsoft/typescript-go/shim/{checker,ast}); they change when
// the pin moves. A program is not safe for concurrent use. Close releases
// the checker; nothing the program returns is valid after it.
type DeclarationProgram struct {
	program *corsaProgram
}

// NewDeclarationProgram type-checks in's files from its roots. A bad path, a
// root missing from Files or a compiler option the compiler rejects (an
// unknown Lib name) is an error; diagnostics in the files themselves are
// not, and come from Diagnostics.
func NewDeclarationProgram(in DeclarationInput) (*DeclarationProgram, error) {
	if len(in.Roots) == 0 {
		return nil, fmt.Errorf("declaration program: no roots")
	}
	files := make(map[string]string, len(in.Files)+1)
	for name, content := range in.Files {
		if err := checkDeclarationPath(name); err != nil {
			return nil, err
		}
		files[declarationRoot+"/"+name] = content
	}
	roots := make([]string, 0, len(in.Roots))
	for _, root := range in.Roots {
		if _, ok := in.Files[root]; !ok {
			return nil, fmt.Errorf("declaration program: root %q is not one of the files", root)
		}
		roots = append(roots, declarationRoot+"/"+root)
	}
	lib := in.Lib
	if len(lib) == 0 {
		lib = []string{"ES2023"}
	}
	program, configDiags, err := newDeclarationCorsaProgram(declarationRoot, files, roots, lib)
	if err != nil {
		return nil, fmt.Errorf("declaration program: %w", err)
	}
	if len(configDiags) > 0 {
		return nil, fmt.Errorf("declaration program: compiler options (lib %s): %w", strings.Join(lib, ", "), relativeToDeclarationRoot(fromDiagnostics(configDiags)))
	}
	return &DeclarationProgram{program: program}, nil
}

func checkDeclarationPath(name string) error {
	if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, `\`) {
		return fmt.Errorf("declaration program: file path %q is not a clean relative slash path", name)
	}
	if name == "tsconfig.json" {
		return fmt.Errorf("declaration program: %q is reserved; the compiler options are fixed", name)
	}
	return nil
}

// Checker is the program's type checker.
func (p *DeclarationProgram) Checker() *typeChecker {
	return p.program.checker
}

// SourceFile returns the program's file named as in DeclarationInput.Files,
// or nil when that file is not in the program (not a root and never
// imported).
func (p *DeclarationProgram) SourceFile(name string) *astSourceFile {
	return p.program.sourceFile(declarationRoot + "/" + name)
}

// Diagnostics returns the syntactic and semantic diagnostics of the named
// files, in program order, with file names as in DeclarationInput.Files.
// No names means every one of the files in the program. A named file that
// is not in the program is skipped. Leaving a file out is how a caller
// trusts it: its errors are not reported, but its declarations still
// resolve. nil means no diagnostics.
func (p *DeclarationProgram) Diagnostics(names ...string) SchemaErrorList {
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[declarationRoot+"/"+name] = true
	}
	var errs SchemaErrorList
	for _, file := range p.program.sourceFiles() {
		fileName := file.FileName()
		// The compiler's lib files live outside the root.
		if !strings.HasPrefix(fileName, declarationRoot+"/") {
			continue
		}
		if len(names) > 0 && !wanted[fileName] {
			continue
		}
		errs = append(errs, fromDiagnostics(p.program.syntacticDiagnostics(file))...)
		errs = append(errs, fromDiagnostics(p.program.semanticDiagnostics(file))...)
	}
	return relativeToDeclarationRoot(errs)
}

// ErrorAt is a located error at node, in the form Diagnostics reports:
// file:line:col: message, the file named as in DeclarationInput.Files.
func (p *DeclarationProgram) ErrorAt(node *astNode, format string, args ...any) *SchemaError {
	err := errorAtNode(node, format, args...)
	err.File = strings.TrimPrefix(err.File, declarationRoot+"/")
	return err
}

// Close releases the type checker.
func (p *DeclarationProgram) Close() {
	p.program.close()
}

func relativeToDeclarationRoot(errs SchemaErrorList) SchemaErrorList {
	for _, err := range errs {
		err.File = strings.TrimPrefix(err.File, declarationRoot+"/")
	}
	return errs
}
