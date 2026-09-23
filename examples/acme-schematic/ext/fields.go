package ext

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tsast "github.com/microsoft/typescript-go/shim/ast"
	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/loader"
)

// Field is one property of a declared TypeScript type: its name and the
// type the compiler checked, as the compiler prints it.
type Field struct {
	Name string
	Type string
}

// DeclaredFields type-checks the declaration files in dir with the compiler
// the schema loader uses and returns the properties of the top-level
// interface or type alias typeName declares in root (a file in dir), in
// declaration order. Every .d.ts file in dir is available to root's
// relative imports; nothing else is read. Any diagnostic fails the call,
// located in the file that has it.
func DeclaredFields(dir, root, typeName string) ([]Field, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".d.ts") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = string(data)
	}
	program, err := loader.NewDeclarationProgram(loader.DeclarationInput{Files: files, Roots: []string{root}})
	if err != nil {
		return nil, err
	}
	defer program.Close()
	if diags := program.Diagnostics(); len(diags) > 0 {
		return nil, diags
	}
	checker := program.Checker()
	for _, node := range program.SourceFile(root).Statements.Nodes {
		if node.Kind != tsast.KindInterfaceDeclaration && node.Kind != tsast.KindTypeAliasDeclaration {
			continue
		}
		if node.Name().Text() != typeName {
			continue
		}
		declared := checker.GetDeclaredTypeOfSymbol(checker.GetSymbolAtLocation(node.Name()))
		var fields []Field
		for _, property := range checker.GetPropertiesOfType(declared) {
			fields = append(fields, Field{Name: property.Name, Type: checker.TypeToString(checker.GetTypeOfSymbol(property))})
		}
		if len(fields) == 0 {
			return nil, program.ErrorAt(node, "%s has no fields", typeName)
		}
		return fields, nil
	}
	return nil, fmt.Errorf("%s declares no interface or type alias %s", root, typeName)
}

// fieldsCommand prints a declared type's fields. It is the extension's use
// of loader.NewDeclarationProgram: a tool that needs types the schema
// frontend does not walk gets the same compiler, lib files and module
// resolution without importing the core's internals.
func fieldsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "fields <file.d.ts> <type>",
		Short: "Type-check a declaration file and print the fields of one of its types",
		Long: `fields type-checks <file.d.ts>, with the other .d.ts files in its directory
available to its imports, using the TypeScript compiler the schema loader
uses, and prints each property of the interface or type alias <type> as
name: type.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			fields, err := DeclaredFields(filepath.Dir(args[0]), filepath.Base(args[0]), args[1])
			if err != nil {
				return err
			}
			for _, field := range fields {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", field.Name, field.Type)
			}
			return nil
		},
	}
}
