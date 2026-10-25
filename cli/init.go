package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// `init` writes a schemas root (D71): the naming file, the base tsconfig,
// the output root's ignore rules and an empty services directory, so no
// one writes them by hand. `new service` then adds the services.

// defaultSchemasRoot is the schemas root init writes, and new service
// writes into, without one named: ./schemas, which holds the services
// root the stack commands look for.
const defaultSchemasRoot = "schemas"

// initFlags holds `init`'s flag values.
type initFlags struct {
	goModuleRoot   string
	npmScope       string
	superschematic string
}

func newInitCmd(a *app) *cobra.Command {
	flags := &initFlags{}
	cmd := &cobra.Command{
		Use:   "init [<schemas-root>]",
		Short: "Write a schemas root: the naming file, the base tsconfig, the ignore rules and services/",
		Long: `init writes a schemas root, the directory every other command reads
(docs/DECISIONS.md, D71):

  superschematic.toml  go_module_root and npm_scope, and a [paths] table
                       that points the generated packages' path
                       dependencies at a superschematic checkout
  tsconfig.base.json   strict, experimentalDecorators, and paths that map
                       @superschematic/* and superscalar to the checkout's
                       sources and <npm_scope>/* to the services
  .gitignore           the output root's contents, but for the lockfiles
                       of its Bun and Cargo workspaces (D51)
  services/            empty: superschematic new service adds each service

Nothing of superschematic is published yet, so the schema files compile
against a checkout: --superschematic names it, and without the flag init
takes the checkout that holds the schemas root, if one does. The paths
are relative to the files that hold them.

At a terminal it asks for each value no flag gives. Without one, the
module root is example.com/<project> and the scope @<project>, where the
project is the directory that holds the schemas root. init writes no file
that exists: it refuses, naming each, and writes nothing.

Without a directory, init writes ./schemas.

Examples:
  superschematic init --superschematic ~/src/superschematic
  superschematic init ./schemas --go-module-root example.com/acme --npm-scope @acme`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := defaultSchemasRoot
			if len(args) == 1 {
				root = args[0]
			}
			return runInit(cmd, flags, root)
		},
	}
	cmd.Flags().StringVar(&flags.goModuleRoot, "go-module-root", "", "the prefix of every generated Go module path, such as example.com/acme (default example.com/<project>)")
	cmd.Flags().StringVar(&flags.npmScope, "npm-scope", "", "the npm scope of the generated packages and of the services' own, such as @acme (default @<project>)")
	cmd.Flags().StringVar(&flags.superschematic, "superschematic", "", "the superschematic checkout the schema files and the generated packages build against (default: the checkout that holds the schemas root)")
	return cmd
}

// The files init writes, relative to the schemas root.
const (
	initIgnoreFile   = ".gitignore"
	initTSConfigBase = "tsconfig.base.json"
	initServicesDir  = "services"
)

// initValues are what init writes, from the flags and the answers.
type initValues struct {
	goModuleRoot string
	npmScope     string
	// checkout is the superschematic checkout, absolute.
	checkout string
}

func runInit(cmd *cobra.Command, flags *initFlags, schemasRoot string) error {
	schemasRoot, err := filepath.Abs(schemasRoot)
	if err != nil {
		return err
	}
	files := []string{naming.FileName, initTSConfigBase, initIgnoreFile}
	var exist []string
	for _, name := range files {
		path := filepath.Join(schemasRoot, name)
		if _, err := os.Lstat(path); err == nil {
			exist = append(exist, displayPath(path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(exist) > 0 {
		return fmt.Errorf("init writes no file that exists, and %s %s; move %s aside or write the schemas root elsewhere",
			joinNames(exist), pluralVerb(len(exist), "exists", "exist"), pluralVerb(len(exist), "it", "them"))
	}
	if info, err := os.Stat(schemasRoot); err == nil && !info.IsDir() {
		return fmt.Errorf("%s is a file, not a directory", displayPath(schemasRoot))
	}

	values, err := initAnswers(newLinePrompter(cmd), flags, schemasRoot)
	if err != nil {
		return err
	}
	repoRoot := filepath.Dir(schemasRoot)
	toml, err := initNamingFile(values, repoRoot)
	if err != nil {
		return err
	}
	base, err := initTSConfig(values, schemasRoot)
	if err != nil {
		return err
	}
	contents := map[string][]byte{
		naming.FileName:  toml,
		initTSConfigBase: base,
		initIgnoreFile:   []byte(initIgnoreRules),
	}
	if err := os.MkdirAll(filepath.Join(schemasRoot, initServicesDir), 0o755); err != nil {
		return err
	}
	for _, name := range files {
		// O_EXCL: a file that appeared since the check is not replaced.
		if err := writeNewFile(filepath.Join(schemasRoot, name), contents[name]); err != nil {
			return err
		}
	}

	out := cmd.OutOrStdout()
	var b strings.Builder
	fmt.Fprintf(&b, "Wrote the schemas root %s:\n", displayPath(schemasRoot))
	for _, name := range append(files, initServicesDir+"/") {
		fmt.Fprintf(&b, "  %s\n", name)
	}
	fmt.Fprintf(&b, "\nGo modules under %s, npm packages under %s; the schema files compile against %s.\n",
		values.goModuleRoot, values.npmScope, displayPath(values.checkout))
	rootFlag := ""
	if filepath.Clean(schemasRoot) != filepath.Clean(absOrEmpty(defaultSchemasRoot)) && filepath.Clean(schemasRoot) != filepath.Clean(absOrEmpty(".")) {
		rootFlag = " --root " + displayPath(schemasRoot)
	}
	b.WriteString("\nNext, add a service:\n")
	fmt.Fprintf(&b, "  superschematic new service <name> --kind DB|API|General|Bucket|Site%s\n", rootFlag)
	_, _ = io.WriteString(out, b.String())
	return nil
}

// absOrEmpty is path made absolute, or "" when it cannot be.
func absOrEmpty(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return abs
}

// pluralVerb picks one or many by n.
func pluralVerb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// writeNewFile writes a file that must not exist.
func writeNewFile(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s exists; it was not replaced", displayPath(path))
		}
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// --- the values ---

var (
	npmScopePattern     = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]*$`)
	goModuleRootPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]*(/[A-Za-z0-9._~-]+)*$`)
	projectWordPattern  = regexp.MustCompile(`[^a-z0-9]+`)
)

func checkNpmScope(scope string) error {
	if !npmScopePattern.MatchString(scope) {
		return fmt.Errorf("npm_scope %q: want @ then lower-case letters, digits, dots, hyphens or underscores, such as @acme", scope)
	}
	return nil
}

func checkGoModuleRoot(root string) error {
	if !goModuleRootPattern.MatchString(root) {
		return fmt.Errorf("go_module_root %q: want a module path with no trailing slash, such as example.com/acme", root)
	}
	return nil
}

// checkCheckout refuses a directory that is no superschematic checkout:
// the schema files import the authoring packages and superscalar from it.
func checkCheckout(dir string) error {
	for _, rel := range []string{"packages/schema-config/src/index.ts", initSuperscalarTypeScript} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			hint := ""
			if rel == initSuperscalarTypeScript {
				hint = "; run make setup in it, which checks superscalar out under third_party, or without Rust scripts/superscalar-dep.sh --prebuilt, which stages it prebuilt"
			}
			return fmt.Errorf("%s is no superschematic checkout ready to build against: it has no %s%s", displayPath(dir), rel, hint)
		}
	}
	return nil
}

// initProject is the name the defaults derive from: the directory that
// holds the schemas root, in lower case with each run of other characters
// a hyphen, or "" when nothing is left.
func initProject(schemasRoot string) string {
	word := projectWordPattern.ReplaceAllString(strings.ToLower(filepath.Base(filepath.Dir(schemasRoot))), "-")
	return strings.Trim(word, "-")
}

// enclosingCheckout returns the superschematic checkout at or above dir,
// or "" when there is none.
func enclosingCheckout(dir string) string {
	for {
		if checkCheckout(dir) == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// initAnswers takes each value from its flag, asks for the rest at a
// terminal, and otherwise takes the defaults.
func initAnswers(p linePrompter, flags *initFlags, schemasRoot string) (*initValues, error) {
	ask := &asker{p: p}
	defaults := naming.Default()
	defModule, defScope := defaults.GoModuleRoot, defaults.NpmScope
	if project := initProject(schemasRoot); project != "" {
		defModule, defScope = "example.com/"+project, "@"+project
	}
	value := func(flag, question, def string, check func(string) error) (string, error) {
		if flag != "" {
			return flag, check(flag)
		}
		if p == nil {
			return def, check(def)
		}
		return ask.text(question, def, check)
	}
	values := &initValues{}
	var err error
	if values.goModuleRoot, err = value(flags.goModuleRoot, "Go module root of the generated packages", defModule, checkGoModuleRoot); err != nil {
		return nil, err
	}
	if values.npmScope, err = value(flags.npmScope, "npm scope of the generated packages", defScope, checkNpmScope); err != nil {
		return nil, err
	}
	checkAbs := func(dir string) error {
		if dir == "" {
			return errors.New("init needs a superschematic checkout to build against while nothing is published; name one with --superschematic")
		}
		abs, err := filepath.Abs(expandHome(dir))
		if err != nil {
			return err
		}
		return checkCheckout(abs)
	}
	checkout, err := value(flags.superschematic, "superschematic checkout", enclosingCheckout(filepath.Dir(schemasRoot)), checkAbs)
	if err != nil {
		return nil, err
	}
	if values.checkout, err = filepath.Abs(expandHome(checkout)); err != nil {
		return nil, err
	}
	return values, nil
}

// expandHome expands a leading ~/ to the home directory, as a shell would
// for an answer typed at the prompt.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// --- the files ---

// initSuperscalarTypeScript is superscalar's TypeScript entry point in a
// checkout, where make setup checks superscalar out.
const initSuperscalarTypeScript = "third_party/superscalar/bindings/typescript/src/index.ts"

// initIgnoreRules is the schemas root's .gitignore: the output root's
// contents but its workspaces' lockfiles (D51, D57), with the directory
// taken back first in case a rule above ignores it.
const initIgnoreRules = `# The output root is generated, but for the lockfiles of its Bun and Cargo
# workspaces, which pin the dependencies of the generated packages (D51).
# A broader rule may ignore the directory itself, which would hide every
# file under it, so it is taken back and its contents ignored instead.
!/dist/
/dist/*
!/dist/bun.lock
!/dist/Cargo.lock
`

// initPathKeys are the [paths] keys init sets, in the order it writes
// them, with the checkout directory each names: those acme-shop's naming
// file sets. A key whose directory the checkout lacks is left out.
var initPathKeys = []struct{ key, dir string }{
	{"scalar_go", "third_party/superscalar/go"},
	{"scalar_typescript", "third_party/superscalar/bindings/typescript"},
	{"scalar_rust", "third_party/superscalar/crates/core"},
	{"schema_ir", "ir"},
	{"schema_runtime_go", "runtime/schema/go"},
	{"schema_runtime_rust", "runtime/schema/rust"},
	{"http_runtime_go", "runtime/http/go"},
	{"http_runtime_rust", "runtime/http/rust"},
	{"http_runtime_typescript", "runtime/http/typescript"},
	{"versiongraph_go", "runtime/versiongraph/go"},
}

// initNamingFile is the naming file: the two coordinates and the [paths]
// table, relative to repoRoot, the parent of the schemas root, which
// [paths] resolves against. build_context, the directory a stack's server
// images build from, is the checkout when it holds the project, since the
// images need the runtimes; otherwise the file says what to do.
func initNamingFile(v *initValues, repoRoot string) ([]byte, error) {
	var b strings.Builder
	b.WriteString(`# The naming file: every command reads it from the schemas root (this
# directory). A key left out keeps the core default; the docs site's naming
# reference lists every key.

# Coordinates of the generated packages: <go_module_root>/types/go/<service>,
# <npm_scope>/<service>-types, and the services' own packages,
# <npm_scope>/<service>.
`)
	fmt.Fprintf(&b, "go_module_root = %q\n", v.goModuleRoot)
	fmt.Fprintf(&b, "npm_scope = %q\n", v.npmScope)
	b.WriteString(`
# Where the runtime modules live, relative to the parent of the schemas
# root. Generated manifests point their path dependencies (go.mod replace,
# npm file:, Cargo path) here, so the project compiles against a
# superschematic checkout before anything is published. Once the modules
# are published, remove this table.
[paths]
`)
	for _, p := range initPathKeys {
		dir := filepath.Join(v.checkout, filepath.FromSlash(p.dir))
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		rel, err := relSlash(repoRoot, dir)
		if err != nil {
			return nil, fmt.Errorf("[paths] %s: %w", p.key, err)
		}
		fmt.Fprintf(&b, "%s = %q\n", p.key, rel)
	}
	if within(repoRoot, v.checkout) {
		rel, err := relSlash(repoRoot, v.checkout)
		if err != nil {
			return nil, err
		}
		b.WriteString("# A stack's server images build from the checkout, which holds the runtime\n# modules above as well as this project.\n")
		fmt.Fprintf(&b, "build_context = %q\n", rel)
	} else {
		b.WriteString("# A stack's server images build from the parent of the schemas root, which\n# does not hold the runtime modules above: before building a stack's images,\n# set build_context to a directory that holds both.\n")
	}
	return []byte(b.String()), nil
}

// initTSConfig is the base tsconfig the services' extend: acme-shop's
// options, with paths from the schemas root to the checkout's authoring
// packages and superscalar, and to the services under the npm scope.
func initTSConfig(v *initValues, schemasRoot string) ([]byte, error) {
	rel, err := relSlash(schemasRoot, v.checkout)
	if err != nil {
		return nil, fmt.Errorf("tsconfig.base.json's paths: %w", err)
	}
	return fmt.Appendf(nil, `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "lib": ["ES2022"],
    "strict": true,
    "strictPropertyInitialization": false,
    "experimentalDecorators": true,
    "emitDecoratorMetadata": false,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "noEmit": true,
    "baseUrl": ".",
    "paths": {
      "@superschematic/*": [%q],
      "superscalar": [%q],
      %q: ["./services/*/src/index.ts"]
    }
  }
}
`, rel+"/packages/*/src/index.ts", rel+"/"+initSuperscalarTypeScript, v.npmScope+"/*"), nil
}

// relSlash is target relative to base, with forward slashes.
func relSlash(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// within reports whether dir lies at or under root.
func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
