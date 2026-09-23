package loader_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tsast "github.com/microsoft/typescript-go/shim/ast"

	"github.com/parable-work/superschematic/loader"
)

// declaration finds the top-level interface or type alias called name.
func declaration(t *testing.T, program *loader.DeclarationProgram, file, name string) *tsast.Node {
	t.Helper()
	source := program.SourceFile(file)
	if source == nil {
		t.Fatalf("%s is not in the program", file)
	}
	for _, node := range source.Statements.Nodes {
		if (node.Kind == tsast.KindInterfaceDeclaration || node.Kind == tsast.KindTypeAliasDeclaration) && node.Name().Text() == name {
			return node
		}
	}
	t.Fatalf("%s declares no %s", file, name)
	return nil
}

func newProgram(t *testing.T, in loader.DeclarationInput) *loader.DeclarationProgram {
	t.Helper()
	program, err := loader.NewDeclarationProgram(in)
	if err != nil {
		t.Fatalf("NewDeclarationProgram: %v", err)
	}
	t.Cleanup(program.Close)
	return program
}

// The checker resolves a relative import and a bare import under
// node_modules/ in the supplied files, and hands out checked types.
func TestDeclarationProgramChecksTypesAcrossFiles(t *testing.T) {
	program := newProgram(t, loader.DeclarationInput{
		Files: map[string]string{
			"price.d.ts": `import type { Money } from "money";
import type { Currency } from "./currency";
export interface Price {
  amount: Money;
  currency: Currency;
  note?: string;
}
`,
			"currency.d.ts":                   `export type Currency = "EUR" | "USD";` + "\n",
			"node_modules/money/package.json": `{"name": "money", "types": "index.d.ts"}`,
			"node_modules/money/index.d.ts":   `export type Money = number & { readonly __brand: "Money" };` + "\n",
			"unused.d.ts":                     `export type Unused = string;` + "\n",
		},
		Roots: []string{"price.d.ts"},
	})
	if diags := program.Diagnostics(); len(diags) > 0 {
		t.Fatalf("Diagnostics = %v", diags)
	}
	if program.SourceFile("node_modules/money/index.d.ts") == nil || program.SourceFile("currency.d.ts") == nil {
		t.Fatal("imported files are not in the program")
	}
	if program.SourceFile("unused.d.ts") != nil {
		t.Fatal("a file no root imports joined the program")
	}

	checker := program.Checker()
	node := declaration(t, program, "price.d.ts", "Price")
	price := checker.GetDeclaredTypeOfSymbol(checker.GetSymbolAtLocation(node.Name()))
	got := map[string]string{}
	for _, property := range checker.GetPropertiesOfType(price) {
		got[property.Name] = checker.TypeToString(checker.GetTypeOfSymbol(property))
	}
	want := map[string]string{"amount": "Money", "currency": "Currency", "note": "string | undefined"}
	if len(got) != len(want) {
		t.Fatalf("Price properties = %v, want %v", got, want)
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("Price.%s = %q, want %q", name, got[name], typ)
		}
	}

	located := program.ErrorAt(node, "Price is %s", "rejected")
	if located.File != "price.d.ts" || located.Line != 3 || located.Col != 1 || located.Error() != "price.d.ts:3:1: Price is rejected" {
		t.Errorf("ErrorAt = %+v (%q), want price.d.ts:3:1", located, located.Error())
	}
}

// Diagnostics are located in the supplied file names, checked strictly, and
// limited to the named files when names are given.
func TestDeclarationProgramDiagnostics(t *testing.T) {
	program := newProgram(t, loader.DeclarationInput{
		Files: map[string]string{
			"entry.d.ts":  `import type { Vendor } from "./vendor";` + "\n" + `export type Entry = { vendor: Vendor; callback: (value) => void };` + "\n",
			"vendor.d.ts": `export type Vendor = Missing;` + "\n",
		},
		Roots: []string{"entry.d.ts"},
	})

	all := program.Diagnostics()
	if len(all) != 2 {
		t.Fatalf("Diagnostics() = %v, want the implicit any in entry.d.ts and the missing name in vendor.d.ts", all)
	}
	var files []string
	for _, diag := range all {
		files = append(files, diag.File)
	}
	if strings.Join(files, ",") != "entry.d.ts,vendor.d.ts" && strings.Join(files, ",") != "vendor.d.ts,entry.d.ts" {
		t.Fatalf("diagnostic files = %v", files)
	}
	for _, diag := range all {
		switch diag.File {
		case "entry.d.ts":
			if diag.Line != 2 || !strings.Contains(diag.Msg, "implicitly has an 'any' type") {
				t.Errorf("entry.d.ts diagnostic = %+v, want strict mode's implicit any on line 2", diag)
			}
		case "vendor.d.ts":
			if diag.Line != 1 || diag.Col != 22 || !strings.Contains(diag.Msg, "Missing") {
				t.Errorf("vendor.d.ts diagnostic = %+v, want Missing at 1:22", diag)
			}
		}
	}

	trusted := program.Diagnostics("entry.d.ts", "not-in-program.d.ts")
	if len(trusted) != 1 || trusted[0].File != "entry.d.ts" {
		t.Fatalf("Diagnostics(entry.d.ts) = %v, want only entry.d.ts's", trusted)
	}
	var list loader.SchemaErrorList = trusted
	var err error = list
	var target loader.SchemaErrorList
	if !errors.As(err, &target) || !strings.HasPrefix(err.Error(), "entry.d.ts:2:") {
		t.Errorf("the list as an error = %q", err)
	}
}

// Nothing outside the supplied files is read, even a file that exists on
// disk at the path an import names.
func TestDeclarationProgramReadsNoFilesFromDisk(t *testing.T) {
	onDisk := filepath.Join(t.TempDir(), "real.d.ts")
	if err := os.WriteFile(onDisk, []byte("export type Real = string;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	importPath := filepath.ToSlash(strings.TrimSuffix(onDisk, ".d.ts"))
	program := newProgram(t, loader.DeclarationInput{
		Files: map[string]string{
			"entry.d.ts": `import type { Real } from "` + importPath + `";` + "\n" + `export type Entry = Real;` + "\n",
		},
		Roots: []string{"entry.d.ts"},
	})
	diags := program.Diagnostics()
	if len(diags) == 0 || !strings.Contains(diags.Error(), "Cannot find module") {
		t.Fatalf("Diagnostics = %v, want the on-disk module unresolved", diags)
	}
}

func TestDeclarationProgramLib(t *testing.T) {
	files := map[string]string{"dom.d.ts": "export type Element = HTMLElement;\n"}
	withoutDOM := newProgram(t, loader.DeclarationInput{Files: files, Roots: []string{"dom.d.ts"}})
	if diags := withoutDOM.Diagnostics(); len(diags) != 1 || !strings.Contains(diags[0].Msg, "HTMLElement") {
		t.Fatalf("default lib Diagnostics = %v, want HTMLElement unknown", diags)
	}
	withDOM := newProgram(t, loader.DeclarationInput{Files: files, Roots: []string{"dom.d.ts"}, Lib: []string{"ES2023", "DOM"}})
	if diags := withDOM.Diagnostics(); len(diags) != 0 {
		t.Fatalf("DOM lib Diagnostics = %v, want none", diags)
	}
	_, err := loader.NewDeclarationProgram(loader.DeclarationInput{Files: files, Roots: []string{"dom.d.ts"}, Lib: []string{"NoSuchLib"}})
	var diags loader.SchemaErrorList
	if err == nil || !strings.Contains(err.Error(), "lib NoSuchLib") || !strings.Contains(err.Error(), "'--lib'") || !errors.As(err, &diags) {
		t.Fatalf("unknown lib: err = %v, want the lib and the compiler's diagnostic", err)
	}
}

func TestNewDeclarationProgramRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   loader.DeclarationInput
		want string
	}{
		{"no roots", loader.DeclarationInput{Files: map[string]string{"a.d.ts": ""}}, "no roots"},
		{"root not a file", loader.DeclarationInput{Files: map[string]string{"a.d.ts": ""}, Roots: []string{"b.d.ts"}}, `root "b.d.ts"`},
		{"absolute path", loader.DeclarationInput{Files: map[string]string{"/a.d.ts": ""}, Roots: []string{"/a.d.ts"}}, `"/a.d.ts"`},
		{"parent path", loader.DeclarationInput{Files: map[string]string{"../a.d.ts": ""}, Roots: []string{"../a.d.ts"}}, `"../a.d.ts"`},
		{"unclean path", loader.DeclarationInput{Files: map[string]string{"x/../a.d.ts": ""}, Roots: []string{"x/../a.d.ts"}}, `"x/../a.d.ts"`},
		{"empty path", loader.DeclarationInput{Files: map[string]string{"": ""}, Roots: []string{""}}, `""`},
		{"reserved config", loader.DeclarationInput{Files: map[string]string{"a.d.ts": "", "tsconfig.json": "{}"}, Roots: []string{"a.d.ts"}}, "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loader.NewDeclarationProgram(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %s", err, tc.want)
			}
		})
	}
}
