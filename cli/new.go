package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/sentinel"
	"github.com/parable-work/superschematic/internal/stackinit"
	ir "github.com/parable-work/superschematic/ir"
)

// `new service` writes a service into a schemas root (D71): its config,
// with an outputs block that loads, its package files, and a starter
// schema file, then builds it once so its sentinel exists.

func newNewCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Write a new piece of a schemas root",
		Long: `The new commands write a piece of a schemas root that init wrote, so no one
writes its boilerplate by hand. Each refuses to replace a file that exists.`,
	}
	cmd.AddCommand(newNewServiceCmd(a))
	return cmd
}

// newServiceFlags holds `new service`'s flag values.
type newServiceFlags struct {
	kind       string
	root       string
	namingPath string
}

// newServiceKinds are the kinds new service writes, as --kind names them.
// A Stack service comes from stack init, which reads the tree it deploys.
var newServiceKinds = []ir.SchemaKind{ir.SchemaKindDB, ir.SchemaKindAPI, ir.SchemaKindGeneral, ir.SchemaKindBucket, ir.SchemaKindSite}

func newNewServiceCmd(a *app) *cobra.Command {
	flags := &newServiceFlags{}
	cmd := &cobra.Command{
		Use:   "service <name> --kind DB|API|General|Bucket|Site",
		Short: "Write a service: its config, package files and a starter schema, then build it once",
		Long: `service writes services/<name>/ under a schemas root (docs/DECISIONS.md, D71):

  schema.config.ts     defineConfig with the name, the kind and an outputs
                       block that loads
  package.json         names the package <npm_scope>/<name>, as the other
                       services import it
  tsconfig.json        extends ../../tsconfig.base.json
  src/index.ts         re-exports the starter schema's declarations and the
                       service's handle
  src/<name>.schema.ts a starter declaration to rename and grow: a table
                       for a DB, an object type for General, a GET route
                       for an API

A DB gets Go and TypeScript types, the Go types every DB needs. An API
gets Go and TypeScript types, a Go server and Go and TypeScript SDKs,
since a server or an SDK in a language needs the types in it. A General
schema gets Go and TypeScript types. A Bucket and a Site declare nothing
in schema files, so they get the config, the package files and
src/index.ts alone: an API lists a bucket in its buckets, and a site's
config lists the APIs its browser code calls.

Last, it builds the service once, as superschematic build does, into the
schemas root's dist/, which writes src/service.generated.ts, the handle
other services' configs import. The name is lower-case words joined by
hyphens, such as shop-db. A file that exists is never replaced: the
command refuses, naming each, and writes nothing.

Without --root, the schemas root is the working directory when it holds
superschematic.toml, else ./schemas.

Examples:
  superschematic new service shop-db --kind DB
  superschematic new service shop-api --kind API --root ./schemas`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNewService(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.kind, "kind", "", "the service's kind: DB, API, General, Bucket or Site")
	cmd.Flags().StringVar(&flags.root, "root", "", "the schemas root (default: the working directory when it holds superschematic.toml, else ./schemas)")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <schemas-root>/superschematic.toml)")
	_ = cmd.MarkFlagRequired("kind")
	return cmd
}

// newServiceKind reads --kind, in any case.
func newServiceKind(value string) (ir.SchemaKind, error) {
	for _, kind := range newServiceKinds {
		if strings.EqualFold(value, string(kind)) {
			return kind, nil
		}
	}
	names := make([]string, len(newServiceKinds))
	for i, kind := range newServiceKinds {
		names[i] = string(kind)
	}
	hint := ""
	if strings.EqualFold(value, string(ir.SchemaKindStack)) {
		hint = "; superschematic stack init writes a Stack service over the tree's APIs"
	}
	return "", fmt.Errorf("--kind %s: want one of %s%s", value, strings.Join(names, ", "), hint)
}

// newServiceRoot is the schemas root new service writes into: --root,
// else the working directory when it holds the naming file, else
// ./schemas.
func newServiceRoot(flag string) (string, error) {
	root := flag
	if root == "" {
		root = defaultSchemasRoot
		if _, err := os.Stat(naming.FileName); err == nil {
			root = "."
		}
	}
	return filepath.Abs(root)
}

func runNewService(cmd *cobra.Command, a *app, flags *newServiceFlags, name string) error {
	kind, err := newServiceKind(flags.kind)
	if err != nil {
		return err
	}
	if err := checkServiceName(name); err != nil {
		return err
	}
	schemasRoot, err := newServiceRoot(flags.root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(schemasRoot, initTSConfigBase)); err != nil {
		return fmt.Errorf("%s has no %s for the service's tsconfig.json to extend; superschematic init writes a schemas root, or --root names one", displayPath(schemasRoot), initTSConfigBase)
	}
	names, err := resolveNaming(flags.namingPath, schemasRoot)
	if err != nil {
		return err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	servicesRoot := filepath.Join(schemasRoot, initServicesDir)
	dir := filepath.Join(servicesRoot, name)
	if entries, err := os.ReadDir(servicesRoot); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == name {
				continue
			}
			cfg, err := buildplan.ReadConfig(filepath.Join(servicesRoot, entry.Name()), reg)
			if err == nil && cfg.Name == name {
				return fmt.Errorf("the service in %s is named %s already", displayPath(filepath.Join(servicesRoot, entry.Name())), name)
			}
		}
	}

	files := newServiceFiles(name, kind, names)
	var exist []string
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if _, err := os.Lstat(path); err == nil {
			exist = append(exist, displayPath(path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, other := range []string{"schema.config.json", "schema.config.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, other)); err == nil {
			exist = append(exist, displayPath(filepath.Join(dir, other)))
		}
	}
	if len(exist) > 0 {
		return fmt.Errorf("new service writes no file that exists, and %s %s; name the service another way or move %s aside",
			joinNames(exist), pluralVerb(len(exist), "exists", "exist"), pluralVerb(len(exist), "it", "them"))
	}
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeNewFile(path, f.Content); err != nil {
			return err
		}
	}

	out := cmd.OutOrStdout()
	var b strings.Builder
	fmt.Fprintf(&b, "Wrote the %s service %s in %s:\n", kind, name, displayPath(dir))
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", f.Path)
	}
	b.WriteString("\n")
	_, _ = io.WriteString(out, b.String())

	// The first build writes the service's handle, which the build itself
	// does not need: the starter imports no sibling.
	buildFlags := &buildFlags{namingPath: flags.namingPath}
	if err := runBuild(cmd, a, buildFlags, dir); err != nil {
		return fmt.Errorf("the service is written, but its first build failed: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(sentinel.GeneratedFile))); err != nil {
		return fmt.Errorf("the service is written and built, but the build wrote no %s", sentinel.GeneratedFile)
	}
	_, _ = io.WriteString(out, newServiceNext(name, kind, names, dir))
	return nil
}

// checkServiceName refuses a name that is not lower-case words joined by
// hyphens, the form every service directory, package and module takes.
func checkServiceName(name string) error {
	if err := stackinit.CheckName(name); err != nil {
		return fmt.Errorf("service name %q: want lower-case letters and digits in words joined by hyphens, such as shop-db", name)
	}
	return nil
}

// newServiceNext says what the service is for and what to do next.
func newServiceNext(name string, kind ir.SchemaKind, names naming.Naming, dir string) string {
	handle := tsutil.ToClassName(name)
	schema := displayPath(filepath.Join(dir, "src", name+".schema.ts"))
	var b strings.Builder
	b.WriteString("\nNext:\n")
	switch kind {
	case ir.SchemaKindDB:
		fmt.Fprintf(&b, "  Declare the tables in %s.\n", schema)
		fmt.Fprintf(&b, "  An API stores its users' sessions here with authDb: %s, imported from %q.\n", handle, names.NpmServicePackage(name))
	case ir.SchemaKindAPI:
		fmt.Fprintf(&b, "  Declare the operations in %s.\n", schema)
		b.WriteString("  Give its config authDb, the DB its sessions live in, and calls, the APIs it calls.\n")
	case ir.SchemaKindGeneral:
		fmt.Fprintf(&b, "  Declare the types in %s; other schemas import them from %q.\n", schema, names.NpmServicePackage(name))
	case ir.SchemaKindBucket:
		fmt.Fprintf(&b, "  An API stores objects in it by listing it in its config's buckets: [%s], imported from %q.\n", handle, names.NpmServicePackage(name))
	case ir.SchemaKindSite:
		b.WriteString("  List the APIs its browser code calls in its config's calls, and its build in site.\n")
	}
	fmt.Fprintf(&b, "  superschematic build-all %s\n", displayPath(filepath.Dir(dir)))
	return b.String()
}

// --- the files ---

// newServiceFiles returns the files of a new service, in the order the
// command writes them.
func newServiceFiles(name string, kind ir.SchemaKind, names naming.Naming) []stackinit.File {
	pkg, _ := json.MarshalIndent(map[string]any{"name": names.NpmServicePackage(name), "private": true}, "", "  ")
	files := []stackinit.File{
		{Path: "schema.config.ts", Content: newServiceConfig(name, kind, names)},
		{Path: "package.json", Content: append(pkg, '\n')},
		{Path: "tsconfig.json", Content: []byte(newServiceTSConfig)},
	}
	starter, exports := newServiceStarter(kind, names)
	var index bytes.Buffer
	if starter != nil {
		fmt.Fprintf(&index, "export { %s } from \"./%s.schema\";\n", strings.Join(exports, ", "), name)
	}
	index.WriteString("export * from \"./service.generated\";\n")
	files = append(files, stackinit.File{Path: "src/index.ts", Content: index.Bytes()})
	if starter != nil {
		files = append(files, stackinit.File{Path: "src/" + name + ".schema.ts", Content: starter})
	}
	return files
}

// newServiceTSConfig is every service's tsconfig.json: the schemas root's
// base, and the service's config and schema files.
const newServiceTSConfig = `{
  "extends": "../../tsconfig.base.json",
  "include": ["schema.config.ts", "src/**/*.ts"]
}
`

// newServiceConfig is the service's schema.config.ts. Its outputs load:
// a DB has Go types, and each server and SDK language has its types.
func newServiceConfig(name string, kind ir.SchemaKind, names naming.Naming) []byte {
	config := names.Specifier("@superschematic/schema-config")
	types := `    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    }`
	var comment, body string
	language := false
	switch kind {
	case ir.SchemaKindDB:
		language = true
		comment = "// A database: each plain class of its schema files is a table. Every DB\n// has Go types, which its migrations and ORM are generated with."
		body = "  outputs: {\n" + types + "\n  }"
	case ir.SchemaKindAPI:
		language = true
		comment = "// An API: a Go server, and SDKs its callers use. A server or an SDK in a\n// language needs the types in it. authDb names the DB its sessions live\n// in, and calls the APIs it calls."
		body = "  outputs: {\n" + types + `,
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }`
	case ir.SchemaKindGeneral:
		language = true
		comment = "// Plain types, which other schemas import and every language decodes and\n// validates the same way."
		body = "  outputs: {\n" + types + "\n  }"
	case ir.SchemaKindBucket:
		comment = "// A bucket of objects: its config is all it has. An API that stores objects\n// in it lists it in its buckets."
		body = "  outputs: {}"
	case ir.SchemaKindSite:
		comment = "// A static site: its config is all it has. calls lists the APIs its\n// browser code calls, and site its build."
	}
	imports := "defineConfig, SchemaKind"
	if language {
		imports += ", TargetLanguage"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "import { %s } from %q;\n\n%s\nexport default defineConfig({\n", imports, config, comment)
	fmt.Fprintf(&b, "  name: %q,\n  kind: SchemaKind.%s", name, kind)
	if body != "" {
		b.WriteString(",\n" + body)
	}
	b.WriteString("\n});\n")
	return []byte(b.String())
}

// newServiceStarter is the starter schema file of a kind and the names it
// exports, or nil for a kind that declares nothing in schema files.
func newServiceStarter(kind ir.SchemaKind, names naming.Naming) ([]byte, []string) {
	spec := func(pkg string) string { return fmt.Sprintf("%q", names.Specifier(pkg)) }
	switch kind {
	case ir.SchemaKindDB:
		return fmt.Appendf(nil, `import { Identity } from %s;
import { key } from %s;

// A table: every plain class of a DB schema is one, and @key marks its
// primary key. Rename it, give it its fields and add tables beside it.
export abstract class Item {
  @key
  id: Identity.UUID;

  name: string;
}
`, spec(names.ScalarNpmPackage), spec("@superschematic/db")), []string{"Item"}
	case ir.SchemaKindAPI:
		return fmt.Appendf(nil, `import { HttpMethod, rest } from %s;

// What the operation returns. An API that serves a DB's rows returns a
// view of them, a class with @source(Table) that lists the fields it shows.
export abstract class ItemView {
  id: string;
  name: string;
}

// An operation is a method of a class: its decorator gives the route, its
// parameters the request and its return type the response. The body is
// never run; the generated server calls the implementation.
export class ItemQueries {
  @rest(HttpMethod.GET, "items/{id}")
  getItem(id: string): ItemView {
    throw new Error("schema declaration only");
  }
}
`, spec("@superschematic/api")), []string{"ItemQueries", "ItemView"}
	case ir.SchemaKindGeneral:
		return []byte(`// An object type: an abstract class declares one, and an enum an enum.
// Fields take superscalar scalars, enums and other types. Rename it and
// add types beside it.
export abstract class Item {
  name: string;
}
`), []string{"Item"}
	}
	return nil, nil
}
