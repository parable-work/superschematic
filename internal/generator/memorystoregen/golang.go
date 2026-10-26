package memorystoregen

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	ir "github.com/parable-work/superschematic/ir"
)

// The files of a store's Go definitions module.
const (
	// GoDefinitionsFile holds the definitions.
	GoDefinitionsFile = "definitions.go"

	// GoModFile is the module's go.mod.
	GoModFile = "go.mod"
)

// GoVersion is the go directive of the module, as every generated
// module's.
const GoVersion = "1.26.4"

// zeroVersion is the version a go.mod requires a module at that a replace
// points at a directory.
const zeroVersion = "v0.0.0-00010101000000-000000000000"

// GoPackageName is the Go package of a store's definitions: its name
// without its hyphens and underscores, `shopcache`.
func GoPackageName(store string) string {
	name := goutil.ToPackageName(store)
	if name == "" || !token.IsIdentifier(name) || token.IsKeyword(name) {
		name = "store" + name
	}
	return name
}

// GoModule is the module path of a store's Go definitions: the Go types
// module path of the service, since the definitions are its types output.
func GoModule(n naming.Naming, store string) string {
	return n.OrDefault().GoTypesModule(store)
}

// GenerateGo writes the store's Go definitions into in.Dir: a module whose
// package is named after the store (`shopcache`), with a value per
// declaration whose For takes the parts in order, each as its scalar's Go
// type, and returns the runtime's memorystore.Channel[T] or
// memorystore.Key (D75). A key whose life depends on the call has ForTTL in
// place of For, which refuses a TTL above the declaration's maximum. A
// channel's payload is the Go type its schema's types module already
// declares; a channel with none carries memorystore.Signal.
func GenerateGo(in Input) error {
	n := in.Context.Options.Naming.OrDefault()
	g, err := planGo(in, n)
	if err != nil {
		return err
	}
	if err := g.module(in, n); err != nil {
		return err
	}
	var src bytes.Buffer
	if err := goDefinitions.Execute(&src, g); err != nil {
		return fmt.Errorf("render %s: %w", GoDefinitionsFile, err)
	}
	formatted, err := format.Source(src.Bytes())
	if err != nil {
		return fmt.Errorf("format %s: %w\n%s", GoDefinitionsFile, err, src.Bytes())
	}
	if err := os.WriteFile(filepath.Join(in.Dir, GoDefinitionsFile), formatted, 0o644); err != nil {
		return err
	}
	var mod bytes.Buffer
	if err := goMod.Execute(&mod, g); err != nil {
		return fmt.Errorf("render %s: %w", GoModFile, err)
	}
	return os.WriteFile(filepath.Join(in.Dir, GoModFile), mod.Bytes(), 0o644)
}

// goPlan is what the templates read.
type goPlan struct {
	Store string
	// Example is the definition the package's doc uses, and DepsField
	// the Deps field of an API that lists the store.
	Example     string
	DepsField   string
	Package     string
	Module      string
	Runtime     string
	Scalars     string
	UsesScalars bool
	UsesTime    bool
	Imports     []goImport
	Defs        []goDef

	Requires []goRequire
	Indirect []goRequire
	Replaces []goReplace
}

type goImport struct {
	Alias   string
	Path    string
	Service string
}

type goRequire struct{ Path, Version string }

type goReplace struct{ Path, Target string }

// goDef is one declaration's definition.
type goDef struct {
	Decl *ir.MemoryStoreDeclaration
	// Var is the exported value, Type its unexported type.
	Var  string
	Type string
	// Message is a channel's Go message type.
	Message string
	Params  []goParam
	// TTL and MaxTTL are Go duration expressions.
	TTL    string
	MaxTTL string
	Doc    []string
}

// goParam is one part as For's parameter: its name, its Go type and the
// expression that writes its wire string.
type goParam struct {
	Part ir.MemoryStorePart
	Name string
	Type string
	Wire string
}

// Kind words.
func (d goDef) Channel() bool { return d.Decl.Kind == ir.MemoryStoreChannel }
func (d goDef) PerCall() bool { return d.Decl.MaxTTLSeconds > 0 }

// Signature is For's (or ForTTL's) parameter list.
func (d goDef) Signature() string {
	var params []string
	if d.PerCall() {
		params = append(params, "ttl time.Duration")
	}
	for _, p := range d.Params {
		params = append(params, p.Name+" "+p.Type)
	}
	return strings.Join(params, ", ")
}

// Wires are the expressions of the parts' wire strings, as Name's
// arguments.
func (d goDef) Wires() string {
	var wires []string
	for _, p := range d.Params {
		wires = append(wires, ", "+p.Wire)
	}
	return strings.Join(wires, "")
}

// PartList names the parts in a sentence: `productId, day and region`.
func (d goDef) PartList() string {
	names := make([]string, len(d.Params))
	for i, p := range d.Params {
		names[i] = p.Name
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// reservedParams are the names For's body uses beside its parameters.
var reservedParams = map[string]bool{"ttl": true, "name": true, "err": true, "memorystore": true, "scalars": true, "time": true}

func planGo(in Input, n naming.Naming) (*goPlan, error) {
	g := &goPlan{
		Store:   in.Store,
		Package: GoPackageName(in.Store),
		Module:  GoModule(n, in.Store),
		Runtime: n.HTTPRuntimeGoModule + "/memorystore",
		Scalars: n.ScalarGoModule,
	}
	aliases := map[string]string{}
	taken := map[string]bool{g.Package: true, "memorystore": true, "scalars": true, "time": true}
	importOfService := func(service string) string {
		if alias, ok := aliases[service]; ok {
			return alias
		}
		alias := goutil.ToPackageName(service) + "types"
		for i := 2; taken[alias]; i++ {
			alias = fmt.Sprintf("%stypes%d", goutil.ToPackageName(service), i)
		}
		taken[alias] = true
		aliases[service] = alias
		g.Imports = append(g.Imports, goImport{Alias: alias, Path: n.GoTypesModule(service), Service: service})
		return alias
	}
	vars := map[string]string{"Store": "the store's name"}
	for _, decl := range in.Declarations {
		d := goDef{
			Decl: decl,
			Var:  goutil.GoPublicIdentifier(decl.Symbol),
			Type: goutil.GoPrivateIdentifier(decl.Symbol) + "Def",
			Doc:  docLines(decl.Doc),
		}
		if prev, clash := vars[d.Var]; clash {
			return nil, fmt.Errorf("declaration %s would be %s.%s, the name of %s", decl.Name, g.Package, d.Var, prev)
		}
		vars[d.Var] = "declaration " + decl.Name
		switch {
		case decl.Kind == ir.MemoryStoreChannel && decl.Payload == nil:
			d.Message = "memorystore.Signal"
		case decl.Kind == ir.MemoryStoreChannel:
			p, ok := in.PayloadOf(decl)
			if !ok {
				return nil, fmt.Errorf("channel %s carries %s, which no dependency declares", decl.Name, decl.Payload.Name)
			}
			d.Message = importOfService(p.Service) + "." + goutil.GoPublicIdentifier(p.Type)
		case decl.TTLSeconds > 0:
			d.TTL = seconds(decl.TTLSeconds)
			g.UsesTime = true
		default:
			d.MaxTTL = seconds(decl.MaxTTLSeconds)
			g.UsesTime = true
		}
		params := map[string]bool{}
		for _, part := range decl.Parts {
			// productId is productID, as Go spells an initialism.
			name := goutil.GoPrivateIdentifier(part.Name)
			if base, ok := strings.CutSuffix(name, "Id"); ok && base != "" {
				name = base + "ID"
			}
			if reservedParams[name] || token.IsKeyword(name) || taken[name] || params[name] {
				name += "Part"
			}
			params[name] = true
			typ, wire, err := goPart(in, part, name, importOfService)
			if err != nil {
				return nil, fmt.Errorf("declaration %s part %s: %w", decl.Name, part.Name, err)
			}
			if strings.HasPrefix(typ, "scalars.") {
				g.UsesScalars = true
			}
			d.Params = append(d.Params, goParam{Part: part, Name: name, Type: typ, Wire: wire})
		}
		g.Defs = append(g.Defs, d)
	}
	slices.SortFunc(g.Imports, func(a, b goImport) int { return strings.Compare(a.Path, b.Path) })
	g.DepsField = goutil.GoPublicIdentifier(in.Store)
	g.Example = "Example"
	for _, d := range g.Defs {
		if d.Channel() {
			g.Example = d.Var
			break
		}
	}
	return g, nil
}

// goPart is a part's Go type and the expression that writes its value's
// wire string.
func goPart(in Input, part ir.MemoryStorePart, name string, alias func(string) string) (typ, wire string, err error) {
	switch part.Type.Name {
	case "string":
		return "string", name, nil
	case "boolean":
		return "bool", "memorystore.BooleanPart(" + name + ")", nil
	}
	if part.Format == ir.MemoryStorePartEnum {
		pkg := importOf(in.Schema, part.Type.Name)
		if pkg == "" {
			return "", "", fmt.Errorf("enum %s is imported from no dependency", part.Type.Name)
		}
		return alias(typegen.DependencyServiceName(pkg)) + "." + goutil.GoPublicIdentifier(part.Type.Name), "memorystore.EnumPart(" + name + ")", nil
	}
	symbol := codegen.BuildScalarTokens(part.Type.Name).Symbol
	switch part.Format {
	case ir.MemoryStorePartString:
		return "scalars." + symbol, "memorystore.StringPart(" + name + ")", nil
	case ir.MemoryStorePartInteger:
		return "scalars." + symbol, "memorystore.IntegerPart(" + name + ")", nil
	case ir.MemoryStorePartBoolean:
		return "scalars." + symbol, "memorystore.BooleanPart(" + name + ")", nil
	}
	return "", "", fmt.Errorf("type %s has format %q; want string, integer, boolean or enum", part.Type.Name, part.Format)
}

// seconds is a Go duration expression of n seconds.
func seconds(n int) string { return fmt.Sprintf("%d * time.Second", n) }

// docLines are a doc comment's lines.
func docLines(doc string) []string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return nil
	}
	return strings.Split(doc, "\n")
}

// module plans go.mod: the HTTP runtime, superscalar, and the Go types
// module of each dependency the definitions name, with the types modules
// those reach, each replaced by its directory when the build has one, or
// pinned to the release that generates it.
func (g *goPlan) module(in Input, n naming.Naming) error {
	opts := in.Context.Options
	pins := n.ReleasePins(opts.Paths, opts.ReleaseInfo()).Of(n.HTTPRuntimeGoModule, n.ScalarGoModule, n.SchemaIRGoModule, n.SchemaRuntimeGoModule)
	g.Requires = append(g.Requires,
		goRequire{n.HTTPRuntimeGoModule, pins.Version(n.HTTPRuntimeGoModule, zeroVersion)},
		goRequire{n.ScalarGoModule, pins.Version(n.ScalarGoModule, "v1.0.0")},
	)
	g.Indirect = append(g.Indirect,
		goRequire{n.SchemaIRGoModule, pins.Version(n.SchemaIRGoModule, zeroVersion)},
		goRequire{n.SchemaRuntimeGoModule, pins.Version(n.SchemaRuntimeGoModule, zeroVersion)},
	)
	for _, p := range []struct{ module, dir string }{
		{n.HTTPRuntimeGoModule, opts.Paths.HTTPRuntimeGo},
		{n.ScalarGoModule, opts.Paths.ScalarGo},
		{n.SchemaIRGoModule, opts.Paths.SchemaIR},
		{n.SchemaRuntimeGoModule, opts.Paths.SchemaRuntimeGo},
	} {
		if _, pinned := pins[p.module]; pinned || p.dir == "" {
			continue
		}
		rel, err := naming.RelPath(in.Dir, p.dir)
		if err != nil {
			return err
		}
		g.Replaces = append(g.Replaces, goReplace{p.module, rel})
	}
	for module, version := range pins {
		g.Replaces = append(g.Replaces, goReplace{module, module + " " + version})
	}
	direct := map[string]bool{}
	for _, imp := range g.Imports {
		direct[imp.Service] = true
	}
	closure, err := typesClosure(in, slices.Sorted(maps.Keys(direct)))
	if err != nil {
		return err
	}
	for _, service := range closure {
		module := n.GoTypesModule(service)
		if direct[service] {
			g.Requires = append(g.Requires, goRequire{module, zeroVersion})
		} else {
			g.Indirect = append(g.Indirect, goRequire{module, zeroVersion})
		}
		// The Go types modules sit beside one another, at types/go/<service>.
		g.Replaces = append(g.Replaces, goReplace{module, "../" + service})
	}
	byPath := func(a, b goRequire) int { return strings.Compare(a.Path, b.Path) }
	slices.SortFunc(g.Requires, byPath)
	slices.SortFunc(g.Indirect, byPath)
	slices.SortFunc(g.Replaces, func(a, b goReplace) int { return strings.Compare(a.Path, b.Path) })
	return nil
}

// typesClosure is services and every service whose Go types module theirs
// import, directly or through one another, sorted.
func typesClosure(in Input, services []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	queue := slices.Clone(services)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		schema, err := in.load(name)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", name, err)
		}
		deps := map[string]*ir.Schema{}
		for _, imp := range schema.Imports {
			dep := typegen.DependencyServiceName(imp.Package)
			if deps[dep] != nil {
				continue
			}
			d, err := in.load(dep)
			if err != nil {
				return nil, fmt.Errorf("load %s, a dependency of %s: %w", dep, name, err)
			}
			deps[dep] = d
		}
		imported, err := typegen.ImportedDependencies(schema, deps)
		if err != nil {
			return nil, err
		}
		queue = append(queue, imported...)
	}
	slices.Sort(out)
	return out, nil
}

// load is the dependency service's schema.
func (in Input) load(service string) (*ir.Schema, error) {
	if dep, ok := in.Dependencies[service]; ok {
		return dep, nil
	}
	if in.Context.LoadDependency == nil {
		return nil, fmt.Errorf("no dependency loader is configured")
	}
	return in.Context.LoadDependency(service)
}

var goDefinitions = template.Must(template.New(GoDefinitionsFile).Parse(`// Code generated by superschematic memorystoregen. DO NOT EDIT.

// Package {{ .Package }} holds the typed definitions of the memory store
// {{ .Store }} (D75): a value per channel and key its schema declares, whose
// For takes the declaration's parts in order and builds the channel or
// the key, so a caller never formats a name. A part that is no segment
// (empty, over 128 bytes, or holding ':', '*', '?', '[', ']', '@', '\' or
// a control character) is refused, never escaped.
//
// A channel or a key is used with the store the Deps of an API that
// lists the store holds:
//
//	ch, err := {{ .Package }}.{{ .Example }}.For(...)
//	err = ch.Publish(ctx, deps.{{ .DepsField }}, msg)
//
// While the store is unavailable every call returns
// memorystore.ErrUnavailable at once, and the caller takes the path it had
// before the store existed.
package {{ .Package }}

import (
{{- if .UsesTime }}
	"time"
{{ end }}
	"{{ .Runtime }}"
{{- if .UsesScalars }}
	scalars "{{ .Scalars }}"
{{- end }}
{{- range .Imports }}
	{{ .Alias }} "{{ .Path }}"
{{- end }}
)

// Store is the memory store's name, the first segment of every name it
// holds.
const Store = "{{ .Store }}"
{{ range .Defs }}
{{- $d := . }}
{{- if .Channel }}
// {{ .Var }} is the channel {{ .Decl.Name }}, {{ .Decl.NamePattern }},
{{- if eq .Message "memorystore.Signal" }} a signal, whose
// name is the whole message.
{{- else }} whose
// messages are {{ .Message }}.
{{- end }}
{{- else if .PerCall }}
// {{ .Var }} is the {{ .Decl.Kind }} {{ .Decl.Name }}, {{ .Decl.NamePattern }}, whose TTL
// each call gives, at most {{ .Decl.MaxTTLSeconds }}s.
{{- else }}
// {{ .Var }} is the {{ .Decl.Kind }} {{ .Decl.Name }}, {{ .Decl.NamePattern }}, which
// lives {{ .Decl.TTLSeconds }}s from the write that creates it.
{{- end }}
{{- if .Doc }}
//
{{- range .Doc }}
// {{ . }}
{{- end }}
{{- end }}
var {{ .Var }} {{ .Type }}

// {{ .Type }} is {{ .Var }}'s type.
type {{ .Type }} struct{}

// Name is the declaration's name.
func ({{ .Type }}) Name() string { return {{ printf "%q" .Decl.Name }} }
{{- if .PerCall }}

// MaxTTL is the longest TTL ForTTL takes.
func ({{ .Type }}) MaxTTL() time.Duration { return {{ .MaxTTL }} }

// ForTTL is the {{ .Decl.Kind }}{{ if .Params }} of {{ .PartList }}{{ end }}, expiring ttl after the
// write that creates it. It refuses a TTL below 1ms or above MaxTTL, and a
// part that is no segment.
func ({{ .Type }}) ForTTL({{ .Signature }}) (memorystore.Key, error) {
	ttl, err := memorystore.KeyTTL({{ printf "%q" .Decl.Name }}, ttl, {{ .MaxTTL }})
	if err != nil {
		return memorystore.Key{}, err
	}
	name, err := memorystore.Name({{ printf "%q" .Decl.Name }}{{ .Wires }})
	if err != nil {
		return memorystore.Key{}, err
	}
	return memorystore.NewKey({{ printf "%q" .Decl.Name }}, name, ttl), nil
}
{{- else if .Channel }}

// For is the channel{{ if .Params }} of {{ .PartList }}{{ end }}. It refuses a part that is no
// segment.
func ({{ .Type }}) For({{ .Signature }}) (memorystore.Channel[{{ .Message }}], error) {
	name, err := memorystore.Name({{ printf "%q" .Decl.Name }}{{ .Wires }})
	if err != nil {
		return memorystore.Channel[{{ .Message }}]{}, err
	}
	return memorystore.NewChannel[{{ .Message }}]({{ printf "%q" .Decl.Name }}, name), nil
}
{{- else }}

// TTL is how long a key lives from the write that creates it.
func ({{ .Type }}) TTL() time.Duration { return {{ .TTL }} }

// For is the {{ .Decl.Kind }}{{ if .Params }} of {{ .PartList }}{{ end }}. It refuses a part that is no
// segment.
func ({{ .Type }}) For({{ .Signature }}) (memorystore.Key, error) {
	name, err := memorystore.Name({{ printf "%q" .Decl.Name }}{{ .Wires }})
	if err != nil {
		return memorystore.Key{}, err
	}
	return memorystore.NewKey({{ printf "%q" .Decl.Name }}, name, {{ .TTL }}), nil
}
{{- end }}
{{ end }}`))

var goMod = template.Must(template.New(GoModFile).Parse(`// Code generated by superschematic memorystoregen. DO NOT EDIT.

module {{ .Module }}

go ` + GoVersion + `

require (
{{- range .Requires }}
	{{ .Path }} {{ .Version }}
{{- end }}
{{- range .Indirect }}
	{{ .Path }} {{ .Version }} // indirect
{{- end }}
)
{{ range .Replaces }}
replace {{ .Path }} => {{ .Target }}
{{- end }}
`))
