package topcoat

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// referencesModel is what references.tmpl renders (D61): for each value a
// form holds or a record shows that refers to an item (RefersTo), the
// options a form offers and the labels a display component shows, from the
// operation that lists the items, as Choices by path.
type referencesModel struct {
	// Options are the options operations the references call, each kept
	// for the request; Lookups their lookups, by argument.
	Options []*refOptions
	Lookups []*refLookup
	// Sources are each target, by the value its references hold and the
	// label that names an item, through its options and lookup.
	Sources []*refSource
	// Forms are the forms that hold a reference, with the choices function
	// a page calls for each; Records the records that show one, with the
	// labels function; Trails the steps of the console's trail that one
	// names, with the label function (D44, amended).
	Forms   []*refForm
	Records []*refRecord
	Trails  []*refTrail
}

// refOptions is an operation that lists a reference's target: one call per
// request, its first page of up to its maxLimit when it pages.
type refOptions struct {
	Fn        string // catalog_list_items_options
	Operation string // catalog.listItems
	Snake     string // catalog_list_items, its in-process call
	// ArgsName is its Args struct, empty for an operation without
	// arguments, and Nones the Args fields the call leaves out: each query
	// argument, none of which is required.
	ArgsName string
	Nones    []string
	Paged    bool
	// Search is its search argument's call (D61, amended), which a picker
	// makes for the text a reader types; nil when it does not search.
	Search *refSearch
	// Hosted is the crate of the API dependency whose operation it is
	// (D61, amended), nil for the service's own, whose call it makes
	// through that crate's operations, under that crate's setup.
	Hosted *hostedCrate
	// controls are its route's, which a page that calls it meets.
	controls routeControls
}

// API is the Rust path of the API crate of the options operation: the
// service's own, or its dependency's through its hosted crate.
func (o *refOptions) API() string {
	if o.Hosted != nil {
		return o.Hosted.Ident + "::api"
	}
	return "crate::api"
}

// Ops is the Rust path of the module of the options operation's in-process
// call.
func (o *refOptions) Ops() string {
	if o.Hosted != nil {
		return o.Hosted.Ident + "::operations"
	}
	return "operations"
}

// PageRequest is the Rust path of the page request a paged options call
// asks for.
func (o *refOptions) PageRequest() string {
	if o.Hosted != nil {
		return o.Hosted.Ident + "::api::runtime::page::PageRequest"
	}
	return "PageRequest"
}

// refSearch is an options operation's search argument (SearchQuery, D62),
// which makes a reference to its target a picker (D61, amended): one call
// per distinct text, kept for the request, of the first page, and at most
// MAX_LOOKUP_CALLS texts a request, which Searched counts for every picker
// whose options it is.
type refSearch struct {
	Fn       string // product_list_products_search
	Searched string // product_list_products_searched
	Arg      string // q
	Field    string // q, its Args field
	// Others are the Args fields the call leaves out.
	Others []string
	// Attrs are the search box's attributes from the argument's rules, as
	// an argument form's search box has them: ` maxlength="50"`.
	Attrs string
}

// refLookup is an options operation's lookup argument, the list query
// argument that narrows the list to the items whose values it holds: one
// call per chunk of values, kept for the request.
type refLookup struct {
	Fn      string // catalog_list_items_by_ids
	Options *refOptions
	Arg     string // ids
	Field   string // ids, its Args field
	// Others are the Args fields the call leaves out, and Read the helper
	// that reads a value's text as the argument's element's JSON.
	Others []string
	Read   string
	// Chunk is how many values one call names: the argument's listMax, or
	// the page's maxLimit when it pages and that is less; 0 for all.
	Chunk int
}

// ChunkExpr is the Rust of how many values one call names.
func (l *refLookup) ChunkExpr() string {
	if l.Chunk > 0 {
		return fmt.Sprintf("%d", l.Chunk)
	}
	return "values.len().max(1)"
}

// refSource is where the items a reference names come from: its target,
// the field its value holds, the field that labels an item, and the
// options and lookup operations.
type refSource struct {
	// Base is the stem of its private functions, <Base>_target_choices
	// and <Base>_target_labels: item_view.
	Base   string
	Target string // ItemView
	// Plural names the target's items in a sentence: its @display plural,
	// else its name in words.
	Plural string
	Value  string // id
	// Label is the field that names an item, empty when none does and an
	// item is named by its value.
	Label   string
	Options *refOptions
	Lookup  *refLookup
	key     string
}

// Picks reports whether a form's reference to the source is a picker
// (D61, amended): its options operation searches.
func (s *refSource) Picks() bool { return s.Options.Search != nil }

// PickPath is the path of the source's pick shard, under the crate's
// procedures': /_superschematic/<service>/_pick/<base>.
func (s *refSource) PickPath(service string) string {
	return "/_superschematic/" + service + "/_pick/" + strings.ReplaceAll(s.Base, "_", "-")
}

// RateLimitExpr is the Rust of the limiter of the source's options
// operation, from the app's registry, `limits`, which a picker's shard
// meets; None for a route without a rate limit.
func (s *refSource) RateLimitExpr() string {
	if s.Options.controls.RateLimit == 0 || s.Options.Hosted != nil {
		return "None"
	}
	return "limits.of(&crate::api::operations::" + s.Options.controls.Const + ")"
}

// RateFromExpr is the Rust of the function that takes the limiter of an
// API dependency's options operation from its crate's setup at a request
// (D61, amended); None for the service's own, or a route without a rate
// limit.
func (s *refSource) RateFromExpr() string {
	if s.Options.controls.RateLimit == 0 || s.Options.Hosted == nil {
		return "None"
	}
	return "Some(" + s.Options.controls.LimiterFrom() + ")"
}

// TimeoutExpr is the Rust Option of the options operation's route's
// timeout, which a picker's shard meets.
func (s *refSource) TimeoutExpr() string {
	if s.Options.controls.Timeout == 0 {
		return "None"
	}
	return fmt.Sprintf("Some(Duration::from_secs(%d))", s.Options.controls.Timeout)
}

// LabelExpr is the Rust Option of the label field's JSON name.
func (s *refSource) LabelExpr() string {
	if s.Label == "" {
		return "None"
	}
	return "Some(" + rustString(s.Label) + ")"
}

// refSite is one place a form or a record holds a reference: its path, as
// Choices names it (`lines.itemId`), its JSON keys from the record's root,
// and its source. A form's site also has the Rust place of its form's
// struct its steps start at, Root, form or form.input, and the steps from
// there to its controls.
type refSite struct {
	Path   string
	Keys   []string
	Source *refSource
	Root   string
	Steps  []siteStep
	// Pick marks a form's site whose source searches (D61, amended): each
	// of its controls is a picker. A query list argument's controls share
	// its name, which no picker's box and select could, so they are a
	// select of the options as any other site's are.
	Pick bool
}

// siteStep is one step from a form's struct to a site's controls: a field,
// by its Rust name and its JSON name, that holds one value or object, or
// rows of them.
type siteStep struct {
	Field string
	Key   string
	Rows  bool
}

// ControlsLines are the Rust lines, at indent levels, that push each of
// the site's controls onto `controls`, its name and the value it holds,
// through the rows on the way, unless the app's choices name the control.
func (s refSite) ControlsLines(indent int) string {
	var lines []string
	pad := func(depth int) string { return strings.Repeat("    ", indent+depth) }
	expr, name, dynamic, depth := s.Root, "", false, 0
	for i, step := range s.Steps {
		expr += "." + step.Field
		if i > 0 {
			name += "."
		}
		name += escapeBraces(step.Key)
		if step.Rows {
			index, row := fmt.Sprintf("i%d", depth), fmt.Sprintf("row%d", depth)
			lines = append(lines, pad(depth)+"for ("+index+", "+row+") in "+expr+".iter().enumerate() {")
			name += "[{" + index + "}]"
			expr, dynamic = row, true
			depth++
		}
	}
	if !dynamic {
		// The site's path is its one control's name, which the caller
		// checked the app's choices do not name.
		plain := strings.ReplaceAll(strings.ReplaceAll(name, "{{", "{"), "}}", "}")
		return pad(0) + "controls.push((" + rustString(plain) + ".to_owned(), " + expr + ".clone()));"
	}
	lines = append(lines,
		pad(depth)+"let name = format!("+rustString(name)+");",
		pad(depth)+"if !named(&choices, &name) {",
		pad(depth)+"    controls.push((name, "+expr+".clone()));",
		pad(depth)+"}")
	for ; depth > 0; depth-- {
		lines = append(lines, pad(depth-1)+"}")
	}
	return strings.Join(lines, "\n")
}

// KeysExpr is the Rust slice of the site's keys.
func (s refSite) KeysExpr() string {
	keys := make([]string, len(s.Keys))
	for i, key := range s.Keys {
		keys[i] = rustString(key)
	}
	return "&[" + strings.Join(keys, ", ") + "]"
}

// refForm is a form that holds a reference: its choices function gives
// each such control its options, and each picker its pick (D61, amended).
// Get marks a filter, sent by GET, whose pickers each search their text;
// a post's searches only the one whose "Find" it pressed.
type refForm struct {
	Fn    string // basket_input_form_choices
	Form  string // BasketInputForm
	Sites []refSite
	Get   bool
}

// ChoiceSites are the form's sites whose source does not search, each a
// select of its options.
func (f *refForm) ChoiceSites() []refSite {
	var out []refSite
	for _, site := range f.Sites {
		if !site.Pick {
			out = append(out, site)
		}
	}
	return out
}

// PickGroups are the form's picker sites by their source, in the order of
// their first site: one call of the source's picks covers every control
// of the group.
func (f *refForm) PickGroups() []refGroup {
	var groups []refGroup
	for _, site := range f.Sites {
		if !site.Pick {
			continue
		}
		placed := false
		for i := range groups {
			if groups[i].Source == site.Source {
				groups[i].Sites = append(groups[i].Sites, site)
				placed = true
			}
		}
		if !placed {
			groups = append(groups, refGroup{Source: site.Source, Sites: []refSite{site}})
		}
	}
	return groups
}

// Picks reports whether a control of the form is a picker.
func (f *refForm) Picks() bool { return len(f.PickGroups()) > 0 }

// refRecord is a record that shows a reference, at any depth: its labels
// function names each value its records hold, a call per source.
type refRecord struct {
	Fn     string // basket_view_labels
	Record string // BasketViewRecord
	Groups []refGroup
}

// refTrail is a step of the console's trail whose route's parameter refers
// to an item (D44, amended): the collection's key, by which the trail
// names it, and the source whose label names the step.
type refTrail struct {
	Key    string
	Source *refSource
}

// Fn is the function a page's trail names the step with:
// <Base>_target_label, the label of one value.
func (t *refTrail) Fn() string { return t.Source.Base + "_target_label" }

// refGroup is the sites of one source in a record.
type refGroup struct {
	Source *refSource
	Sites  []refSite
}

// SitesText names each site for a doc: `lines.itemId` (ItemView).
func (f *refForm) SitesText() string { return sitesText(f.Sites) }

// SitesText names each site for a doc.
func (r *refRecord) SitesText() string {
	var sites []refSite
	for _, g := range r.Groups {
		sites = append(sites, g.Sites...)
	}
	return sitesText(sites)
}

func sitesText(sites []refSite) string {
	parts := make([]string, len(sites))
	for i, site := range sites {
		parts[i] = "`" + site.Path + "` (" + site.Source.Target + ")"
	}
	return strings.Join(parts, ", ")
}

// Paged reports whether an options operation of the service's own pages,
// so references.rs names PageRequest.
func (m *referencesModel) Paged() bool {
	for _, o := range m.Options {
		if o.Paged && o.Hosted == nil {
			return true
		}
	}
	return false
}

// UsesRead reports whether a lookup reads its values with the helper read.
func (m *referencesModel) UsesRead(read string) bool {
	for _, l := range m.Lookups {
		if l.Read == read {
			return true
		}
	}
	return false
}

// RecordNames are the records references.rs names, sorted and joined.
func (m *referencesModel) RecordNames() string {
	names := make([]string, 0, len(m.Records))
	for _, r := range m.Records {
		names = append(names, r.Record)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// UsesChoices, UsesPick and UsesLabels report whether a form's choices
// function gives a source's options, or its picks (D61, amended), or a
// record's labels function or a picker names the values a source's
// reference holds.
func (m *referencesModel) UsesChoices(s *refSource) bool {
	for _, f := range m.Forms {
		for _, site := range f.Sites {
			if site.Source == s && !site.Pick {
				return true
			}
		}
	}
	return false
}

func (m *referencesModel) UsesPick(s *refSource) bool {
	for _, f := range m.Forms {
		for _, site := range f.Sites {
			if site.Source == s && site.Pick {
				return true
			}
		}
	}
	return false
}

func (m *referencesModel) UsesLabels(s *refSource) bool {
	for _, r := range m.Records {
		for _, g := range r.Groups {
			if g.Source == s {
				return true
			}
		}
	}
	return m.UsesTrail(s) || m.UsesPick(s)
}

// Picking reports whether a form's control is a picker, so the crate has
// the picks, their shards and their records.
func (m *referencesModel) Picking() bool {
	if m == nil {
		return false
	}
	for _, s := range m.Sources {
		if m.UsesPick(s) {
			return true
		}
	}
	return false
}

// Searching reports whether an options operation's search is called, by
// a picker.
func (m *referencesModel) Searching(o *refOptions) bool {
	for _, s := range m.Sources {
		if s.Options == o && m.UsesPick(s) {
			return true
		}
	}
	return false
}

// PickControlled are the sources of the pickers whose shards meet their
// options operation's route controls (its rate limit and timeout), each a
// layer on the shard's path.
func (m *referencesModel) PickControlled() []*refSource {
	var out []*refSource
	for _, s := range m.Sources {
		if m.UsesPick(s) && (s.Options.controls.RateLimit > 0 || s.Options.controls.Timeout > 0) {
			out = append(out, s)
		}
	}
	return out
}

// PicksUse reports whether a picker's shard meets the control
// ("rate_limit" or "timeout"): "rate_limit" the rate limit of the
// service's own operation, from the app's registry the setup hands
// route_controls, and "rate_from" an API dependency's, from its crate's
// setup (D61, amended).
func (m *referencesModel) PicksUse(control string) bool {
	for _, s := range m.PickControlled() {
		switch {
		case control == "rate_limit" && s.Options.controls.RateLimit > 0 && s.Options.Hosted == nil,
			control == "rate_from" && s.Options.controls.RateLimit > 0 && s.Options.Hosted != nil,
			control == "timeout" && s.Options.controls.Timeout > 0:
			return true
		}
	}
	return false
}

// UsesTrail reports whether a step of the console's trail is named by a
// source's label, so references.rs writes <Base>_target_label, which
// reads its labels.
func (m *referencesModel) UsesTrail(s *refSource) bool {
	for _, t := range m.Trails {
		if t.Source == s {
			return true
		}
	}
	return false
}

// UsesListing reports whether a source's options listing is called: by a
// form's choices, or by a record's labels without a lookup.
func (m *referencesModel) UsesListing(o *refOptions) bool {
	for _, s := range m.Sources {
		if s.Options == o && (m.UsesChoices(s) || m.UsesLabels(s) && s.Lookup == nil) {
			return true
		}
	}
	return false
}

// Choosing reports whether a form's choices or picks are written, which
// read whether more items follow a listing's.
func (m *referencesModel) Choosing() bool {
	for _, s := range m.Sources {
		if m.UsesChoices(s) || m.UsesPick(s) {
			return true
		}
	}
	return false
}

// Listing reports whether an options operation's listing is called, so
// references.rs declares what holds one.
func (m *referencesModel) Listing() bool {
	for _, o := range m.Options {
		if m.UsesListing(o) || m.Searching(o) {
			return true
		}
	}
	return false
}

// OwnOperations reports whether an options operation is the service's
// own, so references.rs names its operations module.
func (m *referencesModel) OwnOperations() bool {
	for _, o := range m.Options {
		if o.Hosted == nil {
			return true
		}
	}
	return false
}

// FormNames are the forms references.rs names, whose choices functions
// read them, sorted and joined.
func (m *referencesModel) FormNames() string {
	names := make([]string, 0, len(m.Forms))
	for _, f := range m.Forms {
		names = append(names, f.Form)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// UsesLookup reports whether a lookup is called: by a record's labels,
// or a picker's.
func (m *referencesModel) UsesLookup(l *refLookup) bool {
	for _, s := range m.Sources {
		if s.Lookup == l && m.UsesLabels(s) {
			return true
		}
	}
	return false
}

// referencesOf builds the references of the crate's forms and records:
// each control of a form, and each value of a record, whose field or
// argument declares RefersTo, or whose field derives one (D61, amended),
// and whose options operation has an in-process call; and the steps of the
// console's trail, trails, whose operations' parameter refers to one
// target by one value. A reference whose options operation has none is
// left out, with the reason in the build log, and its control, its value
// and its step are as they were. It is nil when nothing the crate holds
// refers to an item.
func referencesOf(c *crate, schemas schemaSet, inProcess []declared, trails []trailStep, logf func(format string, args ...any)) (*referencesModel, error) {
	b := &refBuilder{
		schemas:   schemas,
		calls:     map[*ir.FieldDef]declared{},
		sources:   map[string]*refSource{},
		options:   map[*ir.FieldDef]*refOptions{},
		lookups:   map[string]*refLookup{},
		hosted:    c.Hosted,
		hostedNot: c.hostedNot,
		logged:    map[string]bool{},
		logf:      logf,
		m:         &referencesModel{},
	}
	b.dependencies = map[string]*ir.Schema{}
	for _, dep := range schemas[1:] {
		b.dependencies[dep.Name] = dep
	}
	for _, d := range inProcess {
		if d.op != nil {
			b.calls[d.op] = d
		}
	}
	b.noCall = c.noCalls
	argsOf := map[string]*ir.FieldDef{}
	for _, d := range inProcess {
		if d.endpoint.ArgsName != "" {
			argsOf[d.endpoint.ArgsName] = d.op
		}
	}
	for _, f := range c.Forms {
		sites := b.structSites(f.Struct, "form", nil, map[*formStruct]bool{f.Struct: true})
		if len(sites) > 0 {
			b.m.Forms = append(b.m.Forms, &refForm{Fn: componentName(f.Name) + "_choices", Form: f.Name, Sites: sites})
		}
	}
	for _, f := range c.ArgForms {
		var sites []refSite
		op := argsOf[f.ArgsName]
		for _, a := range f.Fields {
			if a.Kind == kindPath || !a.Choosable || op == nil {
				continue
			}
			for _, arg := range op.Arguments {
				if arg.Name == a.JSONName && arg.References != nil {
					if source := b.source(fmt.Sprintf("%s.%s(%s)", f.Namespace, f.Operation, arg.Name), arg.References); source != nil {
						site := refSite{Path: arg.Name, Keys: []string{arg.Name}, Source: source, Root: "form"}
						site.Steps = []siteStep{{Field: a.Name, Key: arg.Name}}
						site.Pick = source.Picks() && a.Kind == kindValue
						sites = append(sites, site)
					}
				}
			}
		}
		if f.Input != nil {
			sites = append(sites, b.structSites(f.Input.Struct, "form.input", nil, map[*formStruct]bool{f.Input.Struct: true})...)
		}
		if len(sites) > 0 {
			b.m.Forms = append(b.m.Forms, &refForm{Fn: componentName(f.Name) + "_choices", Form: f.Name, Sites: sites, Get: f.IsGet()})
		}
	}
	// A display component names what a record shows, so only a crate with
	// the components labels its records.
	if len(c.Views) > 0 {
		for _, rec := range c.Records {
			sites := b.recordSites(rec.TypeName, nil, map[string]bool{rec.TypeName: true})
			if len(sites) == 0 {
				continue
			}
			r := &refRecord{Fn: componentName(rec.TypeName) + "_labels", Record: rec.Name}
			for _, site := range sites {
				placed := false
				for i := range r.Groups {
					if r.Groups[i].Source == site.Source {
						r.Groups[i].Sites = append(r.Groups[i].Sites, site)
						placed = true
					}
				}
				if !placed {
					r.Groups = append(r.Groups, refGroup{Source: site.Source, Sites: []refSite{site}})
				}
			}
			b.m.Records = append(b.m.Records, r)
		}
	}
	for _, step := range trails {
		if source := b.trail(step); source != nil {
			b.m.Trails = append(b.m.Trails, &refTrail{Key: step.node.key, Source: source})
		}
	}
	if len(b.m.Forms) == 0 && len(b.m.Records) == 0 && len(b.m.Trails) == 0 {
		return nil, nil
	}
	b.finish()
	return b.m, nil
}

// trail is the source that names a step of the console's trail (D44,
// amended): the one target, by one value, that each operation's argument
// at the step refers to, resolved by the core's rule. When they disagree
// it is nil, and the build log names each; when its options operation has
// no in-process call, nil with that reason.
func (b *refBuilder) trail(step trailStep) *refSource {
	var first *trailHolder
	var target *ir.TypeDef
	value := ""
	agree := true
	var said []string
	for i := range step.holders {
		h := &step.holders[i]
		resolved, err := ir.ResolveReference(b.schemas[0], b.dependencies, h.arg.References)
		if err != nil {
			continue
		}
		verb := "to"
		if first == nil {
			first, target, value, verb = h, resolved.Target, resolved.Value.Name, "refers to"
		} else if resolved.Target != target || resolved.Value.Name != value {
			agree = false
		}
		said = append(said, fmt.Sprintf("%s %s %s by %s", h.name(), verb, targetName(resolved), resolved.Value.Name))
	}
	switch {
	case first == nil:
		return nil
	case !agree:
		b.log("  - topcoat: the trail names %s by its id: %s, and %s\n", first.item, strings.Join(said[:len(said)-1], ", "), said[len(said)-1])
		return nil
	}
	return b.source(first.name(), first.arg.References)
}

// targetName is a reference's target as the build log names it: by its
// name, after its service's when a dependency declares it (D66).
func targetName(resolved ir.ResolvedReference) string {
	if resolved.TargetService != "" {
		return resolved.TargetService + "." + resolved.TargetName
	}
	return resolved.TargetName
}

// refBuilder resolves each reference once, by its target and members.
type refBuilder struct {
	schemas      schemaSet
	dependencies map[string]*ir.Schema
	// calls are the in-process calls by their operation; noCall the reason
	// of each operation without one.
	calls  map[*ir.FieldDef]declared
	noCall map[*ir.FieldDef]string
	// hosted are the crates of the API dependencies one app hosts beside
	// the service, whose calls a reference to a dependency's options makes
	// (D61, amended); hostedNot why each other API dependency has none.
	hosted    map[string]*hostedCrate
	hostedNot map[string]string
	sources   map[string]*refSource
	options   map[*ir.FieldDef]*refOptions
	lookups   map[string]*refLookup
	logged    map[string]bool
	logf      func(format string, args ...any)
	m         *referencesModel
}

// log writes a reference's reason once.
func (b *refBuilder) log(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	if !b.logged[line] {
		b.logged[line] = true
		b.logf("%s", line)
	}
}

// structSites are the references the controls of a form struct hold, at
// their paths under prefix, through its nested objects and rows, the Rust
// place of the form's struct at root. A struct already on the way down is
// not entered again.
func (b *refBuilder) structSites(st *formStruct, root string, prefix []siteStep, open map[*formStruct]bool) []refSite {
	typeDef := b.schemas.objectType(st.TypeName)
	if typeDef == nil {
		return nil
	}
	var sites []refSite
	for _, f := range st.Fields {
		if f.Hidden {
			continue
		}
		steps := append(append([]siteStep(nil), prefix...), siteStep{Field: f.Name, Key: f.JSONName, Rows: f.Kind == kindValueRows || f.Kind == kindObjectRows})
		keys := make([]string, len(steps))
		for i, step := range steps {
			keys[i] = step.Key
		}
		switch f.Kind {
		case kindValue, kindValueRows:
			field := fieldNamed(typeDef, f.JSONName)
			if field == nil || !f.Choosable {
				continue
			}
			ref := b.fieldReference(typeDef, field)
			if ref == nil {
				continue
			}
			if source := b.source(st.TypeName+"."+field.Name, ref); source != nil {
				sites = append(sites, refSite{Path: strings.Join(keys, "."), Keys: keys, Source: source, Root: root, Steps: steps, Pick: source.Picks()})
			}
		case kindObject, kindObjectRows:
			if open[f.Child] {
				continue
			}
			open[f.Child] = true
			sites = append(sites, b.structSites(f.Child, root, steps, open)...)
			delete(open, f.Child)
		}
	}
	return sites
}

// recordSites are the references a record of typeName shows, at their
// paths under prefix, through the records it nests: a value a display
// component shows as text or a number, which its choices label. A type
// already on the way down is not entered again.
func (b *refBuilder) recordSites(typeName string, prefix []string, open map[string]bool) []refSite {
	typeDef := b.schemas.objectType(typeName)
	if typeDef == nil {
		return nil
	}
	var sites []refSite
	for _, field := range typeDef.Fields {
		if field.UIHidden || field.Secret {
			continue
		}
		ref := field.TypeRef
		keys := append(append([]string(nil), prefix...), field.Name)
		if ref.IsMap || ref.IsArrayOfArrays {
			continue
		}
		if nested := b.schemas.objectType(ref.Name); nested != nil {
			if open[ref.Name] {
				continue
			}
			open[ref.Name] = true
			sites = append(sites, b.recordSites(ref.Name, keys, open)...)
			delete(open, ref.Name)
			continue
		}
		held := b.fieldReference(typeDef, field)
		if held == nil || !b.labelled(ref.Name) {
			continue
		}
		if source := b.source(typeName+"."+field.Name, held); source != nil {
			sites = append(sites, refSite{Path: strings.Join(keys, "."), Keys: keys, Source: source})
		}
	}
	return sites
}

// fieldReference is the reference a field of holder holds (D61, amended):
// its RefersTo, or, without one, the reference a relation's key derives
// from holder's @source, so a key that reads a relation's foreign key is
// offered and labeled as a written one is. Nil when it refers to nothing.
func (b *refBuilder) fieldReference(holder *ir.TypeDef, field *ir.FieldDef) *ir.ReferenceDef {
	ref, _ := ir.FieldReference(b.schemas[0], b.dependencies, holder, field)
	return ref
}

// labelled reports whether a display component shows a value of the named
// type as text or a number, which its choices label: not an enum, a date
// or a time, a boolean or JSON.
func (b *refBuilder) labelled(name string) bool {
	if b.schemas.enum(name) != nil || timeFunc(b.schemas.scalar(name)) != "" {
		return false
	}
	switch b.schemas.leafOf(name) {
	case stringLeaf, integerLeaf, numberLeaf:
		return true
	}
	return false
}

// source is the source of a reference holder holds, resolved by the core's
// rule (ir.ResolveReference), or nil, with the reason in the build log,
// when its options operation has no in-process call.
func (b *refBuilder) source(holder string, ref *ir.ReferenceDef) *refSource {
	resolved, err := ir.ResolveReference(b.schemas[0], b.dependencies, ref)
	if err != nil {
		b.log("  - topcoat: %s refers to %s, which the extension ignores: %v\n", holder, ref.Type, err)
		return nil
	}
	options := resolved.OptionsSet.Name + "." + resolved.Options.Name
	calls, noCalls := b.calls, b.noCall
	var hosted *hostedCrate
	if service := resolved.OptionsService; service != "" {
		// The options of an API dependency's type are its own operation's
		// (D61, amended), which its crate calls, when one app hosts both.
		options = service + "'s " + options
		if hosted = b.hosted[service]; hosted == nil {
			b.log("  - topcoat: %s refers to %s, whose options operation %s has no in-process call: %s\n", holder, targetName(resolved), options, b.hostedNot[service])
			return nil
		}
		calls, noCalls = hosted.calls, hosted.noCall
	}
	d, ok := calls[resolved.Options]
	if !ok {
		reason := noCalls[resolved.Options]
		if reason == "" {
			reason = "it is not among the crate's operations"
		}
		b.log("  - topcoat: %s refers to %s, whose options operation %s has no in-process call: %s\n", holder, targetName(resolved), options, reason)
		return nil
	}
	if hosted != nil {
		hosted.used = true
	}
	label := ""
	if resolved.Label != nil {
		label = resolved.Label.Name
	}
	lookup := ""
	if resolved.Lookup != nil {
		lookup = resolved.Lookup.Name
	}
	key := strings.Join([]string{resolved.TargetService, resolved.TargetName, resolved.Value.Name, label, resolved.OptionsService, options, lookup}, "|")
	if s := b.sources[key]; s != nil {
		return s
	}
	s := &refSource{
		Target:  resolved.TargetName,
		Plural:  b.pluralOf(resolved),
		Value:   resolved.Value.Name,
		Label:   label,
		Options: b.optionsOf(d, resolved.Search, hosted),
		key:     key,
	}
	if resolved.Lookup != nil {
		s.Lookup = b.lookupOf(d, s.Options, resolved.Lookup)
	}
	b.sources[key] = s
	return s
}

// pluralOf names a reference's target's items in a sentence: the
// target's @display plural, else its name in words.
func (b *refBuilder) pluralOf(resolved ir.ResolvedReference) string {
	if display := resolved.Target.Display; display != nil && display.Plural != "" {
		return display.Plural
	}
	return humanize(resolved.TargetName)
}

// optionsOf is the options call of the operation d serves, made once, and
// its search's, by search, when it has one (D61, amended), through hosted,
// the crate of the API dependency whose operation it is, or the service's
// own when nil. A dependency's functions are named after it too.
func (b *refBuilder) optionsOf(d declared, search *ir.ArgumentDef, hosted *hostedCrate) *refOptions {
	if o := b.options[d.op]; o != nil {
		return o
	}
	e := d.endpoint
	stem := e.SnakeName()
	operation := e.Namespace + "." + e.Name
	if hosted != nil {
		stem = strings.TrimPrefix(registry.RustIdentifier(hosted.Service, "service"), "r#") + "_" + stem
		operation = hosted.Service + "'s " + operation
	}
	o := &refOptions{
		Fn:        stem + "_options",
		Operation: operation,
		Snake:     e.SnakeName(),
		Paged:     e.Page != nil,
		Hosted:    hosted,
		controls:  controlsOf(e),
	}
	if hosted != nil {
		o.controls.Crate = hosted.Ident
	}
	if e.HasArgs() {
		o.ArgsName = e.ArgsName
		for _, param := range e.QueryArgs {
			o.Nones = append(o.Nones, param.Field)
		}
	}
	if search != nil && e.HasArgs() {
		for _, param := range e.QueryArgs {
			if param.Name != search.Name {
				continue
			}
			o.Search = &refSearch{Fn: stem + "_search", Searched: stem + "_searched", Arg: search.Name, Field: param.Field}
			o.Search.Attrs = b.schemas.argField(search, param, "query", true).Attrs
			for _, other := range e.QueryArgs {
				if other.Name != search.Name {
					o.Search.Others = append(o.Search.Others, other.Field)
				}
			}
		}
	}
	b.options[d.op] = o
	b.m.Options = append(b.m.Options, o)
	return o
}

// lookupOf is the lookup call of the operation d serves by arg, made once.
func (b *refBuilder) lookupOf(d declared, options *refOptions, arg *ir.ArgumentDef) *refLookup {
	e := d.endpoint
	key := options.Fn + "|" + arg.Name
	if l := b.lookups[key]; l != nil {
		return l
	}
	l := &refLookup{Options: options, Arg: arg.Name, Read: "as_text"}
	for _, param := range e.QueryArgs {
		if param.Name == arg.Name {
			l.Field = param.Field
		} else {
			l.Others = append(l.Others, param.Field)
		}
	}
	l.Fn = strings.TrimSuffix(options.Fn, "_options") + "_by_" + strings.TrimPrefix(registry.RustIdentifier(arg.Name, "values"), "r#")
	switch b.schemas.leafOf(arg.TypeRef.Name) {
	case integerLeaf:
		l.Read = "as_integer"
	case numberLeaf:
		l.Read = "as_number"
	}
	if arg.ValidateListMax != nil && *arg.ValidateListMax > 0 {
		l.Chunk = *arg.ValidateListMax
	}
	if e.Page != nil && (l.Chunk == 0 || e.Page.MaxLimit < l.Chunk) {
		l.Chunk = e.Page.MaxLimit
	}
	b.lookups[key] = l
	b.m.Lookups = append(b.m.Lookups, l)
	return l
}

// finish names each source's functions after its target and its value
// when it is not the default, and a number when two still share a name,
// in the order of their keys. A source's functions are
// <Base>_target_choices and <Base>_target_labels, apart from a form's
// <form>_choices and a record's <record>_labels, which a target's own
// record has too when it shows a reference itself (Product, whose
// supplier it names, and the order lines that name products); a stem
// whose functions would still be one of the module's others takes a
// number.
func (b *refBuilder) finish() {
	module := map[string]bool{}
	for _, f := range b.m.Forms {
		module[f.Fn] = true
	}
	for _, r := range b.m.Records {
		module[r.Fn] = true
	}
	for _, o := range b.m.Options {
		module[o.Fn] = true
		if o.Search != nil {
			module[o.Search.Fn] = true
		}
	}
	for _, l := range b.m.Lookups {
		module[l.Fn] = true
	}
	taken := map[string]bool{}
	clashes := func(name string) bool {
		return taken[name] || module[name+"_target_choices"] || module[name+"_target_labels"] || module[name+"_target_label"] ||
			module[name+"_target_picks"] || module[name+"_pick"]
	}
	for _, key := range sortedKeys(b.sources) {
		s := b.sources[key]
		base := componentName(s.Target)
		if s.Value != ir.DefaultReferenceValue {
			base += "_by_" + snakeWord(s.Value)
		}
		name := base
		for i := 2; clashes(name); i++ {
			name = fmt.Sprintf("%s_%d", base, i)
		}
		taken[name] = true
		s.Base = name
		b.m.Sources = append(b.m.Sources, s)
	}
	// An options call or a lookup that nothing calls is not written: a
	// form's reference calls the options, a record's the lookup.
	var lookups []*refLookup
	for _, l := range b.m.Lookups {
		if b.m.UsesLookup(l) {
			lookups = append(lookups, l)
		}
	}
	b.m.Lookups = lookups
	var options []*refOptions
	for _, o := range b.m.Options {
		used := b.m.UsesListing(o) || b.m.Searching(o)
		for _, l := range lookups {
			used = used || l.Options == o
		}
		if used {
			options = append(options, o)
		}
	}
	b.m.Options = options
	sort.Slice(b.m.Options, func(i, j int) bool { return b.m.Options[i].Fn < b.m.Options[j].Fn })
	sort.Slice(b.m.Lookups, func(i, j int) bool { return b.m.Lookups[i].Fn < b.m.Lookups[j].Fn })
	sort.Slice(b.m.Forms, func(i, j int) bool { return b.m.Forms[i].Fn < b.m.Forms[j].Fn })
	sort.Slice(b.m.Records, func(i, j int) bool { return b.m.Records[i].Fn < b.m.Records[j].Fn })
	sort.Slice(b.m.Trails, func(i, j int) bool { return b.m.Trails[i].Key < b.m.Trails[j].Key })
}

// FormChoices is the choices function of the form named form, or empty
// when it holds no reference.
func (m *referencesModel) FormChoices(form string) string {
	if m == nil {
		return ""
	}
	for _, f := range m.Forms {
		if f.Form == form {
			return f.Fn
		}
	}
	return ""
}

// OptionsCalledBy are the options operations a form's choices function, a
// record's labels function or a trail's label function, fn, calls, listing
// their targets or looking them up, once each in the order of its sites;
// none for an empty fn.
func (m *referencesModel) OptionsCalledBy(fn string) []*refOptions {
	if m == nil || fn == "" {
		return nil
	}
	var sources []*refSource
	for _, f := range m.Forms {
		if f.Fn == fn {
			for _, site := range f.Sites {
				sources = append(sources, site.Source)
			}
		}
	}
	for _, r := range m.Records {
		if r.Fn == fn {
			for _, g := range r.Groups {
				sources = append(sources, g.Source)
			}
		}
	}
	for _, t := range m.Trails {
		if t.Fn() == fn {
			sources = append(sources, t.Source)
		}
	}
	var called []*refOptions
	seen := map[*refOptions]bool{}
	for _, s := range sources {
		if !seen[s.Options] {
			seen[s.Options] = true
			called = append(called, s.Options)
		}
	}
	return called
}

// RecordLabels is the labels function of the record named record, or
// empty when it shows no reference.
func (m *referencesModel) RecordLabels(record string) string {
	if m == nil {
		return ""
	}
	for _, r := range m.Records {
		if r.Record == record {
			return r.Fn
		}
	}
	return ""
}

// TrailLabel is the label function that names an item's step of the
// collection key in the console's trail, or empty when none does.
func (m *referencesModel) TrailLabel(key string) string {
	if m == nil {
		return ""
	}
	for _, t := range m.Trails {
		if t.Key == key {
			return t.Fn()
		}
	}
	return ""
}
