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
	// labels function.
	Forms   []*refForm
	Records []*refRecord
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
	Base   string // item_view, the stem of its functions
	Target string // ItemView
	Value  string // id
	// Label is the field that names an item, empty when none does and an
	// item is named by its value.
	Label   string
	Options *refOptions
	Lookup  *refLookup
	key     string
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
// and its source.
type refSite struct {
	Path   string
	Keys   []string
	Source *refSource
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
// each such control its options.
type refForm struct {
	Fn    string // basket_input_form_choices
	Form  string // BasketInputForm
	Sites []refSite
}

// refRecord is a record that shows a reference, at any depth: its labels
// function names each value its records hold, a call per source.
type refRecord struct {
	Fn     string // basket_view_labels
	Record string // BasketViewRecord
	Groups []refGroup
}

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

// Paged reports whether an options operation pages, so references.rs
// names PageRequest.
func (m *referencesModel) Paged() bool {
	for _, o := range m.Options {
		if o.Paged {
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

// UsesChoices and UsesLabels report whether a form's choices function, or
// a record's labels function, calls a source's.
func (m *referencesModel) UsesChoices(s *refSource) bool {
	for _, f := range m.Forms {
		for _, site := range f.Sites {
			if site.Source == s {
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

// Choosing reports whether a form's choices are written, which read
// whether more items follow a listing's.
func (m *referencesModel) Choosing() bool {
	for _, s := range m.Sources {
		if m.UsesChoices(s) {
			return true
		}
	}
	return false
}

// Listing reports whether an options operation's listing is called, so
// references.rs declares what holds one.
func (m *referencesModel) Listing() bool {
	for _, o := range m.Options {
		if m.UsesListing(o) {
			return true
		}
	}
	return false
}

// UsesLookup reports whether a lookup is called: by a record's labels.
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
// argument declares RefersTo and whose options operation has an in-process
// call. A reference whose options operation has none is left out, with the
// reason in the build log, and its control and value are as they were.
// It is nil when nothing the crate holds refers to an item.
func referencesOf(c *crate, schemas schemaSet, inProcess []declared, logf func(format string, args ...any)) (*referencesModel, error) {
	b := &refBuilder{
		schemas: schemas,
		calls:   map[*ir.FieldDef]declared{},
		sources: map[string]*refSource{},
		options: map[*ir.FieldDef]*refOptions{},
		lookups: map[string]*refLookup{},
		logged:  map[string]bool{},
		logf:    logf,
		m:       &referencesModel{},
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
		sites := b.structSites(f.Struct, nil, map[*formStruct]bool{f.Struct: true})
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
						sites = append(sites, refSite{Path: arg.Name, Keys: []string{arg.Name}, Source: source})
					}
				}
			}
		}
		if f.Input != nil {
			sites = append(sites, b.structSites(f.Input.Struct, nil, map[*formStruct]bool{f.Input.Struct: true})...)
		}
		if len(sites) > 0 {
			b.m.Forms = append(b.m.Forms, &refForm{Fn: componentName(f.Name) + "_choices", Form: f.Name, Sites: sites})
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
	if len(b.m.Forms) == 0 && len(b.m.Records) == 0 {
		return nil, nil
	}
	b.finish()
	return b.m, nil
}

// refBuilder resolves each reference once, by its target and members.
type refBuilder struct {
	schemas      schemaSet
	dependencies map[string]*ir.Schema
	// calls are the in-process calls by their operation; noCall the reason
	// of each operation without one.
	calls   map[*ir.FieldDef]declared
	noCall  map[*ir.FieldDef]string
	sources map[string]*refSource
	options map[*ir.FieldDef]*refOptions
	lookups map[string]*refLookup
	logged  map[string]bool
	logf    func(format string, args ...any)
	m       *referencesModel
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
// their paths under prefix, through its nested objects and rows. A struct
// already on the way down is not entered again.
func (b *refBuilder) structSites(st *formStruct, prefix []string, open map[*formStruct]bool) []refSite {
	typeDef := b.schemas.objectType(st.TypeName)
	if typeDef == nil {
		return nil
	}
	var sites []refSite
	for _, f := range st.Fields {
		if f.Hidden {
			continue
		}
		keys := append(append([]string(nil), prefix...), f.JSONName)
		switch f.Kind {
		case kindValue, kindValueRows:
			field := fieldNamed(typeDef, f.JSONName)
			if field == nil || field.References == nil || !f.Choosable {
				continue
			}
			if source := b.source(st.TypeName+"."+field.Name, field.References); source != nil {
				sites = append(sites, refSite{Path: strings.Join(keys, "."), Keys: keys, Source: source})
			}
		case kindObject, kindObjectRows:
			if open[f.Child] {
				continue
			}
			open[f.Child] = true
			sites = append(sites, b.structSites(f.Child, keys, open)...)
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
		if field.References == nil || !b.labelled(ref.Name) {
			continue
		}
		if source := b.source(typeName+"."+field.Name, field.References); source != nil {
			sites = append(sites, refSite{Path: strings.Join(keys, "."), Keys: keys, Source: source})
		}
	}
	return sites
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
	d, ok := b.calls[resolved.Options]
	if !ok {
		reason := b.noCall[resolved.Options]
		if reason == "" {
			reason = "it is not among the crate's operations"
		}
		b.log("  - topcoat: %s refers to %s, whose options operation %s has no in-process call: %s\n", holder, resolved.TargetName, options, reason)
		return nil
	}
	label := ""
	if resolved.Label != nil {
		label = resolved.Label.Name
	}
	lookup := ""
	if resolved.Lookup != nil {
		lookup = resolved.Lookup.Name
	}
	key := strings.Join([]string{resolved.TargetName, resolved.Value.Name, label, options, lookup}, "|")
	if s := b.sources[key]; s != nil {
		return s
	}
	s := &refSource{
		Target:  resolved.TargetName,
		Value:   resolved.Value.Name,
		Label:   label,
		Options: b.optionsOf(d),
		key:     key,
	}
	if resolved.Lookup != nil {
		s.Lookup = b.lookupOf(d, s.Options, resolved.Lookup)
	}
	b.sources[key] = s
	return s
}

// optionsOf is the options call of the operation d serves, made once.
func (b *refBuilder) optionsOf(d declared) *refOptions {
	if o := b.options[d.op]; o != nil {
		return o
	}
	e := d.endpoint
	o := &refOptions{
		Fn:        e.SnakeName() + "_options",
		Operation: e.Namespace + "." + e.Name,
		Snake:     e.SnakeName(),
		Paged:     e.Page != nil,
	}
	if e.HasArgs() {
		o.ArgsName = e.ArgsName
		for _, param := range e.QueryArgs {
			o.Nones = append(o.Nones, param.Field)
		}
	}
	b.options[d.op] = o
	b.m.Options = append(b.m.Options, o)
	return o
}

// lookupOf is the lookup call of the operation d serves by arg, made once.
func (b *refBuilder) lookupOf(d declared, options *refOptions, arg *ir.ArgumentDef) *refLookup {
	e := d.endpoint
	key := e.SnakeName() + "|" + arg.Name
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
	l.Fn = e.SnakeName() + "_by_" + strings.TrimPrefix(registry.RustIdentifier(arg.Name, "values"), "r#")
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

// finish names each source's functions after its target, the value and
// the label when they are not the default, and a number when two still
// share a name, in the order of their keys.
func (b *refBuilder) finish() {
	taken := map[string]bool{}
	for _, key := range sortedKeys(b.sources) {
		s := b.sources[key]
		base := componentName(s.Target)
		if s.Value != ir.DefaultReferenceValue {
			base += "_by_" + snakeWord(s.Value)
		}
		name := base
		for i := 2; taken[name]; i++ {
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
		used := b.m.UsesListing(o)
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
