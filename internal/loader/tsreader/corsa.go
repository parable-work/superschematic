// Package tsreader implements the psgen v2 TypeScript frontend: it walks
// .schema.ts files with the TypeScript 7.0 Go compiler (Project Corsa) and
// produces the v2 Schema IR.
//
// This file is the thin pinned wrapper over the compiler: it is the ONLY file
// in the package that imports the fork's shim packages
// (github.com/microsoft/typescript-go/shim/...). Everything else in tsreader
// programs against the local aliases and bindings declared here, so compiler
// API churn localizes to this file. The fork version is pinned in go.mod and
// upgraded in dedicated PRs.
package tsreader

import (
	"context"
	"fmt"
	"path/filepath"

	tsast "github.com/microsoft/typescript-go/shim/ast"
	tsbundled "github.com/microsoft/typescript-go/shim/bundled"
	tschecker "github.com/microsoft/typescript-go/shim/checker"
	tscompiler "github.com/microsoft/typescript-go/shim/compiler"
	tscore "github.com/microsoft/typescript-go/shim/core"
	tsoptions "github.com/microsoft/typescript-go/shim/tsoptions"
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

// AST node kinds.
const (
	kindNumericLiteral           = tsast.KindNumericLiteral
	kindStringLiteral            = tsast.KindStringLiteral
	kindIdentifier               = tsast.KindIdentifier
	kindDecorator                = tsast.KindDecorator
	kindPropertyDeclaration      = tsast.KindPropertyDeclaration
	kindMethodDeclaration        = tsast.KindMethodDeclaration
	kindTypeReference            = tsast.KindTypeReference
	kindArrayType                = tsast.KindArrayType
	kindUnionType                = tsast.KindUnionType
	kindObjectLiteralExpression  = tsast.KindObjectLiteralExpression
	kindPropertyAccessExpression = tsast.KindPropertyAccessExpression
	kindCallExpression           = tsast.KindCallExpression
	kindClassDeclaration         = tsast.KindClassDeclaration
	kindInterfaceDeclaration     = tsast.KindInterfaceDeclaration
	kindTypeAliasDeclaration     = tsast.KindTypeAliasDeclaration
	kindEnumDeclaration          = tsast.KindEnumDeclaration
	kindHeritageClause           = tsast.KindHeritageClause
	kindSourceFile               = tsast.KindSourceFile
	// The latest fork shim keeps only the first-pass walker constants public.
	// These values match internal/ast/kind_stringer_generated.go at the pinned
	// replacement version.
	kindExtendsKeyword              = tsast.Kind(95)
	kindImplementsKeyword           = tsast.Kind(118)
	kindImportDeclaration           = tsast.Kind(273)
	kindNamedImports                = tsast.Kind(276)
	kindNamespaceImport             = tsast.Kind(275)
	kindImportSpecifier             = tsast.Kind(277)
	kindArrayLiteralExpression      = tsast.Kind(210)
	kindTrueKeyword                 = tsast.Kind(111)
	kindFalseKeyword                = tsast.Kind(96)
	kindNullKeyword                 = tsast.Kind(105)
	kindPrefixUnaryExpression       = tsast.Kind(225)
	kindMinusToken                  = tsast.Kind(40)
	kindTypeLiteral                 = tsast.Kind(188)
	kindPropertySignature           = tsast.Kind(172)
	kindLiteralType                 = tsast.Kind(202)
	kindParameter                   = tsast.Kind(170)
	kindEnumMember                  = tsast.Kind(306)
	kindExpressionWithTypeArguments = tsast.Kind(234)
	kindPropertyAssignment          = tsast.Kind(303)
	kindExportAssignment            = tsast.Kind(278)
	kindTypeOperator                = tsast.Kind(199)
	kindParenthesizedType           = tsast.Kind(197)
	kindQualifiedName               = tsast.Kind(167)
	kindComputedPropertyName        = tsast.Kind(168)
	kindStringKeyword               = tsast.Kind(154)
	kindNumberKeyword               = tsast.Kind(150)
	kindBooleanKeyword              = tsast.Kind(135)
	kindObjectKeyword               = tsast.Kind(151)
	kindAnyKeyword                  = tsast.Kind(132)
	kindUnknownKeyword              = tsast.Kind(159)
	kindVariableStatement           = tsast.Kind(244)
	kindVariableDeclaration         = tsast.Kind(261)
	kindQuestionToken               = tsast.Kind(57)
	kindAsExpression                = tsast.Kind(235)
)

// Modifier flags.
const (
	modifierFlagsExport   = 1 << 5
	modifierFlagsAbstract = 1 << 6
)

// Symbol flags.
const (
	symbolFlagsAlias      = 1 << 21
	symbolFlagsClass      = 1 << 5
	symbolFlagsInterface  = 1 << 6
	symbolFlagsEnum       = 1<<7 | 1<<8
	symbolFlagsTypeAlias  = 1 << 19
	symbolFlagsEnumMember = 1 << 3
)

// Type flags.
const (
	typeFlagsString         = 1 << 5
	typeFlagsNumber         = 1 << 6
	typeFlagsBoolean        = 1 << 8
	typeFlagsStringLiteral  = 1 << 10
	typeFlagsNumberLiteral  = 1 << 11
	typeFlagsBooleanLiteral = 1 << 13
	typeFlagsEnumLiteral    = 1 << 15
	typeFlagsUnion          = 1 << 27
	typeFlagsIntersection   = 1 << 28
	typeFlagsStringLike     = 1<<5 | 1<<10 | 1<<22 | 1<<23
	typeFlagsNumberLike     = 1<<6 | 1<<11 | 1<<16
	typeFlagsBooleanLike    = 1<<8 | 1<<13
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

func hasSyntacticModifier(node *astNode, flags uint32) bool {
	return uint32(node.ModifierFlags())&flags != 0
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
