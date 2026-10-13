package stackinit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// The files of a stack service, relative to its directory.
const (
	ConfigFile  = "schema.config.ts"
	SchemaFile  = "src/stack.schema.ts"
	TSConfig    = "tsconfig.json"
	PackageJSON = "package.json"
)

// TargetTypesFile is the file that types a target's values and settings
// for tsc, relative to the stack service's directory: src/gcp-target.ts.
func TargetTypesFile(target string) string {
	return "src/" + target + "-target.ts"
}

// Sibling is what init copies from a service of the tree, so the stack's
// files resolve imports as its siblings' do: the sibling's tsconfig.json
// and whether it has a package.json.
type Sibling struct {
	// TSConfig is the sibling's tsconfig.json.
	TSConfig []byte

	// HasPackageJSON is whether the sibling names its package in a
	// package.json, which the stack then does too.
	HasPackageJSON bool
}

// File is one file init writes.
type File struct {
	// Path is relative to the stack service's directory, with forward
	// slashes.
	Path string

	Content []byte
}

// Render returns the files of the stack service p describes, in the order
// init writes them: the package.json when the sibling has one, the
// tsconfig.json, the config, the schema, and the types of each target
// other than the core's that an environment names. The names are the
// active naming's, which the schema's imports and the package's name use.
func Render(p *Plan, sibling Sibling, names naming.Naming, reg *registry.Registry) ([]File, error) {
	var files []File
	if sibling.HasPackageJSON {
		files = append(files, File{Path: PackageJSON, Content: packageJSON(names.NpmServicePackage(p.Name))})
	}
	files = append(files, File{Path: TSConfig, Content: tsconfig(sibling.TSConfig)})
	files = append(files, File{Path: ConfigFile, Content: Config(p, names)})
	schema, err := Schema(p, reg)
	if err != nil {
		return nil, err
	}
	files = append(files, File{Path: SchemaFile, Content: schema})
	seen := map[string]bool{}
	for _, env := range p.Environments {
		if env.Target == "" || seen[env.Target] || coreTarget(env.Target) {
			continue
		}
		seen[env.Target] = true
		spec, ok := reg.Target(env.Target)
		if !ok {
			return nil, fmt.Errorf("environment %s names target %s, which this binary does not link", env.Name, env.Target)
		}
		types, err := TargetTypes(spec, reg, names)
		if err != nil {
			return nil, err
		}
		files = append(files, File{Path: TargetTypesFile(env.Target), Content: types})
	}
	return files, nil
}

// coreTarget reports whether @superschematic/stack types the target
// itself: the core's local target.
func coreTarget(target string) bool { return target == "local" }

// packageJSON is a service's package.json, which names its package as the
// tree's imports name it.
func packageJSON(name string) []byte {
	data, _ := json.MarshalIndent(map[string]any{"name": name, "private": true}, "", "  ")
	return append(data, '\n')
}

// tsconfig is the stack's tsconfig.json: the sibling's base and the stack's
// own files, or the sibling's file as it is when it extends nothing that
// can be read, since every service sits at the same depth.
func tsconfig(sibling []byte) []byte {
	var parsed struct {
		Extends string `json:"extends"`
	}
	if err := json.Unmarshal(sibling, &parsed); err != nil || parsed.Extends == "" {
		return sibling
	}
	var b bytes.Buffer
	b.WriteString("{\n")
	fmt.Fprintf(&b, "  \"extends\": %s,\n", quote(parsed.Extends))
	b.WriteString("  \"include\": [\"schema.config.ts\", \"src/**/*.ts\"]\n")
	b.WriteString("}\n")
	return b.Bytes()
}

// Config is the stack service's schema.config.ts: its name, the Stack
// kind, and outputs.ci with each renderer p names.
func Config(p *Plan, names naming.Naming) []byte {
	outputs := "{}"
	if len(p.CI) > 0 {
		renderers := make([]string, len(p.CI))
		for i, name := range p.CI {
			renderers[i] = propertyName(name) + ": {}"
		}
		outputs = "{ ci: { " + strings.Join(renderers, ", ") + " } }"
	}
	return fmt.Appendf(nil, `import { defineConfig, SchemaKind } from %s;

export default defineConfig({
  name: %s,
  kind: SchemaKind.Stack,
  outputs: %s
});
`, quote(names.Specifier("@superschematic/schema-config")), quote(p.Name), outputs)
}

// Schema is the stack's schema file, src/stack.schema.ts, written by the
// TypeScript schema writer from the declarations p makes: the @stack class
// and an @environment class per environment, in their order.
func Schema(p *Plan, reg *registry.Registry) ([]byte, error) {
	class := ClassName(p.Name, append(p.Handles(), environmentNames(p)...))
	expose := make([]ir.DeployableRef, len(p.Expose))
	for i := range p.Expose {
		expose[i] = ir.DeployableRef{Service: &p.Expose[i]}
	}
	doc := &schemafile.Document{
		Kind: ir.SchemaKindStack,
		Types: map[string]*ir.TypeDef{
			class: {
				Name:    class,
				Role:    ir.RoleEmbeddedStruct,
				Comment: stackComment(p),
				Stack:   &ir.StackDecl{Deploy: p.Deploy, Expose: expose},
			},
		},
	}
	for i, env := range p.Environments {
		doc.Types[env.Name] = &ir.TypeDef{
			Name:    env.Name,
			Role:    ir.RoleEmbeddedStruct,
			Comment: env.Comment,
			Environment: &ir.EnvironmentDecl{
				Target: env.Target,
				Values: env.Values,
				Order:  i + 1,
			},
		}
	}
	var catalog registry.ScalarCatalog
	if reg != nil {
		catalog = reg.Scalars()
	}
	return writer.WriteWith(doc, writer.FormatTS, catalog)
}

// stackComment is the comment above the @stack class: what deploy and
// expose hold, and the rule that chose them.
func stackComment(p *Plan) string {
	lines := []string{
		"Written by superschematic stack init. It deploys every API and site of the",
		"tree, and the databases and buckets they reach come with them.",
	}
	if p.ExposeNamed {
		return strings.Join(append(lines, "It exposes the APIs it was told to; only other servers reach the rest."), "\n")
	}
	return strings.Join(append(lines,
		"It exposes each API an end user or a third party calls, one with an",
		"operation that is not @requireService, and each API a site calls; only",
		"other servers reach the rest.",
	), "\n")
}

func environmentNames(p *Plan) []string {
	names := make([]string, len(p.Environments))
	for i, env := range p.Environments {
		names[i] = env.Name
	}
	return names
}

// quote renders s as a JSON string, which TypeScript reads as the same
// string.
func quote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

// propertyName renders an object literal's key, quoted when it is not an
// identifier.
func propertyName(key string) string {
	if identifier.MatchString(key) {
		return key
	}
	return quote(key)
}
