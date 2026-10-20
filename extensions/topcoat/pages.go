package topcoat

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	ir "github.com/parable-work/superschematic/ir"
)

// PagesConfig is outputs.topcoat.pages: whether the crate has a page per
// resource its operations describe, the path they are served under, and
// whether a list's filter updates its table in place, through a shard, for
// an app that loads Topcoat's runtime (Live).
type PagesConfig struct {
	Enabled bool   `json:"enabled"`
	Prefix  string `json:"prefix,omitempty"`
	Live    bool   `json:"live,omitempty"`
}

// DefaultPagesPrefix is where the pages are served when the config names
// no prefix.
const DefaultPagesPrefix = "/console"

// CheckPagesPrefix refuses a prefix the pages cannot be served under: one
// that does not start with a slash or ends with one, holds a parameter or
// a segment a page path cannot spell, or is, or is under, the mounted JSON
// API's path or the procedures'.
func CheckPagesPrefix(prefix string) error {
	switch {
	case !strings.HasPrefix(prefix, "/"):
		return fmt.Errorf("the pages' prefix %q must start with /", prefix)
	case strings.HasSuffix(prefix, "/"):
		return fmt.Errorf("the pages' prefix %q must not end with /", prefix)
	case strings.ContainsAny(prefix, "{}"):
		return fmt.Errorf("the pages' prefix %q must hold no parameter", prefix)
	}
	for _, reserved := range []string{"/api", "/_superschematic"} {
		if under(prefix, reserved) {
			return fmt.Errorf("the pages' prefix %q is under %s, which the crate serves itself", prefix, reserved)
		}
	}
	for _, segment := range strings.Split(prefix[1:], "/") {
		if !pageWord(segment) {
			return fmt.Errorf("the pages' prefix %q holds %q, which is no page path's segment (letters, digits and -._~)", prefix, segment)
		}
	}
	return nil
}

// pagesModel is what pages.tmpl renders: a page per resource, under a
// prefix, with a layout, a nav and an index.
type pagesModel struct {
	Prefix string
	// Title is the site's title when the app's Settings give none: the
	// service's name in words.
	Title string
	// Params are the page routes' parameters, each a path_param!.
	Params []routeParam
	// Resources are every collection with a page, parents first; Top
	// those no item holds that have a list, which the nav and the index
	// link.
	Resources []*pageResource
	Top       []*pageResource
	// Ops are the operations the pages call, with their call and submit
	// helpers; Prefills the functions that fill an edit form from an
	// item.
	Ops      []*pageOp
	Prefills []*prefill
	// Tree is the TREE constant the trail walks, as Rust.
	Tree string
	// LeftOut are the operations without a page, and why.
	LeftOut []leftOut
	// Controls are the route controls of each operation a page calls
	// whose route has one: the operation it is for, and the options
	// operations its references call (D61); Controlled each page that
	// meets one, by its method and path, with their places in Controls.
	Controls   []routeControls
	Controlled []controlledPage
	// Trails are the steps of the trail a reference names, an item's by
	// what its route's value refers to when its title does not name it
	// (D44, amended).
	Trails  []*refTrail
	uses    map[string]bool
	imports map[string]map[string]bool
}

// controlledPage is a page whose request meets route controls: its
// method, its path as it is registered, the place in pagesModel.Controls
// of its operation's controls, -1 when its route has none, and those of
// the other operations it calls whose routes have one.
type controlledPage struct {
	Method string
	Path   string
	Own    int
	Called []int
}

// OwnExpr is the Rust Option of the place of the page's operation's
// controls.
func (p controlledPage) OwnExpr() string {
	if p.Own < 0 {
		return "None"
	}
	return fmt.Sprintf("Some(%d)", p.Own)
}

// CalledExpr is the Rust slice of the places of the controls of the other
// operations it calls.
func (p controlledPage) CalledExpr() string {
	places := make([]string, len(p.Called))
	for i, at := range p.Called {
		places[i] = fmt.Sprintf("%d", at)
	}
	return "&[" + strings.Join(places, ", ") + "]"
}

// Live reports whether a list updates in place through a shard.
func (m *pagesModel) Live() bool {
	for _, r := range m.Resources {
		if r.Live {
			return true
		}
	}
	return false
}

// ControlsUse reports whether the route of an operation a page calls has
// the control ("rate_limit", "body_limit" or "timeout"), so pages.rs
// declares what applies it.
func (m *pagesModel) ControlsUse(control string) bool {
	for _, c := range m.Controls {
		switch {
		case control == "rate_limit" && c.RateLimit > 0,
			control == "body_limit" && c.BodyLimit > 0,
			control == "timeout" && c.Timeout > 0:
			return true
		}
	}
	return false
}

// Uses reports whether pages.rs needs a helper (or an import) named item.
func (m *pagesModel) Uses(item string) bool { return m.uses[item] }

// Imports is the names pages.rs imports from module, sorted and joined.
func (m *pagesModel) Imports(module string) string {
	names := sortedKeys(m.imports[module])
	return strings.Join(names, ", ")
}

func (m *pagesModel) use(items ...string) {
	for _, item := range items {
		m.uses[item] = true
	}
}

func (m *pagesModel) importName(module, name string) {
	if m.imports[module] == nil {
		m.imports[module] = map[string]bool{}
	}
	m.imports[module][name] = true
}

// routeParam is a page route's parameter: its name in the route and the
// path_param! type that reads it.
type routeParam struct {
	Name string // project_id
	Type string // ProjectId
}

// pageResource is one resource's pages.
type pageResource struct {
	node  *resourceNode
	Ident string // projects_tasks
	// Name is the resource's path below the prefix, by which the app's
	// Settings scope choices to its pages: projects/{project_id}/tasks.
	Name string
	// Path is the list's route, ItemPath an item's.
	Path     string
	ItemPath string
	// Live reports whether the list renders through a shard at LivePath,
	// which its filter renders again in place when the app loads
	// Topcoat's runtime.
	Live     bool
	LivePath string
	// LiveSubmit is the script the filter's submit runs: it sends the
	// form's query to the shard, through the hidden input whose input
	// event sets the shard's signal, and puts it in the address bar;
	// LiveSizeSubmit the size form's, which does the same with its own.
	LiveSubmit     string
	LiveSizeSubmit string
	// Prev reports whether a paged list links the page before its own,
	// through the cursors its query's `back` keeps (D44, amended): not when
	// its operation declares an argument named back. Sizes are the page
	// sizes its size form offers whatever the query sends, SizesConst the
	// constant that holds them.
	Prev       bool
	Sizes      []int
	SizesConst string
	// ColParams are the route parameters of the collection, its
	// ancestors' items'; ItemParams those and Item, an item's.
	ColParams  []string
	Item       string
	ItemParams []string
	Words      string // the collection's own words: tasks
	Noun       string
	Plural     string
	// Icon is the glyph the nav and the index show beside the list's link:
	// the @display icon of the list's element type, else of the detail's
	// (D44, amended), empty for none.
	Icon string
	// CreateLabel is the new page's heading and the link to it, and
	// SubmitLabel its button's text: the type's @display createLabel, or
	// Create.
	CreateLabel string
	SubmitLabel string
	Parent      *pageResource
	List        *pageOp
	Detail      *pageOp
	Create      *pageOp
	Update      *pageOp
	Delete      *pageOp
	Actions     []*pageOp
	// Sections are the resources under an item shown on its page.
	Sections []*pageResource
	// IsSection reports whether the parent's item page shows the
	// resource.
	IsSection bool
	// RowRecord and RowTable render the list's rows; LinkExpr is a row's
	// link, from `row`, empty when the rows link nowhere.
	RowRecord string
	RowTable  string
	LinkExpr  string
	// RowLabels and DetailLabels are the references' functions that name
	// what the rows' and the item's records refer to (D61), empty when
	// they refer to nothing.
	RowLabels    string
	DetailLabels string
	// BaseExpr is the list's path, which a paged list's links carry its
	// query after (D60).
	BaseExpr string
	// DetailRecord and DetailView render an item; HeadingExpr is its
	// heading, from `record`, and TitleExpr its title for the trail, an
	// Option<String> from `item`, the detail's result, empty when its type
	// declares no title.
	DetailRecord string
	DetailView   string
	HeadingExpr  string
	TitleExpr    string
	// Key names the collection in the trail's tree: projects/{}/tasks.
	Key string
	// HrefBody is the body of <ident>_href, the collection's path.
	HrefBody string
	// BackExpr is the page a deleted item goes back to, and ItemExpr an
	// item's own page, each a String from the route's values.
	BackExpr string
	ItemExpr string
}

// NeedsHref reports whether pages.rs writes <ident>_href.
func (r *pageResource) NeedsHref() bool {
	return r.Detail != nil || r.Create != nil || r.Delete != nil || r.IsSection || r.List != nil && r.List.Paged
}

// Filters reports whether the list's page renders a filter form: its
// argument form shows a control. A paged list without a filter has an
// argument form for its page alone, and renders none.
func (r *pageResource) Filters() bool {
	return r.List != nil && r.List.ArgForm != nil && r.List.ArgForm.Shown()
}

// Linked reports whether the list's rows link to their items' pages.
func (r *pageResource) Linked() bool { return r.LinkExpr != "" }

// NounLower is the noun as a sentence's word: Project as project.
func (r *pageResource) NounLower() string { return lowerFirst(r.Noun) }

// PluralLower is the plural as a sentence's word.
func (r *pageResource) PluralLower() string { return lowerFirst(r.Plural) }

// pageOp is an operation a page calls, and how.
type pageOp struct {
	c        *classified
	Kind     string
	Snake    string // project_archive_project
	ArgsName string // ProjectArchiveProjectArgs
	Output   string // the call's result type
	// Params are the page route parameters its path arguments come from,
	// in its path's order.
	Params []string
	// ArgForm is the argument form a page submits it through; InputForm
	// the input form, for an operation whose only argument is its input;
	// neither for a list or a detail that takes its path's arguments
	// alone, or none.
	ArgForm       *argForm
	InputForm     *form
	InputOptional bool
	// RouteFields are its path arguments: each a form's field when it has
	// an argument form, else an Args struct's.
	RouteFields []routeField
	// Call reports whether pages.rs writes call_<op>, which a list or a
	// detail calls with the route's values; Memoized whether it keeps its
	// result for the request (a detail's, which the trail reads too).
	Call     bool
	Memoized bool
	// DoneBinding and DoneLines are what a post's page does with the
	// operation's result: the redirect (303) that ends it.
	DoneBinding string
	DoneLines   string
	// Verb, Label and Field are an action's word, button text and the
	// item component's prop of its form; PrefillFn fills an edit form.
	Verb      string
	Label     string
	Field     string
	PrefillFn string
	// Paged marks a @paginated list (D60): its call returns a page, whose
	// items, Items, are the rows.
	Paged bool
	Items string
	// ChoicesFn is the references' function that gives the form the page
	// renders its options (D61), empty when it refers to nothing.
	ChoicesFn string
}

// ChoicesExpr is the Rust of the choices the operation's form takes on
// the pages of resource: the app's, and each reference's options under
// them.
func (o *pageOp) ChoicesExpr(resource string) string {
	app := "choices_of(cx, " + rustString(resource) + ")"
	if o.ChoicesFn == "" {
		return app
	}
	return "references::" + o.ChoicesFn + "(cx, " + app + ").await"
}

// routeField is a path argument of an operation: the page route's
// parameter it comes from, the field of the form or the Args struct it
// fills, the API's name for it, and the reader of a value an Args struct
// is built from.
type routeField struct {
	Param    string
	Field    string
	JSONName string
	Read     string
}

// FormName is the form a page submits the operation through.
func (o *pageOp) FormName() string {
	if o.ArgForm != nil {
		return o.ArgForm.Name
	}
	if o.InputForm != nil {
		return o.InputForm.Name
	}
	return ""
}

// FormComponent renders the form's controls.
func (o *pageOp) FormComponent() string {
	if o.ArgForm != nil {
		return o.ArgForm.Component
	}
	return o.InputForm.Component
}

// Rows reports whether the form holds rows, which a row button adds or
// removes without the operation.
func (o *pageOp) Rows() bool {
	if o.ArgForm != nil {
		return o.ArgForm.Rows()
	}
	return o.InputForm != nil && o.InputForm.Rows
}

// NewForm is the Rust of the operation's new form, its path arguments the
// route's values: from &str parameters, and from String locals (owned).
func (o *pageOp) NewForm() string      { return o.newForm(false) }
func (o *pageOp) NewFormOwned() string { return o.newForm(true) }

func (o *pageOp) newForm(owned bool) string {
	if o.ArgForm == nil {
		return o.InputForm.Name + "::new()"
	}
	var args []string
	for _, field := range o.ArgForm.PathFields() {
		param := o.paramOf(field.JSONName)
		if owned {
			param += ".as_str()"
		}
		args = append(args, param)
	}
	return o.ArgForm.Name + "::new(" + strings.Join(args, ", ") + ")"
}

// paramOf is the route parameter the path argument name comes from.
func (o *pageOp) paramOf(name string) string {
	for _, field := range o.RouteFields {
		if field.JSONName == name {
			return field.Param
		}
	}
	return name
}

// MutForm reports whether submit_<op> changes the form it is given: the
// route's values or a row button.
func (o *pageOp) MutForm() bool { return len(o.RouteFields) > 0 && o.ArgForm != nil || o.Rows() }

// RouteNames are the API's names of its path arguments, as a Rust slice's
// items.
func (o *pageOp) RouteNames() string {
	names := make([]string, len(o.RouteFields))
	for i, field := range o.RouteFields {
		names[i] = rustString(field.JSONName)
	}
	return strings.Join(names, ", ")
}

// prefill is a function that fills a form struct from an item's JSON:
// each field the struct and the item's type share by JSON name and kind.
type prefill struct {
	Name     string
	Struct   string
	TypeName string
	Lines    []string
}

// pageSite is the resources the operations a page calls describe, which
// the pages and the trail's references read: the forms the pages submit
// through, the collections no item holds, with their children, and the
// operations left out, with why.
type pageSite struct {
	forms pageForms
	roots []*resourceNode
	out   []leftOut
}

// siteOf classifies the operations of inProcess into the crate's
// resources, through its forms.
func siteOf(c *crate, schemas schemaSet, inProcess []declared) *pageSite {
	forms := pageForms{args: map[string]*argForm{}, inputs: map[string]*form{}}
	for i := range c.ArgForms {
		forms.args[c.ArgForms[i].ArgsName] = &c.ArgForms[i]
	}
	for i := range c.Forms {
		forms.inputs[c.Forms[i].TypeName] = &c.Forms[i]
	}
	roots, out := classify(schemas, inProcess, forms)
	return &pageSite{forms: forms, roots: roots, out: out}
}

// pagesOf builds the pages of the site's resources, under cfg's prefix,
// from the crate's records, views and forms. It logs each operation left
// out, and is nil when no resource has a page.
func pagesOf(c *crate, schemas schemaSet, site *pageSite, prefix, title string, live bool, logf func(format string, args ...any)) (*pagesModel, error) {
	forms, roots, out := site.forms, site.roots, site.out
	m := &pagesModel{
		Prefix:  prefix,
		Title:   title,
		uses:    map[string]bool{},
		imports: map[string]map[string]bool{},
		LeftOut: out,
	}
	for _, left := range out {
		logf("  - topcoat: no page for %s: %s\n", left.Operation, left.Reason)
	}
	if c.References != nil && len(c.References.Trails) > 0 {
		m.Trails = c.References.Trails
		m.use("trailReference", "references")
	}
	b := &pageBuilder{
		m:        m,
		refs:     c.References,
		schemas:  schemas,
		forms:    forms,
		records:  map[string]record{},
		views:    map[string]view{},
		params:   map[string]bool{},
		idents:   map[string]bool{pagerIdent: true},
		prefills: map[string]*prefill{},
		live:     live,
		routes:   map[string]bool{},
		logf:     logf,
	}
	var taken func(nodes []*resourceNode)
	taken = func(nodes []*resourceNode) {
		for _, node := range nodes {
			for _, r := range node.routes() {
				b.routes[r.route] = true
			}
			taken(node.children)
		}
	}
	taken(roots)
	for _, rec := range c.Records {
		b.records[rec.TypeName] = rec
	}
	for _, v := range c.Views {
		b.views[v.TypeName] = v
	}
	for _, root := range roots {
		if err := b.resource(root, nil); err != nil {
			return nil, err
		}
	}
	if len(m.Resources) == 0 {
		logf("  - topcoat: no pages: no operation a page calls describes a resource\n")
		return nil, nil
	}
	// The nav and the index link the lists no item holds: a list under an
	// item, even of a collection without pages, needs the item's id.
	for _, r := range m.Resources {
		if len(r.ColParams) == 0 && r.List != nil {
			m.Top = append(m.Top, r)
			if r.Icon != "" {
				m.use("icons")
			}
		}
	}
	for _, name := range sortedKeys(b.params) {
		m.Params = append(m.Params, routeParam{Name: name, Type: pascalCase(name)})
	}
	for _, name := range sortedKeys(b.prefills) {
		m.Prefills = append(m.Prefills, b.prefills[name])
	}
	m.Tree = b.tree(roots, 0)
	b.controls()
	b.finish()
	return m, nil
}

// controls records, for each page whose request meets a traffic control,
// the page's method and path, the controls of the operation it is for and
// those of the other operations it calls. The operation a page is for: a
// list's page, and its shard, the list's; an item's page, and the edit and
// delete pages, which show the item, the detail's; a post the operation it
// submits; the new page and the index none. A form or a record of the page
// that holds a reference calls its options operation (D61), listing or
// looking up its target, so the page meets that operation's controls too:
// the new page its form's, a list's page its filter's and its rows', an
// item's page its detail's, its actions' forms' and its sections' rows',
// the edit page its form's, the delete page its form's and the item's, and
// a post those of the page it renders again. Not the lists the sections of
// an item's page call, which keep to their own pages.
func (b *pageBuilder) controls() {
	m := b.m
	at := map[string]int{}
	place := func(snake string, controls routeControls) int {
		if !controls.HasControls() {
			return -1
		}
		i, ok := at[snake]
		if !ok {
			i = len(m.Controls)
			at[snake] = i
			m.Controls = append(m.Controls, controls)
		}
		return i
	}
	// add records a page, o the operation it is for, or nil, and refs the
	// choices and labels functions it calls.
	add := func(method, path string, o *pageOp, refs ...string) {
		page := controlledPage{Method: method, Path: path, Own: -1}
		if o != nil {
			page.Own = place(o.Snake, controlsOf(o.c.d.endpoint))
		}
		seen := map[int]bool{page.Own: true}
		for _, fn := range refs {
			for _, options := range b.refs.OptionsCalledBy(fn) {
				if i := place(options.Snake, options.controls); i >= 0 && !seen[i] {
					seen[i] = true
					page.Called = append(page.Called, i)
				}
			}
		}
		if page.Own >= 0 || len(page.Called) > 0 {
			m.Controlled = append(m.Controlled, page)
		}
	}
	choices := func(o *pageOp) string {
		if o == nil {
			return ""
		}
		return o.ChoicesFn
	}
	for _, r := range m.Resources {
		// The trail of a page under the collection names its ancestors'
		// items by reference, and one under an item the item too (D44,
		// amended); a shard renders no trail.
		chain := r.node.chain()
		above, through := b.trailLabels(chain[:len(chain)-1]), b.trailLabels(chain)
		list := []string{choices(r.List), r.RowLabels}
		item := []string{r.DetailLabels}
		for _, o := range r.Actions {
			item = append(item, o.ChoicesFn)
		}
		for _, s := range r.Sections {
			item = append(item, s.RowLabels)
		}
		if r.List != nil {
			add("GET", r.Path, r.List, slices.Concat(list, above)...)
			if r.Live {
				add("POST", r.LivePath, r.List, list...)
			}
		}
		if r.Detail != nil {
			add("GET", r.ItemPath, r.Detail, slices.Concat(item, through)...)
		}
		if r.Create != nil {
			add("GET", r.Path+"/new", nil, slices.Concat([]string{r.Create.ChoicesFn}, above)...)
			add("POST", r.Path+"/new", r.Create, slices.Concat([]string{r.Create.ChoicesFn}, above)...)
		}
		if r.Update != nil {
			add("GET", r.ItemPath+"/edit", r.Detail, slices.Concat([]string{r.Update.ChoicesFn}, through)...)
			add("POST", r.ItemPath+"/edit", r.Update, slices.Concat([]string{r.Update.ChoicesFn}, through)...)
		}
		if r.Delete != nil {
			add("GET", r.ItemPath+"/delete", r.Detail, slices.Concat([]string{r.DetailLabels, r.Delete.ChoicesFn}, through)...)
			add("POST", r.ItemPath+"/delete", r.Delete, slices.Concat([]string{r.DetailLabels, r.Delete.ChoicesFn}, through)...)
		}
		for _, o := range r.Actions {
			add("POST", r.ItemPath+"/"+o.Verb, o, slices.Concat(item, through)...)
		}
	}
	if len(m.Controlled) > 0 {
		m.use("controls")
	}
}

// trailLabels are the label functions that name the items of nodes in
// the trail, each collection's whose item a reference names.
func (b *pageBuilder) trailLabels(nodes []*resourceNode) []string {
	var fns []string
	for _, node := range nodes {
		if fn := b.refs.TrailLabel(node.key); fn != "" {
			fns = append(fns, fn)
		}
	}
	return fns
}

// pagerIdent is the ident no resource takes: its <ident>_href would be
// pager_href, the pager's own helper, beside pager and pager_query. A
// collection at pager is pager_2, as a second one of an ident is.
const pagerIdent = "pager"

// pageBuilder builds each resource's pages.
type pageBuilder struct {
	m        *pagesModel
	refs     *referencesModel
	schemas  schemaSet
	forms    pageForms
	records  map[string]record
	views    map[string]view
	params   map[string]bool
	idents   map[string]bool
	prefills map[string]*prefill
	// live reports whether a list with a filter renders through a shard;
	// routes are the routes the pages serve, "GET projects/{}", which a
	// shard's path must not be.
	live   bool
	routes map[string]bool
	logf   func(format string, args ...any)
}

// resource adds the pages of node, then of the collections under its
// items. A collection that is no resource, only a step of the path to one
// under it, has no page of its own. parent is the resource of the
// collection above node, or nil.
func (b *pageBuilder) resource(node *resourceNode, parent *pageResource) error {
	chain := node.chain()
	var colParams []string
	path := b.m.Prefix
	format := b.m.Prefix
	for i, step := range chain {
		path += "/" + strings.Join(step.words, "/")
		format += "/" + strings.Join(step.words, "/")
		if i < len(chain)-1 {
			path += "/{" + step.param + "}"
			format += "/{}"
			colParams = append(colParams, step.param)
		}
	}
	var r *pageResource
	if node.isResource() {
		ident := snakeWord(strings.Join(collectionWords(node), "_"))
		base := ident
		for i := 2; b.idents[ident]; i++ {
			ident = fmt.Sprintf("%s_%d", base, i)
		}
		b.idents[ident] = true
		r = &pageResource{
			node:       node,
			Ident:      ident,
			Name:       strings.TrimPrefix(path, b.m.Prefix+"/"),
			Path:       path,
			ItemPath:   path + "/{" + node.param + "}",
			ColParams:  colParams,
			Item:       node.param,
			ItemParams: append(append([]string(nil), colParams...), node.param),
			Words:      strings.Join(node.words, "/"),
			Parent:     parent,
		}
		if len(colParams) == 0 {
			r.HrefBody = "Some(" + rustString(path) + ".to_owned())"
		} else {
			args := make([]string, len(colParams))
			for i, param := range colParams {
				args[i] = "seg(" + param + ")?"
			}
			r.HrefBody = "Some(format!(" + rustString(format) + ", " + strings.Join(args, ", ") + "))"
			b.m.use("seg")
		}
		if err := b.fill(r); err != nil {
			return err
		}
		b.liveList(r)
		// A route holds the collection's parameters, and an item's page,
		// which only a detail gives, its own.
		for _, param := range r.ColParams {
			b.params[param] = true
		}
		if r.Detail != nil {
			b.params[r.Item] = true
		}
		b.m.Resources = append(b.m.Resources, r)
	}
	// A collection's parent is the resource of the collection right above
	// it, none for one under a collection without pages: an item's page
	// shows only the resources whose routes its own parameters fill.
	for _, child := range node.children {
		if err := b.resource(child, r); err != nil {
			return err
		}
	}
	return nil
}

// stepTitle is a collection's title from its path: the words after the
// collection whose path it extends (`Drafts` for `documents/drafts`), else
// its last word, as the path gives them.
func stepTitle(node *resourceNode) string {
	if len(node.extends) > 0 {
		return humanize(strings.Join(node.words[len(node.extends):], " "))
	}
	return humanize(node.words[len(node.words)-1])
}

// liveSegment is the segment under the prefix of the shards' paths:
// <prefix>/_live/<resource>.
const liveSegment = "_live"

// liveList makes a list with a filter, or one that pages, render through a
// shard, when the pages' lists are live: the shard's path, under the
// prefix, is its own, unless a page serves it, which leaves the list as it
// is, with the reason in the build log. A list whose argument form holds
// its route's values alone, and does not page, has nothing that would set
// the shard's query.
func (b *pageBuilder) liveList(r *pageResource) {
	if !b.live || r.List == nil || r.List.ArgForm == nil || !r.Filters() && !r.List.Paged {
		return
	}
	route := "POST " + liveSegment + "/" + r.Ident
	if b.routes[route] {
		b.logf("  - topcoat: %s's list is not live: its shard's path, %s/%s/%s, is a page's\n", r.node.key, b.m.Prefix, liveSegment, r.Ident)
		return
	}
	b.routes[route] = true
	r.Live, r.LivePath = true, b.m.Prefix+"/"+liveSegment+"/"+r.Ident
	r.LiveSubmit = liveSubmit(`form[data-ss-live="` + r.Ident + `"]`)
	if r.List.Paged {
		r.LiveSizeSubmit = liveSubmit(`form[data-ss-live-size="` + r.Ident + `"]`)
	}
}

// liveSubmit is the script a live list's form runs on submit, after
// Topcoat's handler stops the browser's own, for the form selector finds:
// the filter (data-ss-live) or a paged list's size form (data-ss-live-size).
// The form's query, as the browser would send it, is written to the form's
// hidden input, whose input event sets the shard's signal, which renders
// the list again, and put in the address bar, so a reload or a link keeps
// the filter and the size.
func liveSubmit(selector string) string {
	return `(() => { const form = document.querySelector('` + selector + `'); ` +
		`const query = new URLSearchParams(new FormData(form)).toString(); ` +
		`history.replaceState(history.state, '', query === '' ? location.pathname : '?' + query); ` +
		`const sent = form.querySelector('input[data-ss-query]'); sent.value = query; sent.dispatchEvent(new Event('input')); })()`
}

// backParam is the query parameter in which a paged list's page keeps the
// cursors of the pages before it (D44, amended).
const backParam = "back"

// pageSizeSteps are the page sizes a size form offers below a route's
// maxLimit, beside its defaultLimit and maxLimit (D44, amended).
var pageSizeSteps = []int{10, 20, 50, 100, 200, 500, 1000}

// pager sets how a paged list's pages link one another: the page before
// its own unless its operation declares an argument named back, which the
// build log says, and the sizes its size form offers.
func (b *pageBuilder) pager(r *pageResource) {
	e := r.List.c.d.endpoint
	r.Prev = true
	for _, arg := range r.List.c.d.op.Arguments {
		if arg.Name == backParam {
			r.Prev = false
			b.logf("  - topcoat: no previous page for %s: %s declares an argument named %s, the query parameter the console keeps the earlier pages' cursors in\n", r.node.key, r.List.c.name(), backParam)
		}
	}
	r.Sizes = pageSizes(e.Page.DefaultLimit, e.Page.MaxLimit)
	r.SizesConst = strings.ToUpper(r.Ident) + "_PAGE_SIZES"
	b.m.use("pageSizes")
	if r.Prev {
		b.m.use("back")
	}
}

// pageSizes are the sizes a paged list's size form offers whatever the
// query sends, in order and each once: the route's defaultLimit and
// maxLimit, and each of pageSizeSteps below maxLimit. The page adds the
// limit sent when the route admits it.
func pageSizes(defaultLimit, maxLimit int) []int {
	sizes := []int{defaultLimit, maxLimit}
	for _, size := range pageSizeSteps {
		if size < maxLimit {
			sizes = append(sizes, size)
		}
	}
	slices.Sort(sizes)
	return slices.Compact(sizes)
}

// SizesExpr is the Rust slice of the list's page sizes.
func (r *pageResource) SizesExpr() string {
	sizes := make([]string, len(r.Sizes))
	for i, size := range r.Sizes {
		sizes[i] = fmt.Sprintf("%d", size)
	}
	return "&[" + strings.Join(sizes, ", ") + "]"
}

// SizesText names the list's page sizes for a doc: 2 and 5.
func (r *pageResource) SizesText() string {
	sizes := make([]string, len(r.Sizes))
	for i, size := range r.Sizes {
		sizes[i] = fmt.Sprintf("%d", size)
	}
	if len(sizes) < 2 {
		return strings.Join(sizes, "")
	}
	return strings.Join(sizes[:len(sizes)-1], ", ") + " and " + sizes[len(sizes)-1]
}

// collectionWords are the words of each collection on the path to node.
func collectionWords(node *resourceNode) []string {
	var words []string
	for _, step := range node.chain() {
		words = append(words, step.words...)
	}
	return words
}

// fill sets a resource's labels, its operations and how its pages render.
func (b *pageBuilder) fill(r *pageResource) error {
	node := r.node
	itemType, rowType := "", ""
	if node.detail != nil {
		itemType = node.detail.d.op.TypeRef.Name
	}
	if node.list != nil {
		rowType = node.list.d.op.TypeRef.Name
	}
	// An item is named by its type's @display noun, else by the type's
	// name in words, as a display component names an untitled row; the
	// collection by the type's plural, else by its path's last word as
	// the path gives it. Neither guesses a word's singular, which English
	// rules get wrong (shelves is no shelve). A collection whose path
	// extends another's is titled by its own words.
	typeName := itemType
	if typeName == "" {
		typeName = rowType
	}
	r.Plural, r.Noun = stepTitle(node), humanize(typeName)
	createLabel := ""
	for _, name := range []string{rowType, itemType} {
		if name == "" {
			continue
		}
		display := b.schemas.typeDisplay(name)
		if display.Noun != "" {
			r.Noun = display.Noun
		}
		if display.Plural != "" && len(node.extends) == 0 {
			r.Plural = display.Plural
		}
		if display.CreateLabel != "" {
			createLabel = display.CreateLabel
		}
		if display.Icon != "" && r.Icon == "" {
			r.Icon = display.Icon
		}
	}
	r.CreateLabel, r.SubmitLabel = createLabel, createLabel
	if createLabel == "" {
		r.CreateLabel, r.SubmitLabel = "New "+r.NounLower(), "Create"
	}
	var err error
	op := func(c *classified) *pageOp {
		if c == nil || err != nil {
			return nil
		}
		var o *pageOp
		o, err = b.op(c, r)
		return o
	}
	r.List, r.Detail, r.Create, r.Update, r.Delete = op(node.list), op(node.detail), op(node.create), op(node.update), op(node.del)
	fields := map[string]bool{}
	for _, action := range node.actions {
		o := op(action)
		if o == nil {
			continue
		}
		field := snakeWord(o.Verb) + "_form"
		for i := 2; fields[field]; i++ {
			field = fmt.Sprintf("%s_form_%d", snakeWord(o.Verb), i)
		}
		fields[field] = true
		o.Field = field
		r.Actions = append(r.Actions, o)
	}
	if err != nil {
		return err
	}
	if r.Parent != nil && r.Parent.Detail != nil && (r.List != nil || r.Create != nil) {
		r.IsSection = true
		r.Parent.Sections = append(r.Parent.Sections, r)
	}
	r.BackExpr = "PREFIX.to_owned()"
	switch {
	case r.List != nil:
		r.BackExpr = r.Ident + "_href(" + pass(r.ColParams, "&") + ").unwrap_or_else(|| PREFIX.to_owned())"
	case r.Parent != nil && r.Parent.Detail != nil:
		r.BackExpr = r.Parent.Ident + "_item_href(" + pass(r.Parent.ItemParams, "&") + ").unwrap_or_else(|| PREFIX.to_owned())"
	}
	if r.List != nil {
		rec, ok := b.records[rowType]
		v, viewed := b.views[rowType]
		if !ok || !viewed {
			return fmt.Errorf("topcoat: pages: the list of %s returns %s, which has no record or no table", r.Words, rowType)
		}
		r.RowRecord, r.RowTable = rec.Name, v.Table
		b.m.importName("records", rec.Name)
		b.m.importName("views", v.Table)
		if r.Detail != nil && rowType == itemType {
			r.LinkExpr = b.linkExpr(r, rec)
		}
		r.RowLabels = b.refs.RecordLabels(rec.Name)
		if r.List.Paged {
			r.BaseExpr = r.Ident + "_href(" + pass(r.ColParams, "&") + ").unwrap_or_default()"
			b.pager(r)
		}
	}
	if r.Detail != nil {
		rec, ok := b.records[itemType]
		v, viewed := b.views[itemType]
		if !ok || !viewed {
			return fmt.Errorf("topcoat: pages: the detail of %s returns %s, which has no record or no detail", r.Words, itemType)
		}
		r.DetailRecord, r.DetailView = rec.Name, v.Detail
		r.DetailLabels = b.refs.RecordLabels(rec.Name)
		b.m.importName("records", rec.Name)
		b.m.importName("views", v.Detail)
		r.HeadingExpr = rustString(r.Noun) + ".to_owned()"
		if typeDef := b.schemas.objectType(itemType); typeDef != nil {
			if title := titleField(typeDef, rec); title != nil {
				text := "&record." + title.Name
				if title.Optional {
					text = "record." + title.Name + ".as_deref().unwrap_or_default()"
				}
				r.HeadingExpr = "title_or(" + text + ", " + rustString(r.Noun) + ")"
				b.m.use("titleOr", "itemTitle")
				r.TitleExpr = rec.Name + "::from(item)." + title.Name
				if !title.Optional {
					r.TitleExpr = "Some(" + r.TitleExpr + ")"
				}
			}
		}
	}
	if r.Detail != nil {
		r.ItemExpr = r.Ident + "_item_href(" + pass(r.ItemParams, "&") + ").unwrap_or_else(|| PREFIX.to_owned())"
	}
	r.Key = r.node.key
	redirect := func(to string) string { return "return Err(see_other(" + to + ").into());" }
	if o := r.Create; o != nil {
		o.DoneBinding, o.DoneLines = "_", redirect(r.BackExpr)
		if lines := b.createdLines(r); lines != "" {
			o.DoneBinding, o.DoneLines = "created", lines
		}
	}
	if o := r.Update; o != nil {
		o.DoneBinding, o.DoneLines = "_", redirect(r.ItemExpr)
	}
	if o := r.Delete; o != nil {
		o.DoneBinding, o.DoneLines = "_", redirect(r.BackExpr)
	}
	for _, o := range r.Actions {
		o.DoneBinding, o.DoneLines = "_", redirect("item")
	}
	return nil
}

// linkExpr is the Rust of a list row's link, from `row`: its item's page
// by the field the detail's path parameter names ({id} and id), a text or
// a whole number; empty when the row's type has no such field.
func (b *pageBuilder) linkExpr(r *pageResource, rec record) string {
	field := idField(r.node.detail, rec)
	if field == nil {
		return ""
	}
	href := func(id string) string {
		args := append(prefixed(r.ColParams, "&"), id)
		return r.Ident + "_item_href(" + strings.Join(args, ", ") + ")"
	}
	switch field.Type {
	case "String":
		return href("&row."+field.Name) + ".unwrap_or_default()"
	case "i64":
		return href("&row."+field.Name+".to_string()") + ".unwrap_or_default()"
	case "Option<String>":
		return "row." + field.Name + ".as_deref().map_or_else(String::new, |id| " + href("id") + ".unwrap_or_default())"
	case "Option<i64>":
		return "row." + field.Name + ".map_or_else(String::new, |id| " + href("&id.to_string()") + ".unwrap_or_default())"
	}
	return ""
}

// idField is the record field the detail's path parameter names, by its
// JSON name, when it holds a text or a whole number; else nil. A parameter
// whose argument refers to the detail's own result type (RefersTo, D61)
// names the reference's value field, `id` unsaid: `products/{productId}`
// and `id`.
func idField(detail *classified, rec record) *recordField {
	name := detail.segs[len(detail.segs)-1].param
	for _, arg := range detail.d.op.Arguments {
		if arg.Name == name && arg.References != nil && arg.References.Type == detail.d.op.TypeRef.Name {
			name = arg.References.Value
			if name == "" {
				name = ir.DefaultReferenceValue
			}
		}
	}
	for i, field := range rec.Fields {
		if field.JSONName != name {
			continue
		}
		switch field.Type {
		case "String", "i64", "Option<String>", "Option<i64>":
			return &rec.Fields[i]
		}
	}
	return nil
}

// createdLines turn `created`, the item a create returned, into the page
// it goes to: its own, when its type holds the field the detail's path
// parameter names, else the page a deleted item goes back to.
func (b *pageBuilder) createdLines(r *pageResource) string {
	if r.Detail != nil {
		name := r.Create.c.d.op.TypeRef.Name
		if rec, ok := b.records[name]; ok && !r.Create.c.d.op.TypeRef.IsArray {
			if field := idField(r.node.detail, rec); field != nil && !strings.HasPrefix(field.Type, "Option<") {
				id := "&created." + field.Name
				if field.Type == "i64" {
					id += ".to_string()"
				}
				b.m.importName("records", rec.Name)
				args := append(prefixed(r.ColParams, "&"), id)
				return "let created = " + rec.Name + "::from(created);\n" +
					"return Err(see_other(" + r.Ident + "_item_href(" + strings.Join(args, ", ") + ").unwrap_or_else(|| " + r.BackExpr + ")).into());"
			}
		}
	}
	return ""
}

// op is how a page calls the classified operation c of r.
func (b *pageBuilder) op(c *classified, r *pageResource) (*pageOp, error) {
	e := c.d.endpoint
	info := operationOf(e)
	o := &pageOp{
		c:        c,
		Kind:     c.kind,
		Snake:    info.SnakeName,
		ArgsName: info.ArgsName,
		Output:   info.Output,
		Items:    info.Output,
		Verb:     c.verb,
		Paged:    e.Page != nil,
	}
	if o.Paged {
		o.Items = "Vec<" + e.Page.ItemRustType + ">"
	}
	if strings.Contains(o.Output, "types::") {
		b.m.use("types")
	}
	// Each path argument comes from the page route's parameter at its
	// place: the item parameters of the collections on the way, in order.
	chain := r.node.chain()
	var places []string
	for _, step := range chain {
		places = append(places, step.param)
	}
	position := map[string]int{}
	index := 0
	for _, seg := range c.segs {
		if seg.param != "" {
			position[seg.param] = index
			index++
		}
	}
	for i := 0; i < index; i++ {
		o.Params = append(o.Params, places[i])
	}
	o.ArgForm = b.forms.argsFormOf(c.d)
	if o.ArgForm != nil {
		b.m.importName("forms", o.ArgForm.Name)
		for _, field := range o.ArgForm.PathFields() {
			at, ok := position[field.JSONName]
			if !ok {
				return nil, fmt.Errorf("topcoat: pages: %s.%s: path argument %s is not in its path", e.Namespace, e.Name, field.JSONName)
			}
			o.RouteFields = append(o.RouteFields, routeField{Param: places[at], Field: field.Name, JSONName: field.JSONName})
		}
	} else if e.Input != nil {
		o.InputForm = b.forms.inputFormOf(c.d)
		o.InputOptional = !e.Input.Required
		b.m.importName("forms", o.InputForm.Name)
	} else {
		for _, param := range e.PathArgs {
			at, ok := position[param.Name]
			if !ok {
				return nil, fmt.Errorf("topcoat: pages: %s.%s: path argument %s is not in its path", e.Namespace, e.Name, param.Name)
			}
			read := routeReader(b.schemas, c.d, param.Name)
			o.RouteFields = append(o.RouteFields, routeField{Param: places[at], Field: param.Field, JSONName: param.Name, Read: read})
			b.m.use(read)
		}
	}
	switch c.kind {
	case pageList:
		o.Call = r.Parent != nil && r.Parent.node.detail != nil || o.ArgForm == nil
	case pageDetail:
		o.Call, o.Memoized = true, true
	case pageAction:
		o.Label = humanize(e.Name)
		if c.d.op.Docs != nil && c.d.op.Docs.Title != "" {
			o.Label = c.d.op.Docs.Title
		}
	case pageUpdate:
		input := o.ArgForm.Input
		if input == nil {
			return nil, fmt.Errorf("topcoat: pages: %s.%s updates without an input form", e.Namespace, e.Name)
		}
		o.PrefillFn = b.prefill(input.Struct, r.node.detail.d.op.TypeRef.Name)
	}
	// A page renders the form of every operation it submits; an item's
	// page only submits its detail's, through call_<op>, and renders none,
	// and a paged list without a filter renders none for its page.
	if o.FormName() != "" && c.kind != pageDetail && (o.ArgForm == nil || o.ArgForm.Shown() || c.kind != pageList) {
		b.m.importName("forms", o.FormComponent())
		o.ChoicesFn = b.refs.FormChoices(o.FormName())
	}
	b.m.Ops = append(b.m.Ops, o)
	return o, nil
}

// prefill is the name of the function that fills st from an item of
// typeName, as the item's JSON: each of st's fields whose JSON name a field
// of the item's type has, of the same kind (a value of the same type's
// values, an object, rows of objects or of values, a group of one enum's
// members, JSON text of the same type). A secret field and a field the
// item hides are not filled; any other starts as the new form has it.
func (b *pageBuilder) prefill(st *formStruct, typeName string) string {
	name := "fill_" + componentName(st.Name) + "_from_" + componentName(typeName)
	if _, done := b.prefills[name]; done {
		return name
	}
	p := &prefill{Name: name, Struct: st.Name, TypeName: typeName}
	b.prefills[name] = p
	b.m.importName("forms", st.Name)
	b.m.use("value")
	input := b.schemas.objectType(st.TypeName)
	item := b.schemas.objectType(typeName)
	if input == nil || item == nil {
		return name
	}
	for _, f := range st.Fields {
		in := fieldNamed(input, f.JSONName)
		out := fieldNamed(item, f.JSONName)
		if in == nil || out == nil || in.Secret || out.Secret || out.UIHidden || f.Control == "password" {
			continue
		}
		at := "&json[" + rustString(f.JSONName) + "]"
		switch f.Kind {
		case kindValue:
			if b.valueKind(in.TypeRef) != b.valueKind(out.TypeRef) {
				continue
			}
			// A long text's control is "longtext", held as text; a JSON
			// textarea's holds the value's JSON text.
			read := "held"
			switch f.Control {
			case "checkbox":
				read = "held_checked"
			case "textarea":
				read = "held_json"
			}
			b.m.use(read)
			p.Lines = append(p.Lines, "form."+f.Name+" = "+read+"("+at+");")
		case kindObject:
			ref := out.TypeRef
			if ref.IsArray || ref.IsMap || b.schemas.objectType(ref.Name) == nil {
				continue
			}
			child := b.prefill(f.Child, ref.Name)
			p.Lines = append(p.Lines, "if json["+rustString(f.JSONName)+"].is_object() {", "    form."+f.Name+" = "+child+"("+at+");", "}")
		case kindObjectRows:
			ref := out.TypeRef
			if !ref.IsArray || ref.IsArrayOfArrays || ref.IsMap || b.schemas.objectType(ref.Name) == nil {
				continue
			}
			child := b.prefill(f.Child, ref.Name)
			p.Lines = append(p.Lines, "if let Some(rows) = json["+rustString(f.JSONName)+"].as_array() {", "    form."+f.Name+" = rows.iter().map("+child+").collect();", "}")
		case kindValueRows:
			if b.valueKind(in.TypeRef) != b.valueKind(out.TypeRef) {
				continue
			}
			b.m.use("held")
			p.Lines = append(p.Lines, "if let Some(rows) = json["+rustString(f.JSONName)+"].as_array() {", "    form."+f.Name+" = rows.iter().map(held).collect();", "}")
		case kindGroup:
			if b.valueKind(in.TypeRef) != b.valueKind(out.TypeRef) {
				continue
			}
			p.Lines = append(p.Lines, "if let Some(members) = json["+rustString(f.JSONName)+"].as_array() {", "    form."+f.Name+" = members.iter().filter_map(Value::as_str).map(str::to_owned).collect();", "}")
		}
	}
	return name
}

// valueKind is what a value of ref holds, for matching an input's field to
// an item's: JSON text of its type for a map, a list of lists or a value no
// leaf holds; an enum by its name; else its leaf; each after "list:" for a
// list.
func (b *pageBuilder) valueKind(ref ir.TypeRef) string {
	kind := ""
	switch {
	case ref.IsMap || ref.IsArrayOfArrays:
		kind = fmt.Sprintf("json:%s:%t:%t", ref.Name, ref.IsMap, ref.IsArray)
	case b.schemas.objectType(ref.Name) != nil:
		kind = "object:" + ref.Name
	case b.schemas.enum(ref.Name) != nil:
		kind = "enum:" + ref.Name
	default:
		switch b.schemas.leafOf(ref.Name) {
		case stringLeaf:
			kind = "string"
		case integerLeaf:
			kind = "integer"
		case numberLeaf:
			kind = "number"
		case booleanLeaf:
			kind = "boolean"
		default:
			kind = "json:" + ref.Name
		}
	}
	if ref.IsArray && !ref.IsArrayOfArrays && !ref.IsMap {
		kind = "list:" + kind
	}
	return kind
}

// tree is the TREE constant's entries for nodes, at indent levels of
// nesting.
func (b *pageBuilder) tree(nodes []*resourceNode, level int) string {
	if len(nodes) == 0 {
		return "&[]"
	}
	pad := strings.Repeat("    ", 2*level+1)
	var lines []string
	lines = append(lines, "&[")
	for _, node := range nodes {
		r := b.pageOf(node)
		words := make([]string, len(node.words))
		for i, word := range node.words {
			words[i] = rustString(word)
		}
		// A collection without pages names an item by its id alone: it has
		// no type to name it by.
		plural, noun := stepTitle(node), ""
		list, detail, edit, del := false, false, false, false
		create := "None"
		var actions []string
		if r != nil {
			plural, noun = r.Plural, r.Noun
			list, detail, edit, del = r.List != nil, r.Detail != nil, r.Update != nil, r.Delete != nil
			if r.Create != nil {
				create = "Some(" + rustString(r.CreateLabel) + ")"
			}
			for _, action := range r.Actions {
				actions = append(actions, "("+rustString(action.Verb)+", "+rustString(action.Label)+")")
			}
		}
		lines = append(lines, pad+"Node {")
		// An item's title is read only where a type declares one, and what
		// its route's value refers to only where a reference names it.
		if b.m.uses["itemTitle"] {
			lines = append(lines,
				pad+"    key: "+rustString(node.key)+",",
				pad+fmt.Sprintf("    titled: %t,", r != nil && r.TitleExpr != ""))
		}
		if b.m.uses["trailReference"] {
			reference := "None"
			if b.refs.TrailLabel(node.key) != "" {
				reference = "Some(" + rustString(node.key) + ")"
			}
			lines = append(lines, pad+"    reference: "+reference+",")
		}
		lines = append(lines,
			pad+"    words: &["+strings.Join(words, ", ")+"],",
			pad+"    plural: "+rustString(plural)+",",
			pad+"    noun: "+rustString(noun)+",",
			pad+fmt.Sprintf("    list: %t,", list),
			pad+fmt.Sprintf("    detail: %t,", detail),
			pad+"    create: "+create+",",
			pad+fmt.Sprintf("    edit: %t,", edit),
			pad+fmt.Sprintf("    delete: %t,", del),
			pad+"    actions: &["+strings.Join(actions, ", ")+"],",
			pad+"    children: "+b.tree(node.children, level+1)+",",
			pad+"},",
		)
	}
	lines = append(lines, strings.Repeat("    ", 2*level)+"]")
	return strings.Join(lines, "\n")
}

// pageOf is the pages of node, or nil for a collection with none.
func (b *pageBuilder) pageOf(node *resourceNode) *pageResource {
	for _, r := range b.m.Resources {
		if r.node == node {
			return r
		}
	}
	return nil
}

// finish records what pages.rs uses from the helpers each page needs.
func (b *pageBuilder) finish() {
	m := b.m
	m.importName("forms", "Refusal")
	for _, o := range m.Ops {
		if o.ChoicesFn != "" {
			m.use("references")
		}
		if !o.Call {
			continue
		}
		if o.Paged {
			m.use("runtime")
		}
		if o.Memoized {
			m.use("memoize")
		}
		switch {
		case o.ArgForm != nil:
			m.use("pageRefusal")
		case len(o.RouteFields) > 0:
			m.use("routeValue", "pageRefusal", "value")
		}
	}
	for _, r := range m.Resources {
		if r.Detail != nil {
			m.use("seg")
		}
		if r.List != nil && r.List.ArgForm != nil {
			m.use("form", "pageRefusal")
			if r.Filters() {
				m.use("alone")
			}
		}
		if r.RowLabels != "" || r.DetailLabels != "" {
			m.use("references")
		}
		if r.List != nil && r.List.Paged {
			m.use("pageQuery")
			if !r.Live || r.IsSection {
				m.use("pager")
			}
		}
		posts := append([]*pageOp{r.Create, r.Update, r.Delete}, r.Actions...)
		for _, o := range posts {
			if o == nil {
				continue
			}
			m.use("seeOther", "form", "alone", "pageRefusal", "formErrors")
			if o.Rows() {
				m.use("rowsAgain")
			}
		}
	}
	if m.uses["pageRefusal"] || m.uses["formErrors"] {
		m.importName("forms", "FormErrors")
	}
	// A size form's sizes read the route's page, and a page's history its
	// cursors by the route's rule and its query as a form reads one.
	if m.uses["pageSizes"] {
		m.use("runtime")
	}
	if m.uses["back"] {
		m.use("runtime", "form")
	}
}

// SentLines are the Rust of a post's page from its form, `form`, to
// `sent`, the refusal it renders: the route's values set on the form over
// its hidden inputs, a row button applied, then the form submitted (an
// argument form's `submit`) or parsed and its operation called (an input
// form, with `Refusal::from` and `Refusal::from_api`). The operation's
// result ends the page with DoneLines.
func (o *pageOp) SentLines() string {
	var lines []string
	switch {
	case o.ArgForm != nil && len(o.RouteFields) > 0:
		mut := ""
		if o.Rows() {
			mut = "mut "
		}
		lines = append(lines, "// The route's values, never the hidden inputs, which a reader can change.", "let "+mut+"form = "+o.ArgForm.Name+" {")
		for _, field := range o.RouteFields {
			lines = append(lines, "    "+field.Field+": Some("+field.Param+".clone()),")
		}
		lines = append(lines, "    ..form", "};")
	case o.Rows():
		lines = append(lines, "let mut form = form;")
	}
	done := strings.Split(o.DoneLines, "\n")
	var send []string
	if o.ArgForm != nil {
		send = append(send, "match form.submit(cx).await {", "    Ok("+o.DoneBinding+") => {")
		send = append(send, indentEach(8, done)...)
		send = append(send, "    }", "    Err(refusal) => refusal,", "}")
	} else {
		input := "input"
		if o.InputOptional {
			input = "input: Some(input)"
		}
		send = append(send, "match form.parse() {",
			"    Ok(input) => match operations::"+o.Snake+"(cx, crate::api::"+o.ArgsName+" { "+input+" }).await {",
			"        Ok("+o.DoneBinding+") => {")
		send = append(send, indentEach(12, done)...)
		send = append(send, "        }", "        Err(err) => Refusal::from_api(&err),", "    },", "    Err(errors) => Refusal::from(errors),", "}")
	}
	if o.Rows() {
		lines = append(lines, "let sent = if form.apply_action() {", "    rows_again()", "} else {")
		lines = append(lines, indentEach(4, send)...)
		lines = append(lines, "};")
	} else {
		send[0] = "let sent = " + send[0]
		send[len(send)-1] += ";"
		lines = append(lines, send...)
	}
	return indentLines(4, lines)
}

// pass is params as arguments, each after prefix: &project_id, &task_id.
func pass(params []string, prefix string) string {
	return strings.Join(prefixed(params, prefix), ", ")
}

func prefixed(params []string, prefix string) []string {
	out := make([]string, len(params))
	for i, param := range params {
		out[i] = prefix + param
	}
	return out
}

// lowerFirst is a label as a word inside a sentence: its first letter lower
// case, unless the word is an acronym.
func lowerFirst(label string) string {
	word, _, _ := strings.Cut(label, " ")
	if isAcronym(word) {
		return label
	}
	runes := []rune(label)
	if len(runes) == 0 {
		return label
	}
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

// pageFuncs are the template functions pages.tmpl writes parameters with.
var pageFuncs = map[string]any{
	// declare is params as a function's parameters of typ.
	"declare": func(params []string, typ string) string {
		out := make([]string, len(params))
		for i, param := range params {
			out[i] = param + ": " + typ
		}
		return strings.Join(out, ", ")
	},
	// pass is params as arguments after prefix.
	"pass": pass,
	// passAfter is pass after a comma, or nothing for no params.
	"passAfter": func(params []string, prefix string) string {
		if len(params) == 0 {
			return ""
		}
		return ", " + pass(params, prefix)
	},
	// declareAfter is params as parameters of typ after a comma.
	"declareAfter": func(params []string, typ string) string {
		var b strings.Builder
		for _, param := range params {
			b.WriteString(", " + param + ": " + typ)
		}
		return b.String()
	},
	// props is params as a component's props, each its own local, each
	// followed by a comma and a space.
	"props": func(params []string) string {
		var b strings.Builder
		for _, param := range params {
			b.WriteString(param + ": " + param + ", ")
		}
		return b.String()
	},
	// args is params as a component's props, each its own local, joined
	// by commas.
	"args": func(params []string) string {
		out := make([]string, len(params))
		for i, param := range params {
			out[i] = param + ": " + param
		}
		return strings.Join(out, ", ")
	},
	// cloned is params as props, each a clone of its local.
	"cloned": func(params []string) string {
		out := make([]string, len(params))
		for i, param := range params {
			out[i] = param + ": " + param + ".clone()"
		}
		return strings.Join(out, ", ")
	},
	// reads are the lines that read params from the route.
	"reads": func(params []string) string {
		var b strings.Builder
		for _, param := range params {
			b.WriteString("    let " + param + " = path_param::<" + pascalCase(param) + ">(cx).to_owned();\n")
		}
		return b.String()
	},
}
