package topcoat

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/registry"
)

//go:embed templates/*.tmpl
var templates embed.FS

// crate is what the templates render: one service's Topcoat crate.
type crate struct {
	SchemaName string
	CrateName  string
	// APICrate is the API crate's Cargo name, APICrateIdent its name in
	// Rust source, and APIDepPath its directory from the crate's.
	APICrate      string
	APICrateIdent string
	APIDepPath    string
	// ExtTrait and SetupMethod name the RouterBuilder extension trait and
	// its method: RouterBuilderShopOrdersExt, shop_orders.
	ExtTrait    string
	SetupMethod string
	// HasAuth reports whether an operation needs a caller: the app then
	// hands the setup a PageAuthenticator.
	HasAuth bool
	// Operations are the mounted operations, each an in-process call;
	// Guards every operation, manual ones included.
	Operations []operation
	Guards     []operation
	Records    []record
	Forms      []form
}

// operation is one operation of the service, from the API crate's own
// record of it (registry.RustEndpoint).
type operation struct {
	Namespace    string
	Name         string
	Method       string
	Path         string
	Description  string
	SnakeName    string
	ConstName    string
	Field        string
	FunctionName string
	ArgsName     string // empty for an operation without arguments
	Output       string
	RequiresAuth bool
	Permissions  []string
	Manual       bool
}

func operationOf(e registry.RustEndpoint) operation {
	op := operation{
		Namespace:    e.Namespace,
		Name:         e.Name,
		Method:       strings.ToUpper(e.Method),
		Path:         e.Path,
		Description:  firstLine(e.Description),
		SnakeName:    e.SnakeName(),
		ConstName:    e.ConstName(),
		Field:        e.ImplementationsField(),
		FunctionName: e.FunctionName,
		Output:       e.OutputRustType,
		RequiresAuth: e.RequiresAuth,
		Permissions:  e.RequiredPerms,
		Manual:       e.Manual,
	}
	if e.HasArgs() {
		op.ArgsName = e.ArgsName
	}
	return op
}

func newCrate(c registry.GenerateContext, api *registry.RustAPI, cfg Config) (*crate, error) {
	service := c.Config.Name
	apiDir := registry.APIDir(c.Options.OutputRoot, service)
	rel, err := filepath.Rel(Dir(c.Options.OutputRoot, service), apiDir)
	if err != nil {
		return nil, fmt.Errorf("topcoat: path to the API crate of %s: %w", service, err)
	}
	pascal := pascalCase(service)
	out := &crate{
		SchemaName:    service,
		CrateName:     c.Options.Naming.RustCratePrefix + service + "-topcoat",
		APICrate:      api.CrateName,
		APICrateIdent: strings.ReplaceAll(api.CrateName, "-", "_"),
		APIDepPath:    filepath.ToSlash(rel),
		ExtTrait:      "RouterBuilder" + pascal + "Ext",
		SetupMethod:   registry.RustIdentifier(service, "service"),
		HasAuth:       api.HasAuth,
	}
	for _, endpoint := range api.Endpoints {
		out.Operations = append(out.Operations, operationOf(endpoint))
	}
	for _, endpoint := range api.AllEndpoints() {
		out.Guards = append(out.Guards, operationOf(endpoint))
	}
	schemas, err := schemasOf(c)
	if err != nil {
		return nil, err
	}
	if cfg.WritesRecords() {
		if out.Records, err = recordsOf(schemas); err != nil {
			return nil, fmt.Errorf("topcoat: records of %s: %w", service, err)
		}
	}
	if cfg.WritesForms() {
		if out.Forms, err = formsOf(schemas, api, c.Logf); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// schemasOf is the service's schema and its dependencies', in which a
// result type and the types it nests are declared.
func schemasOf(c registry.GenerateContext) (schemaSet, error) {
	schemas := schemaSet{c.Schema}
	for _, dep := range c.Config.Dependencies {
		schema, err := c.LoadDependency(dep.Name)
		if err != nil {
			return nil, fmt.Errorf("topcoat: load dependency %s: %w", dep.Name, err)
		}
		schemas = append(schemas, schema)
	}
	return schemas, nil
}

// FormsUse reports whether a form writes a field with the helper put, so
// forms.rs declares it.
func (c *crate) FormsUse(put string) bool {
	for _, f := range c.Forms {
		for _, field := range f.Fields {
			if field.Put == put {
				return true
			}
		}
	}
	return false
}

// write renders the crate into dir.
func (c *crate) write(dir string) error {
	files := []struct{ template, path string }{
		{"cargo.tmpl", "Cargo.toml"},
		{"lib.tmpl", filepath.Join("src", "lib.rs")},
		{"operations.tmpl", filepath.Join("src", "operations.rs")},
	}
	if len(c.Records) > 0 {
		files = append(files,
			struct{ template, path string }{"records.tmpl", filepath.Join("src", "records.rs")},
			struct{ template, path string }{"wire.tmpl", filepath.Join("src", "wire.rs")},
		)
	}
	if len(c.Forms) > 0 {
		files = append(files, struct{ template, path string }{"forms.tmpl", filepath.Join("src", "forms.rs")})
	}
	tmpl, err := template.New("topcoat").Funcs(template.FuncMap{
		"rustString": rustString,
		"join":       strings.Join,
	}).ParseFS(templates, "templates/*.tmpl")
	if err != nil {
		return err
	}
	// A crate written before with records or forms keeps no stale module.
	for _, stale := range []string{"records.rs", "wire.rs", "forms.rs"} {
		if err := os.Remove(filepath.Join(dir, "src", stale)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return err
	}
	for _, file := range files {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, file.template, c); err != nil {
			return fmt.Errorf("topcoat: render %s: %w", file.path, err)
		}
		if err := os.WriteFile(filepath.Join(dir, file.path), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pascalCase is a service name as a Rust type name: shop-orders is
// ShopOrders.
func pascalCase(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			if upper && r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			upper = false
		default:
			upper = true
		}
	}
	return b.String()
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

// rustString renders a Rust string literal.
func rustString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s) + `"`
}

// sortedKeys returns m's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
