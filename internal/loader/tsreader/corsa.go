// Package tsreader implements the superschematic v2 TypeScript frontend: it walks
// .schema.ts files with the TypeScript 7.0 Go compiler (Project Corsa) and
// produces the v2 Schema IR.
//
// This file is the thin pinned wrapper over the compiler: it is the ONLY file
// in the package that imports the shim packages
// (github.com/microsoft/typescript-go/shim/...). The shim is its own module,
// github.com/parable-work/typescript-shim, over Microsoft's unmodified
// compiler. Everything else in tsreader programs against the local aliases
// and bindings declared here, so compiler API churn localizes to this file.
// go.mod pins the shim and the compiler, and dedicated PRs upgrade them.
package tsreader

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	tsast "github.com/microsoft/typescript-go/shim/ast"
	tsbundled "github.com/microsoft/typescript-go/shim/bundled"
	tschecker "github.com/microsoft/typescript-go/shim/checker"
	tscompiler "github.com/microsoft/typescript-go/shim/compiler"
	tscore "github.com/microsoft/typescript-go/shim/core"
	tsoptions "github.com/microsoft/typescript-go/shim/tsoptions"
	tsvfs "github.com/microsoft/typescript-go/shim/vfs"
	"github.com/microsoft/typescript-go/shim/vfs/osvfs"

	"github.com/parable-work/superschematic/internal/profile"
)

// Compiler types the rest of the package programs against.
type (
	astNode       = tsast.Node
	astSourceFile = tsast.SourceFile
	astSymbol     = tsast.Symbol
	astDiagnostic = tsast.Diagnostic
	typeChecker   = tschecker.Checker
	checkerType   = tschecker.Type
	tsProgram     = tscompiler.Program
)

// AST node kinds. Each one aliases a shim constant, never a number, because
// a compiler release can renumber the Kind enum.
const (
	kindNumericLiteral              = tsast.KindNumericLiteral
	kindStringLiteral               = tsast.KindStringLiteral
	kindIdentifier                  = tsast.KindIdentifier
	kindDecorator                   = tsast.KindDecorator
	kindPropertyDeclaration         = tsast.KindPropertyDeclaration
	kindMethodDeclaration           = tsast.KindMethodDeclaration
	kindTypeReference               = tsast.KindTypeReference
	kindArrayType                   = tsast.KindArrayType
	kindUnionType                   = tsast.KindUnionType
	kindObjectLiteralExpression     = tsast.KindObjectLiteralExpression
	kindPropertyAccessExpression    = tsast.KindPropertyAccessExpression
	kindCallExpression              = tsast.KindCallExpression
	kindClassDeclaration            = tsast.KindClassDeclaration
	kindInterfaceDeclaration        = tsast.KindInterfaceDeclaration
	kindTypeAliasDeclaration        = tsast.KindTypeAliasDeclaration
	kindEnumDeclaration             = tsast.KindEnumDeclaration
	kindHeritageClause              = tsast.KindHeritageClause
	kindSourceFile                  = tsast.KindSourceFile
	kindExtendsKeyword              = tsast.KindExtendsKeyword
	kindImplementsKeyword           = tsast.KindImplementsKeyword
	kindImportDeclaration           = tsast.KindImportDeclaration
	kindNamedImports                = tsast.KindNamedImports
	kindNamespaceImport             = tsast.KindNamespaceImport
	kindImportSpecifier             = tsast.KindImportSpecifier
	kindArrayLiteralExpression      = tsast.KindArrayLiteralExpression
	kindTrueKeyword                 = tsast.KindTrueKeyword
	kindFalseKeyword                = tsast.KindFalseKeyword
	kindNullKeyword                 = tsast.KindNullKeyword
	kindPrefixUnaryExpression       = tsast.KindPrefixUnaryExpression
	kindNoSubstitutionTemplate      = tsast.KindNoSubstitutionTemplateLiteral
	kindMinusToken                  = tsast.KindMinusToken
	kindTypeLiteral                 = tsast.KindTypeLiteral
	kindTupleType                   = tsast.KindTupleType
	kindPropertySignature           = tsast.KindPropertySignature
	kindLiteralType                 = tsast.KindLiteralType
	kindParameter                   = tsast.KindParameter
	kindEnumMember                  = tsast.KindEnumMember
	kindExpressionWithTypeArguments = tsast.KindExpressionWithTypeArguments
	kindPropertyAssignment          = tsast.KindPropertyAssignment
	kindExportAssignment            = tsast.KindExportAssignment
	kindTypeOperator                = tsast.KindTypeOperator
	kindParenthesizedType           = tsast.KindParenthesizedType
	kindQualifiedName               = tsast.KindQualifiedName
	kindComputedPropertyName        = tsast.KindComputedPropertyName
	kindStringKeyword               = tsast.KindStringKeyword
	kindNumberKeyword               = tsast.KindNumberKeyword
	kindBooleanKeyword              = tsast.KindBooleanKeyword
	kindObjectKeyword               = tsast.KindObjectKeyword
	kindAnyKeyword                  = tsast.KindAnyKeyword
	kindUnknownKeyword              = tsast.KindUnknownKeyword
	kindVariableStatement           = tsast.KindVariableStatement
	kindVariableDeclaration         = tsast.KindVariableDeclaration
	kindQuestionToken               = tsast.KindQuestionToken
	kindAsExpression                = tsast.KindAsExpression
	kindVariableDeclarationList     = tsast.KindVariableDeclarationList
	kindTemplateExpression          = tsast.KindTemplateExpression
	kindBinaryExpression            = tsast.KindBinaryExpression
)

// Modifier flags.
const (
	modifierFlagsExport   = tsast.ModifierFlagsExport
	modifierFlagsAbstract = tsast.ModifierFlagsAbstract
)

// Node flags: a variable declaration list's let and const.
const (
	nodeFlagsLet   = tsast.NodeFlagsLet
	nodeFlagsConst = tsast.NodeFlagsConst
)

// Symbol flags.
const (
	symbolFlagsAlias      = tsast.SymbolFlagsAlias
	symbolFlagsFunction   = tsast.SymbolFlagsFunction
	symbolFlagsClass      = tsast.SymbolFlagsClass
	symbolFlagsModule     = tsast.SymbolFlagsModule
	symbolFlagsInterface  = tsast.SymbolFlagsInterface
	symbolFlagsEnum       = tsast.SymbolFlagsEnum
	symbolFlagsTypeAlias  = tsast.SymbolFlagsTypeAlias
	symbolFlagsEnumMember = tsast.SymbolFlagsEnumMember
)

// Type flags.
const (
	typeFlagsString         = tschecker.TypeFlagsString
	typeFlagsNumber         = tschecker.TypeFlagsNumber
	typeFlagsBoolean        = tschecker.TypeFlagsBoolean
	typeFlagsStringLiteral  = tschecker.TypeFlagsStringLiteral
	typeFlagsNumberLiteral  = tschecker.TypeFlagsNumberLiteral
	typeFlagsBooleanLiteral = tschecker.TypeFlagsBooleanLiteral
	typeFlagsEnumLiteral    = tschecker.TypeFlagsEnumLiteral
	typeFlagsUnion          = tschecker.TypeFlagsUnion
	typeFlagsIntersection   = tschecker.TypeFlagsIntersection
	typeFlagsStringLike     = tschecker.TypeFlagsStringLike
	typeFlagsNumberLike     = tschecker.TypeFlagsNumberLike
	typeFlagsBooleanLike    = tschecker.TypeFlagsBooleanLike
)

// AST helpers.
func getSourceFileOfNode(node *astNode) *astSourceFile {
	for node != nil {
		if node.Kind == kindSourceFile {
			return node.AsSourceFile()
		}
		node = node.Parent
	}
	return nil
}

func hasSyntacticModifier(node *astNode, flags tsast.ModifierFlags) bool {
	return node.ModifierFlags()&flags != 0
}

// corsaProgram bundles a Corsa Program with its type checker and the checker
// release callback.
type corsaProgram struct {
	program *tsProgram
	checker *typeChecker
	release func()
}

// newCorsaProgram parses the service's tsconfig.json and creates one Program
// for the service. Config-parse diagnostics are returned as compiler
// diagnostics; the caller converts them through the diagnostics contract.
func newCorsaProgram(servicePath string, prof *profile.Profiler) (*corsaProgram, []*astDiagnostic, error) {
	dir := filepath.ToSlash(servicePath)

	var host tscompiler.CompilerHost
	if err := prof.Measure("tsreader.program.host", func() error {
		fs := tsbundled.WrapFS(osvfs.FS())
		host = tscompiler.NewCachedFSCompilerHost(dir, fs, tsbundled.LibPath(), nil, nil)
		return nil
	}); err != nil {
		return nil, nil, err
	}

	configFileName := dir + "/tsconfig.json"
	var config *tsoptions.ParsedCommandLine
	var configDiags []*astDiagnostic
	if err := prof.Measure("tsreader.program.config", func() error {
		if !host.FS().FileExists(configFileName) {
			return fmt.Errorf("service %s has no tsconfig.json", servicePath)
		}
		config, configDiags = tsoptions.GetParsedCommandLineOfConfigFile(configFileName, nil, nil, host, nil)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if len(configDiags) > 0 {
		return nil, configDiags, nil
	}
	if config == nil {
		return nil, nil, fmt.Errorf("failed to parse %s", configFileName)
	}

	var program *tsProgram
	if err := prof.Measure("tsreader.program.create", func() error {
		program = tscompiler.NewProgram(tscompiler.ProgramOptions{
			Host:   host,
			Config: config,
			// The walk is single-threaded per service; parallelism happens
			// across services, each with its own independent program.
			SingleThreaded: tscore.TSTrue,
		})
		return nil
	}); err != nil {
		return nil, nil, err
	}

	var checker *typeChecker
	var release func()
	if err := prof.Measure("tsreader.program.checker", func() error {
		checker, release = program.GetTypeChecker(context.Background())
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return &corsaProgram{program: program, checker: checker, release: release}, nil, nil
}

func newWorkspaceCorsaProgram(serviceDirs []string, prof *profile.Profiler) (*corsaProgram, []*astDiagnostic, error) {
	firstService := filepath.ToSlash(serviceDirs[0])
	rootFileNames, err := workspaceRootFileNames(serviceDirs)
	if err != nil {
		return nil, nil, fmt.Errorf("collect workspace TypeScript files: %w", err)
	}
	if len(rootFileNames) == 0 {
		return nil, nil, fmt.Errorf("shared TypeScript program has no root files")
	}

	var host tscompiler.CompilerHost
	if err := prof.Measure("tsreader.program.workspace-host", func() error {
		fs := tsbundled.WrapFS(osvfs.FS())
		host = tscompiler.NewCachedFSCompilerHost(firstService, fs, tsbundled.LibPath(), nil, nil)
		return nil
	}); err != nil {
		return nil, nil, err
	}

	configFileName := firstService + "/tsconfig.json"
	var config *tsoptions.ParsedCommandLine
	var configDiags []*astDiagnostic
	if err := prof.Measure("tsreader.program.workspace-config", func() error {
		config, configDiags = tsoptions.GetParsedCommandLineOfConfigFile(configFileName, nil, nil, host, nil)
		if config != nil {
			config.ParsedConfig.FileNames = rootFileNames
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if len(configDiags) > 0 {
		return nil, configDiags, nil
	}
	if config == nil {
		return nil, nil, fmt.Errorf("failed to parse %s", configFileName)
	}

	var program *tsProgram
	if err := prof.Measure("tsreader.program.workspace-create", func() error {
		program = tscompiler.NewProgram(tscompiler.ProgramOptions{
			Host:           host,
			Config:         config,
			SingleThreaded: tscore.TSTrue,
		})
		return nil
	}); err != nil {
		return nil, nil, err
	}

	var checker *typeChecker
	var release func()
	if err := prof.Measure("tsreader.program.workspace-checker", func() error {
		checker, release = program.GetTypeChecker(context.Background())
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return &corsaProgram{program: program, checker: checker, release: release}, nil, nil
}

// declarationFS serves only the files it was given. An import can never make
// the compiler read the real file system: every existence check and read is
// confined to the map, and the embedded file system answers only directory
// enumeration, for which the virtual root has no entries on disk. The
// compiler's lib files come from the bundle tsbundled.WrapFS adds around it.
type declarationFS struct {
	tsvfs.FS
	files map[string]string
}

func (f declarationFS) FileExists(name string) bool {
	_, ok := f.files[path.Clean(name)]
	return ok
}

func (f declarationFS) ReadFile(name string) (string, bool) {
	content, ok := f.files[path.Clean(name)]
	return content, ok
}

func (f declarationFS) DirectoryExists(name string) bool {
	prefix := strings.TrimSuffix(path.Clean(name), "/") + "/"
	for file := range f.files {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

func (f declarationFS) Realpath(name string) string     { return path.Clean(name) }
func (f declarationFS) UseCaseSensitiveFileNames() bool { return true }

// newDeclarationCorsaProgram builds a program over files, which are keyed by
// absolute paths under dir, starting from roots. It writes its own
// tsconfig.json into dir and never reads one from disk, so the checking
// rules cannot be loosened by the caller's repository: strict, no
// skipLibCheck, no emit, no automatic @types.
func newDeclarationCorsaProgram(dir string, files map[string]string, roots []string, lib []string) (*corsaProgram, []*astDiagnostic, error) {
	config, err := json.Marshal(map[string]any{
		"compilerOptions": map[string]any{
			"strict":           true,
			"skipLibCheck":     false,
			"noEmit":           true,
			"target":           "ES2022",
			"module":           "ESNext",
			"moduleResolution": "Bundler",
			"types":            []string{},
			"lib":              lib,
		},
		"files": roots,
	})
	if err != nil {
		return nil, nil, err
	}
	configFileName := dir + "/tsconfig.json"
	files[configFileName] = string(config)
	fs := tsbundled.WrapFS(declarationFS{FS: osvfs.FS(), files: files})
	host := tscompiler.NewCachedFSCompilerHost(dir, fs, tsbundled.LibPath(), nil, nil)
	parsed, configDiags := tsoptions.GetParsedCommandLineOfConfigFile(configFileName, nil, nil, host, nil)
	if len(configDiags) > 0 {
		return nil, configDiags, nil
	}
	if parsed == nil {
		return nil, nil, fmt.Errorf("failed to parse the declaration program's compiler options")
	}
	// An option value the compiler rejects, such as an unknown lib name, is
	// reported on the parsed config rather than returned above.
	if optionDiags := parsed.GetConfigFileParsingDiagnostics(); len(optionDiags) > 0 {
		return nil, optionDiags, nil
	}
	program := tscompiler.NewProgram(tscompiler.ProgramOptions{Host: host, Config: parsed, SingleThreaded: tscore.TSTrue})
	checker, release := program.GetTypeChecker(context.Background())
	return &corsaProgram{program: program, checker: checker, release: release}, nil, nil
}

// parseOne parses one file with the compiler's parser, in a program of that
// file alone: no lib files, no module resolution and no type checker. The
// program reads text from memory under a directory of its own, never from
// disk. It returns the parsed file and its syntax diagnostics.
func parseOne(fileName, text string) (*astSourceFile, []*astDiagnostic, error) {
	const dir = "/superschematic-parse"
	name := dir + "/" + path.Base(filepath.ToSlash(fileName))
	config, err := json.Marshal(map[string]any{
		"compilerOptions": map[string]any{
			"noLib":     true,
			"noResolve": true,
			"noEmit":    true,
			"types":     []string{},
			"target":    "ES2022",
			"module":    "ESNext",
		},
		"files": []string{name},
	})
	if err != nil {
		return nil, nil, err
	}
	files := map[string]string{name: text, dir + "/tsconfig.json": string(config)}
	fs := tsbundled.WrapFS(declarationFS{FS: osvfs.FS(), files: files})
	host := tscompiler.NewCachedFSCompilerHost(dir, fs, tsbundled.LibPath(), nil, nil)
	parsed, configDiags := tsoptions.GetParsedCommandLineOfConfigFile(dir+"/tsconfig.json", nil, nil, host, nil)
	if len(configDiags) > 0 || parsed == nil {
		return nil, nil, fmt.Errorf("parse %s: the parser's compiler options do not parse", fileName)
	}
	program := tscompiler.NewProgram(tscompiler.ProgramOptions{Host: host, Config: parsed, SingleThreaded: tscore.TSTrue})
	file := program.GetSourceFile(name)
	if file == nil {
		return nil, nil, fmt.Errorf("parse %s: the compiler read no file", fileName)
	}
	return file, program.GetSyntacticDiagnostics(context.Background(), file), nil
}

// close releases the type checker.
func (p *corsaProgram) close() {
	if p.release != nil {
		p.release()
		p.release = nil
	}
}

// sourceFiles returns all source files in the program.
func (p *corsaProgram) sourceFiles() []*astSourceFile {
	return p.program.SourceFiles()
}

// semanticDiagnostics returns the semantic diagnostics for one file. A type
// error in a schema file IS a schema error.
func (p *corsaProgram) semanticDiagnostics(file *astSourceFile) []*astDiagnostic {
	return p.program.GetSemanticDiagnostics(context.Background(), file)
}

// syntacticDiagnostics returns the parse diagnostics for one file.
func (p *corsaProgram) syntacticDiagnostics(file *astSourceFile) []*astDiagnostic {
	return p.program.GetSyntacticDiagnostics(context.Background(), file)
}

// sourceFile returns the program's file at fileName, or nil.
func (p *corsaProgram) sourceFile(fileName string) *astSourceFile {
	return p.program.GetSourceFile(fileName)
}
