package loader

import "github.com/parable-work/superschematic/internal/loader/tsreader"

type (
	// DeclarationInput is the in-memory source of a DeclarationProgram:
	// files by relative path, the roots the program starts from, and the
	// compiler lib files; see internal/loader/tsreader.DeclarationInput.
	DeclarationInput = tsreader.DeclarationInput

	// DeclarationProgram is a type-checked TypeScript program over
	// in-memory files, built with the compiler and module resolution the
	// schema loader uses, for an extension or tool that evaluates types
	// the schema frontend does not walk; see
	// internal/loader/tsreader.DeclarationProgram.
	DeclarationProgram = tsreader.DeclarationProgram

	// SchemaError is one located diagnostic, file:line:col: message. Load
	// errors from TypeScript services and DeclarationProgram diagnostics
	// both use it.
	SchemaError = tsreader.SchemaError

	// SchemaErrorList is several SchemaErrors, one per line.
	SchemaErrorList = tsreader.SchemaErrorList
)

// NewDeclarationProgram type-checks in's files from its roots; see
// internal/loader/tsreader.NewDeclarationProgram. The caller reads the
// result with Checker, SourceFile, Diagnostics and ErrorAt, and calls Close.
func NewDeclarationProgram(in DeclarationInput) (*DeclarationProgram, error) {
	return tsreader.NewDeclarationProgram(in)
}
