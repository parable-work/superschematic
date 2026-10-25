package topcoat

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	// Operations are the operations with an in-process call an end user's
	// page makes, every one with a call but a @requireService one;
	// ServiceCalls those a service caller calls (operations::as_service),
	// each with a @requireService or @allowService clause (D37, amended);
	// Guards every operation, those without a call included.
	Operations   []operation
	ServiceCalls []operation
	Guards       []operation
	Records      []record
	// PageRecords are the records of the pages the paged operations
	// return (D60).
	PageRecords []pageRecord
	Forms       []form
	// FormStructs are the structs of the forms' input types and of the
	// object types they nest.
	FormStructs []*formStruct
	// ArgForms are the forms over each operation's arguments other than
	// its input (argforms.go).
	ArgForms   []argForm
	Procedures []procedure
	// Views are each record's display components, and EnumLabels the
	// label functions of the enums they show.
	Views      []view
	EnumLabels []enumLabel
	// References are the options and labels of what the forms and the
	// records hold that refers to an item (D61), nil when nothing does
	// (references.go).
	References *referencesModel
	// Pages are the pages of each resource the operations describe, nil
	// unless outputs.topcoat.pages turns them on (pages.go).
	Pages *pagesModel
	// Engine is the engine of the API's engine database, which the crate
	// opens in-process on SQLite and mounts at /engine (D58), nil when its
	// database is none (engine.go).
	Engine *engineModel
	// EnginePages are the pages of the engine's types under
	// <prefix>/engine, nil unless the pages are on and the crate mounts an
	// engine (engine_pages.go); EnginePanels the panels its instance pages
	// show per behavior, set whenever EnginePages is (engine_panels.go).
	EnginePages  *enginePagesModel
	EnginePanels *enginePanelsModel
	// IdentityPages are sign-in, sign-out, the account, users and roles
	// pages, nil unless the pages are on and the API authenticates with the
	// identity runtime (identity_pages.go).
	IdentityPages *identityPagesModel
	// Money reports that a display component shows an amount by its
	// currency, or a form reads one as a decimal (money.go). ScalarCrate is the scalar crate, which gives each
	// currency's minor units, and ScalarPath its directory from the
	// crate's, empty when [paths] names none, as for the types crates.
	Money       bool
	ScalarCrate string
	ScalarPath  string
	// hosting gives the crates of the API dependencies one app hosts
	// beside the service (D61, amended), each read the first time a
	// reference's options operation is the dependency's.
	hosting *hosting
	// noCalls is why each operation without an in-process call has none.
	noCalls map[*ir.FieldDef]string
}

// hostedCrate is the Topcoat crate of an API dependency that the app
// hosting the service hosts too (D61, amended): its Cargo name, its name in
// Rust source and its directory from the crate's, and its operations' in-
// process calls, by their rules and under its own setup.
type hostedCrate struct {
	Service string // fixture-console-suppliers-api
	Crate   string // schemas-fixture-console-suppliers-api-topcoat
	Ident   string // schemas_fixture_console_suppliers_api_topcoat
	Path    string // ../fixture-console-suppliers-api
	// Identity reports that its API authenticates with the identity
	// runtime (D50), so its crate declares the identity-* features, which
	// the referring crate's forward to it when it declares them too.
	Identity bool
	// calls are its in-process calls by operation, noCall why each other
	// operation has none.
	calls  map[*ir.FieldDef]declared
	noCall map[*ir.FieldDef]string
	// used reports whether a reference calls it, so Cargo.toml depends on
	// it.
	used bool
}

// UsedHosted are the hosted crates a reference calls, by service, which
// Cargo.toml depends on.
func (c *crate) UsedHosted() []*hostedCrate {
	var out []*hostedCrate
	if c.hosting == nil {
		return out
	}
	for _, service := range sortedKeys(c.hosting.crates) {
		if h := c.hosting.crates[service]; h != nil && h.used {
			out = append(out, h)
		}
	}
	return out
}

// IdentityHosted are the hosted crates a reference calls whose API
// authenticates with the identity runtime, when the crate's does too: its
// identity-* features turn theirs on, so one feature picks the store of
// every crate the app's pages call.
func (c *crate) IdentityHosted() []*hostedCrate {
	if !c.Identity {
		return nil
	}
	var out []*hostedCrate
	for _, h := range c.UsedHosted() {
		if h.Identity {
			out = append(out, h)
		}
	}
	return out
}

// hosting resolves the crates of the service's API dependencies that one
// app hosts beside it (D61, amended), by service: each one the table lists
// beside the service, whose API server is Rust and whose Topcoat crate the
// build wrote before the service's, as build --with-deps and build-all do.
// A dependency is read only when a reference's options operation is its
// own, and once: one that no reference calls costs nothing and fails
// nothing, and one that cannot be hosted, its API unreadable among the
// reasons, leaves the reference out with the reason in the build log.
type hosting struct {
	c     registry.GenerateContext
	table Table
	// crates are the dependencies read so far, nil for one that has no
	// hosted crate, and not why.
	crates map[string]*hostedCrate
	not    map[string]string
}

func newHosting(c registry.GenerateContext, table Table) *hosting {
	return &hosting{c: c, table: table, crates: map[string]*hostedCrate{}, not: map[string]string{}}
}

// crate is the hosted crate of service, an API dependency, read the first
// time it is asked for; nil, with why, when it has none.
func (h *hosting) crate(service string) (*hostedCrate, string) {
	if crate, read := h.crates[service]; read {
		return crate, h.not[service]
	}
	crate, reason := h.read(service)
	h.crates[service], h.not[service] = crate, reason
	return crate, reason
}

// read builds the hosted crate of service, or gives why it has none.
func (h *hosting) read(service string) (*hostedCrate, string) {
	c := h.c
	self := c.Config.Name
	isAPI := false
	for _, dep := range c.Config.Dependencies {
		isAPI = isAPI || dep.Name == service && dep.Kind == ir.SchemaKindAPI
	}
	switch {
	case !isAPI:
		return nil, fmt.Sprintf("%s is not an API dependency of %s", service, self)
	case !slices.Contains(h.table.Services, service) || !slices.Contains(h.table.Services, self):
		return nil, fmt.Sprintf("[extension.topcoat] services does not list %s beside %s, so no app hosts both", service, self)
	}
	api, err := registry.RustAPIOfService(c, service)
	if err != nil {
		return nil, fmt.Sprintf("%s's Rust API could not be read: %v", service, err)
	}
	if api == nil {
		return nil, noRustAPI(c, service)
	}
	dir := Dir(c.Options.OutputRoot, service)
	if _, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err != nil {
		return nil, service + "'s Topcoat crate is not in the output root, which a build of it first writes (build --with-deps)"
	}
	schema, err := c.LoadDependency(service)
	if err != nil {
		return nil, fmt.Sprintf("%s's schema could not be read: %v", service, err)
	}
	rel, err := filepath.Rel(Dir(c.Options.OutputRoot, self), dir)
	if err != nil {
		return nil, fmt.Sprintf("the path to %s's Topcoat crate: %v", service, err)
	}
	crate := &hostedCrate{
		Service:  service,
		Crate:    c.Options.Naming.RustCratePrefix + service + "-topcoat",
		Path:     filepath.ToSlash(rel),
		Identity: api.Identity != nil,
		calls:    map[*ir.FieldDef]declared{},
		noCall:   map[*ir.FieldDef]string{},
	}
	crate.Ident = strings.ReplaceAll(crate.Crate, "-", "_")
	for _, endpoint := range api.AllEndpoints() {
		if endpoint.Manual {
			continue
		}
		op, err := schemaSet{schema}.operation(endpoint)
		if err != nil {
			return nil, fmt.Sprintf("%s's Rust API does not match its schema: %v", service, err)
		}
		reason := noCall(endpoint, op)
		if reason == "" && requiresService(endpoint) {
			reason = "its route admits only a service caller (@requireService)"
		}
		if reason != "" {
			crate.noCall[op] = reason
			continue
		}
		crate.calls[op] = declared{endpoint, op}
	}
	return crate, ""
}

// noRustAPI is why registry.RustAPIOfService gave service, an API
// dependency, no Rust API crate: its config generates no API server, its
// server is not Rust, or its schema declares no operations.
func noRustAPI(c registry.GenerateContext, service string) string {
	var outputs *registry.Outputs
	cfg, err := c.Options.LoadDependencyConfig(service)
	if err == nil {
		outputs, err = registry.ParseOutputs(cfg.Outputs, c.Registry)
	}
	if err != nil {
		return fmt.Sprintf("%s's config could not be read: %v", service, err)
	}
	return noRustAPIOf(service, outputs)
}

// noRustAPIOf is noRustAPI's reason, from the dependency's outputs.
func noRustAPIOf(service string, outputs *registry.Outputs) string {
	switch {
	case !outputs.APIEnabled():
		return service + "'s config generates no API server, so it has no crate"
	case outputs.API.Language != registry.APILanguageRust:
		return service + "'s API server is not Rust, so it has no crate"
	}
	return service + " declares no operations, so its API crate has none to call"
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
	// caller (@requireService): it has no end-user call, and its guard
	// refuses an end user (D37, amended). AsService marks one with a
	// @requireService or @allowService clause, which a service caller
	// calls through operations::as_service.
	ServiceOnly bool
	AsService   bool
	// Paged marks a @paginated operation (D60), whose call returns a page;
	// Sorted one with a SortBy argument (D60, amended), whose call unbinds
	// the cursor from the sort before the implementation reads it and binds
	// the page's next to it after, as the router does.
	Paged  bool
	Sorted bool
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
		AsService:    e.ServiceCallers != nil,
		Paged:        e.Page != nil,
		Sorted:       e.Page != nil && e.Page.Sort != nil,
	}
	if e.HasArgs() {
		op.ArgsName = e.ArgsName
	}
	return op
}

// ServiceOnlyGuardDoc is the doc of a @requireService operation's guard
// in operations.rs, which refuses an end user as the route refuses a
// request without a service credential (D37, amended). It names the
// service caller's guard, `as_service(caller).can_<op>`, only where
// AsService has one: an operation without an in-process call, a manual
// one say, has neither its call nor its guard there, and the line after
// the doc says why it has no call.
func (o operation) ServiceOnlyGuardDoc() string {
	doc := fmt.Sprintf("Admits the caller of %s.%s as its route admits a request without a service credential: the route admits only a service caller (@requireService), so an end user is refused 401 `service_unauthorized`, signed in or not", o.Namespace, o.Name)
	if o.NoCall != "" {
		return doc + " (D37, amended)."
	}
	return doc + fmt.Sprintf(". `as_service(caller).can_%s(cx)` admits a service caller (D37, amended).", o.SnakeName)
}

// ServiceGuardDoc is the doc of the operation's guard in AsService: how
// its route admits a service caller, then what it asks of the end user
// the service forwards (D37, amended).
func (o operation) ServiceGuardDoc() string {
	refuses := "401 without a signed-in end user"
	if o.ServiceOnly {
		refuses += " to forward"
	}
	if len(o.Permissions) > 0 {
		refuses += ", 403 without one of " + strings.Join(o.Permissions, ", ")
	}
	head := fmt.Sprintf("Admits the caller as %s.%s's route admits a service: ", o.Namespace, o.Name)
	switch {
	case !o.ServiceOnly:
		return head + "a caller its `from` lists stands in for the end user, so `Ok(None)`; any other goes on to the end-user step, as `can_" + o.SnakeName + "` admits it: " + refuses + "."
	case o.RequiresAuth:
		return head + "403 `service_forbidden` when its `from` lists none of the APIs the caller serves; then the page's caller, as the end user the service forwards: " + refuses + "."
	default:
		return head + "403 `service_forbidden` when its `from` lists none of the APIs the caller serves; it asks for no end user, so `Ok(None)` otherwise."
	}
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

func newCrate(c registry.GenerateContext, api *registry.RustAPI, cfg Config, table Table) (*crate, error) {
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
		noCalls:       map[*ir.FieldDef]string{},
	}
	schemas, err := schemasOf(c)
	if err != nil {
		return nil, err
	}
	out.hosting = newHosting(c, table)
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
		if o.NoCall != "" && op != nil {
			out.noCalls[op] = o.NoCall
		}
		if o.NoCall != "" {
			c.Logf("  - topcoat: no in-process call for %s.%s: %s\n", o.Namespace, o.Name, o.NoCall)
			continue
		}
		// A @requireService operation is called only as a service: a page
		// cannot call it for an end user, so the compiler refuses the
		// mistake (D37, amended).
		if o.ServiceOnly {
			c.Logf("  - topcoat: no end-user call for %s.%s: its route admits only a service caller (@requireService); operations::as_service calls it\n", o.Namespace, o.Name)
		} else {
			out.Operations = append(out.Operations, o)
		}
		if o.AsService {
			out.ServiceCalls = append(out.ServiceCalls, o)
		}
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
		out.PageRecords = records.sortedPages()
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
		inputs := map[string]*formStruct{}
		for _, f := range out.Forms {
			inputs[f.TypeName] = f.Struct
		}
		if out.ArgForms, err = argFormsOf(schemas, inProcess, inputs, c.Logf); err != nil {
			return nil, err
		}
	}
	// The pages' resources, whose trail's steps a reference may name.
	var site *pageSite
	var trails []trailStep
	if cfg.WritesPages() {
		site = siteOf(out, schemas, inProcess)
		trails = trailSteps(site.roots)
	}
	if out.References, err = referencesOf(out, schemas, inProcess, trails, c.Logf); err != nil {
		return nil, err
	}
	if err := out.markPicks(); err != nil {
		return nil, err
	}
	if out.Engine, err = engineOf(c, api, Dir(c.Options.OutputRoot, service)); err != nil {
		return nil, err
	}
	if site != nil {
		title := humanize(strings.ReplaceAll(service, "-", " "))
		if out.Pages, err = pagesOf(out, schemas, site, cfg.PagesPrefix(), title, cfg.PagesLive(), c.Logf); err != nil {
			return nil, err
		}
	}
	if err := out.consoleModules(c, api, cfg, schemas); err != nil {
		return nil, err
	}
	return out, nil
}

// consoleModules sets the models of the console's own pages, the engine's
// and the identity runtime's, the engine's behavior panels, and money,
// each from what newCrate built before, then what the pages share with
// them (hookPages).
func (c *crate) consoleModules(ctx registry.GenerateContext, api *registry.RustAPI, cfg Config, schemas schemaSet) error {
	var err error
	if c.EnginePages, err = enginePagesOf(ctx, cfg, c); err != nil {
		return err
	}
	if c.EnginePanels, err = enginePanelsOf(ctx, c); err != nil {
		return err
	}
	if c.EnginePages != nil && c.EnginePanels == nil {
		return fmt.Errorf("topcoat: %s has engine pages without their panels, which an instance's page calls", c.SchemaName)
	}
	if c.IdentityPages, err = identityPagesOf(ctx, api, cfg, c); err != nil {
		return err
	}
	if c.Money, err = moneyOf(ctx, schemas, c); err != nil {
		return err
	}
	c.ScalarCrate = ctx.Options.Naming.ScalarRustCrate
	if scalar := ctx.Options.Paths.ScalarRust; scalar != "" {
		rel, err := filepath.Rel(Dir(ctx.Options.OutputRoot, c.SchemaName), scalar)
		if err != nil {
			return fmt.Errorf("topcoat: path to the scalar crate: %w", err)
		}
		c.ScalarPath = filepath.ToSlash(rel)
	}
	c.hookPages()
	return nil
}

// WritesMoney reports whether views.rs writes money_text: a display
// component shows money, a form reads an amount as a decimal, or the
// engine's panels may show money (a Holds' or a Payments' amounts).
// Cargo.toml then depends on the scalar crate, which views.rs and forms.rs
// read each currency's minor units from.
func (c *crate) WritesMoney() bool { return c.Money || c.EnginePanels != nil }

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

// ProceduresTakeArgs reports whether a procedure takes arguments beside a
// page, so procedures.rs declares the decode its records' to_args call.
func (c *crate) ProceduresTakeArgs() bool {
	for _, p := range c.Procedures {
		if len(p.Args) > 0 {
			return true
		}
	}
	return false
}

// HasRecords reports whether the crate has a record, of a type or of a
// page, so it writes records.rs and wire.rs.
func (c *crate) HasRecords() bool { return len(c.Records) > 0 || len(c.PageRecords) > 0 }

// RecordsUseTypes reports whether records.rs names the types module: a
// record converts from its type, and a page record from a page of one.
func (c *crate) RecordsUseTypes() bool {
	if len(c.Records) > 0 {
		return true
	}
	for _, p := range c.PageRecords {
		if strings.Contains(p.Page, "types::") {
			return true
		}
	}
	return false
}

// Paged reports whether an operation with an in-process call pages (D60),
// an end user's or a service's, so operations.rs names the runtime its
// result's page is of.
func (c *crate) Paged() bool {
	for _, o := range slices.Concat(c.Operations, c.ServiceCalls) {
		if o.Paged {
			return true
		}
	}
	return false
}

// Sorted reports whether an operation with an in-process call sorts (D60,
// amended), so operations.rs says how its call unbinds and binds the page.
func (c *crate) Sorted() bool {
	for _, o := range slices.Concat(c.Operations, c.ServiceCalls) {
		if o.Sorted {
			return true
		}
	}
	return false
}

// RecordsUseWire reports whether a record field reads its value with a
// wire helper, or the crate has a page record (D60), which reads its items
// and cursor with them, so records.rs imports the module. A record whose
// fields are all left out reads none, and neither does a field that holds
// a nested record by itself.
func (c *crate) RecordsUseWire() bool {
	if len(c.PageRecords) > 0 {
		return true
	}
	for _, r := range c.Records {
		for _, field := range r.Fields {
			if strings.Contains(field.Read, "wire::") {
				return true
			}
		}
	}
	return false
}

// HasForms reports whether the crate writes forms.rs: it has a form of
// either kind, an input form or an argument form, or pages, which read a
// refusal's errors from it (Refusal, FormErrors) whether or not a page
// submits a form.
func (c *crate) HasForms() bool { return len(c.Forms) > 0 || len(c.ArgForms) > 0 || c.Pages != nil }

// HasChoices reports whether a form or a display component takes the app's
// choices, so the crate writes choices.rs, which forms.rs re-exports.
func (c *crate) HasChoices() bool { return c.HasForms() || len(c.Views) > 0 }

// FormsUse reports whether a form uses item, so forms.rs declares what it
// needs: a kind of an input form's field ("value", "object", "objectRows",
// "valueRows", "group"); a reader ("text", "integer", "number", "boolean",
// "yes_no", "date_time", "json_text"), "read" for any; "put", "list" and
// "rows" for writing an input form; "first", "values", "postedText" and
// "postedRows" for reading a post, and "maxRows" when a list's limit is
// MAX_ROWS; "args", "argsSingle", "argsList", "argsInput" and "query" for
// argument forms; "noRows", "choosable"; "picks" for a search picker's
// form, and "pickRows" for one with rows, whose removal numbers the
// pickers' texts again; and what a rendered control needs ("checked",
// "localDateTime", "showsAll", "showsRow", "showsRows").
func (c *crate) FormsUse(item string) bool {
	return c.formUses()[item]
}

// formUses is every item a form of the crate uses (FormsUse).
func (c *crate) formUses() map[string]bool {
	uses := map[string]bool{}
	for _, f := range c.Forms {
		uses["choosable"] = uses["choosable"] || f.Choosable
		uses["picks"] = uses["picks"] || f.Picks
		uses["pickRows"] = uses["pickRows"] || f.Picks && f.Struct.Rows
	}
	for _, st := range c.FormStructs {
		if st.Rows {
			uses["rows"], uses["postedText"] = true, true
		}
		for _, f := range st.Fields {
			shown := st.Shown && !f.Hidden
			uses[f.Kind] = true
			switch f.Kind {
			case kindValue:
				uses["put"], uses["postedText"] = true, true
			case kindValueRows:
				uses["list"], uses["postedRows"], uses["postedText"], uses["noRows"], uses["showsRow"] = true, true, true, true, uses["showsRow"] || shown
			case kindObjectRows:
				uses["list"], uses["postedRows"], uses["showsRows"] = true, true, uses["showsRows"] || shown
				uses["noRows"] = uses["noRows"] || !f.Child.Rows
			case kindGroup:
				uses["list"], uses["values"] = true, true
			}
			if f.Kind == kindValue || f.Kind == kindValueRows {
				uses["read"], uses[f.Read] = true, true
			}
			if (f.Kind == kindValueRows || f.Kind == kindObjectRows) && f.Limit() == "MAX_ROWS" {
				uses["maxRows"] = true
			}
			if shown {
				addRendered(uses, f)
			}
		}
	}
	for _, f := range c.ArgForms {
		uses["args"], uses["choosable"] = true, uses["choosable"] || f.Choosable
		uses["picks"] = uses["picks"] || f.Picks
		uses["pickRows"] = uses["pickRows"] || f.Picks && f.Input != nil && f.Input.Struct.Picks && f.Input.Struct.Rows
		uses["query"] = uses["query"] || f.IsGet()
		uses["argsInput"] = uses["argsInput"] || f.Input != nil
		if f.Paged {
			uses["paged"], uses["first"] = true, true
		}
		for _, a := range f.Fields {
			uses["read"], uses[a.Read] = true, true
			if a.IsList() {
				uses["values"], uses["argsList"] = true, true
			} else {
				uses["first"], uses["argsSingle"] = true, true
			}
			if a.Kind != kindPath {
				addRendered(uses, a.formField)
				if a.Kind == kindRepeated {
					uses["showsAll"] = true
				}
			}
		}
	}
	return uses
}

// PickShards are the pickers' shards (D61, amended) the forms render,
// sorted and joined, for forms.rs's use.
func (c *crate) PickShards() string {
	if c.References == nil {
		return ""
	}
	var names []string
	for _, s := range c.References.Sources {
		if c.References.UsesPick(s) {
			names = append(names, s.Base+"_pick")
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// markPicks makes each form's control whose reference's options operation
// searches a search picker (D61, amended): its form holds the pickers'
// texts, a post's "Find" acts on it, and its view renders each through its
// source's shard. A form whose field is named as what a form keeps for
// its pickers is refused.
func (c *crate) markPicks() error {
	m := c.References
	if !m.Picking() {
		return nil
	}
	byForm := map[string]*refForm{}
	for _, f := range m.Forms {
		byForm[f.Form] = f
	}
	renders := func(f *refForm, post bool) map[string]pickRender {
		picks := map[string]pickRender{}
		for _, site := range f.Sites {
			if site.Pick {
				picks[site.Path] = pickRender{Shard: site.Source.Base + "_pick", Post: post}
			}
		}
		return picks
	}
	for i := range c.Forms {
		f := &c.Forms[i]
		if rf := byForm[f.Name]; rf != nil && rf.Picks() {
			f.Picks, f.Struct.Picks = true, true
			if err := reservedPicks(f.Name, f.Struct.Fields); err != nil {
				return err
			}
			r := newFormRenderer()
			r.picks = renders(rf, true)
			r.fields(scope{form: "form", idBase: f.Struct.IDBase}, f.Struct, 2)
			f.Choosable, f.UsesForm, f.Body = r.choosable, r.used["form"], r.String()
		}
	}
	for i := range c.ArgForms {
		f := &c.ArgForms[i]
		if rf := byForm[f.Name]; rf != nil && rf.Picks() {
			f.Picks = true
			fields := make([]*formField, len(f.Fields))
			for j, a := range f.Fields {
				fields[j] = a.formField
			}
			if err := reservedPicks(f.Name, fields); err != nil {
				return err
			}
			f.render(renders(rf, !f.IsGet()))
		}
	}
	return nil
}

// addRendered adds what a rendered control of f needs.
func addRendered(uses map[string]bool, f *formField) {
	switch {
	case f.Kind == kindGroup:
		uses["checked"], uses["showsAll"] = true, true
	case f.Control == "textarea":
		uses["showsAll"] = true
	case f.Control == "datetime-local":
		uses["localDateTime"] = true
	}
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

// ControlledPages reports whether a page calls an operation whose route
// has a traffic control, so `<service>(...)` adds the pages' layer that
// applies it.
func (c *crate) ControlledPages() bool { return c.Pages != nil && len(c.Pages.Controlled) > 0 }

// SetupLayers is the expression `<service>(...)` returns its router
// through, `router` wrapped in each module's call that adds its route
// controls' layers; empty when no procedure or page has any. A module
// whose routes have a rate limit takes the app's registry, `rate_limits`,
// so its procedures and pages share each operation's limiter with its
// route (D44, amended).
func (c *crate) SetupLayers() string {
	expr := ""
	wrap := func(call string, limited bool) {
		if expr == "" {
			expr = "router"
		}
		expr = call + "(" + expr
		if limited {
			expr += ", &rate_limits"
		}
		expr += ")"
	}
	if len(c.ControlledProcedures()) > 0 {
		wrap("procedures::route_controls", c.ControlsUse("rate_limit"))
	}
	if c.ControlledPages() {
		wrap("pages::route_controls", c.Pages.ControlsUse("rate_limit"))
	}
	if c.ControlledPicks() {
		wrap("references::route_controls", c.References.PicksUse("rate_limit"))
	}
	return expr
}

// LayersTakeLimits reports whether a layer `<service>(...)` adds takes the
// app's registry of limiters, so the setup keeps a clone of it.
func (c *crate) LayersTakeLimits() bool {
	return c.ControlsUse("rate_limit") || c.ControlledPages() && c.Pages.ControlsUse("rate_limit") ||
		c.ControlledPicks() && c.References.PicksUse("rate_limit")
}

// ControlledPicks reports whether a picker's shard meets its options
// operation's rate limit or timeout (D61, amended), so `<service>(...)`
// adds the layers that apply them (`references::route_controls`).
func (c *crate) ControlledPicks() bool {
	return c.References != nil && len(c.References.PickControlled()) > 0
}

// ControlsUse reports whether a procedure's route has the control
// ("rate_limit", "body_limit" or "timeout"), so procedures.rs declares
// ProcedureControls' builder method for it, and, for a rate limit, its
// route_controls takes the app's registry of limiters.
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
	if c.HasRecords() {
		files = append(files,
			struct{ template, path string }{"records.tmpl", filepath.Join("src", "records.rs")},
			struct{ template, path string }{"wire.tmpl", filepath.Join("src", "wire.rs")},
		)
	}
	if c.HasChoices() {
		files = append(files, struct{ template, path string }{"choices.tmpl", filepath.Join("src", "choices.rs")})
	}
	if c.HasForms() {
		files = append(files, struct{ template, path string }{"forms.tmpl", filepath.Join("src", "forms.rs")})
	}
	if len(c.Procedures) > 0 {
		files = append(files, struct{ template, path string }{"procedures.tmpl", filepath.Join("src", "procedures.rs")})
	}
	if c.HasViews() {
		files = append(files, struct{ template, path string }{"views.tmpl", filepath.Join("src", "views.rs")})
	}
	if c.References != nil {
		files = append(files, struct{ template, path string }{"references.tmpl", filepath.Join("src", "references.rs")})
	}
	if c.Pages != nil {
		files = append(files, struct{ template, path string }{"pages.tmpl", filepath.Join("src", "pages.rs")})
	}
	if c.Engine != nil {
		files = append(files, struct{ template, path string }{"engine.tmpl", filepath.Join("src", "engine.rs")})
	}
	if c.EnginePages != nil {
		files = append(files, struct{ template, path string }{"engine_pages.tmpl", filepath.Join("src", "engine_pages.rs")})
	}
	if c.EnginePanels != nil {
		files = append(files, struct{ template, path string }{"engine_panels.tmpl", filepath.Join("src", "engine_panels.rs")})
	}
	if c.IdentityPages != nil {
		files = append(files, struct{ template, path string }{"identity_pages.tmpl", filepath.Join("src", "identity_pages.rs")})
	}
	tmpl, err := parseTemplates()
	if err != nil {
		return err
	}
	// A crate written before with records or forms keeps no stale module,
	// arg_forms.rs among them, which argument forms were once written to.
	for _, stale := range []string{"records.rs", "wire.rs", "choices.rs", "forms.rs", "arg_forms.rs", "procedures.rs", "views.rs", "references.rs", "pages.rs", "engine.rs", "engine_pages.rs", "engine_panels.rs", "identity_pages.rs"} {
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

// parseTemplates parses the crate's templates with the functions they
// call.
func parseTemplates() (*template.Template, error) {
	funcs := template.FuncMap{
		"engineMount":  func() string { return EngineMount },
		"rustString":   rustString,
		"escapeBraces": escapeBraces,
		"join":         strings.Join,
		"doc":          doc,
		"doc4":         doc4,
		"lowerFirst":   lowerFirst,
	}
	for name, fn := range pageFuncs {
		funcs[name] = fn
	}
	return template.New("topcoat").Funcs(funcs).ParseFS(templates, "templates/*.tmpl")
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

// doc4 is doc for an item indented by four spaces: each line after the
// first, which the template indents, starts with them, and every line
// stays within 80 columns.
func doc4(text string) string {
	var lines []string
	line := "///"
	for _, word := range strings.Fields(text) {
		if line != "///" && 4+len(line)+1+len(word) > 80 {
			lines = append(lines, line)
			line = "///"
		}
		line += " " + word
	}
	return strings.Join(append(lines, line), "\n    ")
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
