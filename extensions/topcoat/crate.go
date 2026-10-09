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

	ir "github.com/parable-work/superschematic/ir"
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
	// Identity reports that the API authenticates with the identity runtime
	// (D50): the crate then offers IdentityPageAuthenticator, which reads a
	// page's caller from the session the JSON API signed in, and forwards
	// the API crate's store features.
	Identity bool
	// Operations are the operations with an in-process call; Guards every
	// operation, those without one included.
	Operations []operation
	Guards     []operation
	Records    []record
	Forms      []form
	// FormStructs are the structs of the forms' input types and of the
	// object types they nest.
	FormStructs []*formStruct
	Procedures  []procedure
	// Views are each record's display components, and EnumLabels the
	// label functions of the enums they show.
	Views      []view
	EnumLabels []enumLabel
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
	// ServiceOnly marks an operation whose route admits only a service
	// caller (@requireService). Its in-process call admits the end user
	// alone, and its doc says so (D37, amended).
	ServiceOnly bool
	// NoCall is why the operation has no in-process call, and so no
	// procedure; NoProcedure why one with a call has no procedure. Each is
	// a reason (noCall, noProcedure), empty when the operation has the
	// item.
	NoCall      string
	NoProcedure string
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
		ServiceOnly:  requiresService(e),
	}
	if e.HasArgs() {
		op.ArgsName = e.ArgsName
	}
	return op
}

// declared is an operation of the API crate with its declaration in the
// schema.
type declared struct {
	endpoint registry.RustEndpoint
	op       *ir.FieldDef
}

// noCall is why an operation has no in-process call, or empty when it has
// one: a clause the guard's doc and the build log complete ("It has no
// in-process call: <reason>."). Without the call it has no procedure
// either. A call would skip what the operation's route does before its
// handler, or what the service mounts in its place. op is the operation's
// declaration, nil for a manual one.
func noCall(e registry.RustEndpoint, op *ir.FieldDef) string {
	signed := e.WebhookProvider != ""
	switch {
	case e.Manual:
		return "the service mounts it (@manualRouteRegistration)"
	case e.IdentityOperation != "":
		return "the identity runtime serves it, as one of the user model's operations (D50)"
	case op.Webhook && signed:
		return "a third party calls it (@webhook), and its route checks the third party's signature first (@hmacVerified)"
	case op.Webhook:
		return "a third party calls it (@webhook)"
	case signed:
		return "its route checks a provider's signature first (@hmacVerified)"
	}
	return ""
}

// noProcedure is why an operation with an in-process call has no
// procedure, or empty when it has one: a clause the call's doc and the
// build log complete ("It has no procedure: <reason>."). A procedure is a
// route browser code calls, so it exists only where a browser's request
// meets the operation's route's rules.
func noProcedure(e registry.RustEndpoint) string {
	if requiresService(e) {
		return "a browser holds no service credential (@requireService)"
	}
	return ""
}

// requiresService reports whether the operation's route admits only a
// service caller: its effective clause is @requireService. An
// @allowService route also admits an end user, as a procedure does.
func requiresService(e registry.RustEndpoint) bool {
	return e.ServiceCallers != nil && e.ServiceCallers.Mode == ir.ServiceCallersRequire
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
		Identity:      api.Identity != nil,
	}
	schemas, err := schemasOf(c)
	if err != nil {
		return nil, err
	}
	// The operations a page calls in-process, and of those the ones browser
	// code calls through a procedure.
	var inProcess, fromBrowser []declared
	for _, endpoint := range api.AllEndpoints() {
		// The service's own route reads a manual operation's request, so
		// its declaration need not name a route this one matches.
		var op *ir.FieldDef
		if !endpoint.Manual {
			if op, err = schemas.operation(endpoint); err != nil {
				return nil, err
			}
		}
		o := operationOf(endpoint)
		o.NoCall = noCall(endpoint, op)
		if o.NoCall == "" && cfg.WritesProcedures() {
			o.NoProcedure = noProcedure(endpoint)
		}
		out.Guards = append(out.Guards, o)
		if o.NoCall != "" {
			c.Logf("  - topcoat: no in-process call for %s.%s: %s\n", o.Namespace, o.Name, o.NoCall)
			continue
		}
		out.Operations = append(out.Operations, o)
		inProcess = append(inProcess, declared{endpoint, op})
		if o.NoProcedure != "" {
			c.Logf("  - topcoat: no procedure for %s.%s: %s\n", o.Namespace, o.Name, o.NoProcedure)
			continue
		}
		fromBrowser = append(fromBrowser, declared{endpoint, op})
	}
	// A procedure's arguments and result are records, so procedures need
	// them.
	if cfg.WritesRecords() {
		records := newRecordBuilder(schemas)
		if err := records.addResults(inProcess); err != nil {
			return nil, fmt.Errorf("topcoat: records of %s: %w", service, err)
		}
		if cfg.WritesProcedures() {
			if out.Procedures, err = proceduresOf(records, fromBrowser, service); err != nil {
				return nil, err
			}
		}
		out.Records = records.sorted()
		if cfg.WritesViews() {
			if out.Views, out.EnumLabels, err = viewsOf(schemas, out.Records); err != nil {
				return nil, err
			}
		}
	}
	if cfg.WritesForms() {
		if out.Forms, out.FormStructs, err = formsOf(schemas, inProcess, c.Logf); err != nil {
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

// ProceduresTakeArgs reports whether a procedure takes arguments, so
// procedures.rs declares the decode its records' to_args call.
func (c *crate) ProceduresTakeArgs() bool {
	for _, p := range c.Procedures {
		if p.ArgsRecord != "" {
			return true
		}
	}
	return false
}

// FormsUse reports whether a form uses item, so forms.rs declares the
// helpers it needs: a kind of field ("value", "object", "objectRows",
// "valueRows", "group"), a reader ("text", "integer", "number",
// "boolean", "date_time", "json_text"), or "read", "list", "rows",
// "noRows", "choosable", and what a rendered field needs ("checked",
// "localDateTime", "showsAll", "showsRow", "showsRows").
func (c *crate) FormsUse(item string) bool {
	if item == "choosable" {
		for _, f := range c.Forms {
			if f.Choosable {
				return true
			}
		}
		return false
	}
	for _, st := range c.FormStructs {
		if item == "rows" && st.Rows {
			return true
		}
		for _, f := range st.Fields {
			if formUses(st, f, item) {
				return true
			}
		}
	}
	return false
}

// formUses reports whether f, a field of st, uses item (FormsUse).
func formUses(st *formStruct, f *formField, item string) bool {
	shown := st.Shown && !f.Hidden
	reads := f.Kind == kindValue || f.Kind == kindValueRows
	switch item {
	case kindValue, kindObject, kindObjectRows, kindValueRows, kindGroup:
		return f.Kind == item
	case "read":
		return reads
	case "list":
		return f.Kind == kindObjectRows || f.Kind == kindValueRows || f.Kind == kindGroup
	case "noRows":
		return f.Kind == kindValueRows || f.Kind == kindObjectRows && !f.Child.Rows
	case "checked":
		return shown && f.Kind == kindGroup
	case "localDateTime":
		return shown && reads && f.Control == "datetime-local"
	case "showsAll":
		return shown && (f.Kind == kindGroup || f.Kind == kindValue && f.Control == "textarea")
	case "showsRow":
		return shown && f.Kind == kindValueRows
	case "showsRows":
		return shown && f.Kind == kindObjectRows
	}
	return reads && f.Read == item
}

// ControlledProcedures are the procedures whose routes have a traffic
// control: each gets a ProcedureControls layer on its path.
func (c *crate) ControlledProcedures() []procedure {
	var out []procedure
	for _, p := range c.Procedures {
		if p.HasControls() {
			out = append(out, p)
		}
	}
	return out
}

// ControlsUse reports whether a procedure's route has the control
// ("rate_limit", "body_limit" or "timeout"), so procedures.rs declares
// ProcedureControls' builder method for it.
func (c *crate) ControlsUse(control string) bool {
	for _, p := range c.Procedures {
		switch {
		case control == "rate_limit" && p.RateLimit > 0,
			control == "body_limit" && p.BodyLimit > 0,
			control == "timeout" && p.Timeout > 0:
			return true
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
	if len(c.Procedures) > 0 {
		files = append(files, struct{ template, path string }{"procedures.tmpl", filepath.Join("src", "procedures.rs")})
	}
	if len(c.Views) > 0 {
		files = append(files, struct{ template, path string }{"views.tmpl", filepath.Join("src", "views.rs")})
	}
	tmpl, err := template.New("topcoat").Funcs(template.FuncMap{
		"rustString":   rustString,
		"escapeBraces": escapeBraces,
		"join":         strings.Join,
		"doc":          doc,
	}).ParseFS(templates, "templates/*.tmpl")
	if err != nil {
		return err
	}
	// A crate written before with records or forms keeps no stale module.
	for _, stale := range []string{"records.rs", "wire.rs", "forms.rs", "procedures.rs", "views.rs"} {
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

// doc wraps text as Rust doc comment lines of at most 80 columns where its
// words allow, as the templates wrap their own.
func doc(text string) string {
	var lines []string
	line := "///"
	for _, word := range strings.Fields(text) {
		if line != "///" && len(line)+1+len(word) > 80 {
			lines = append(lines, line)
			line = "///"
		}
		line += " " + word
	}
	return strings.Join(append(lines, line), "\n")
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
