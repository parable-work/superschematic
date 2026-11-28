package tswriter

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// emitTableFeatureDecorators writes the declarations of a DB class that
// turn on a feature of the platform, each as the decorator the registry
// reads it from: the calendar's sources (D152), events, the notifier and
// channel identities (D153), references and attachments (D154), search
// (D155), the audience, the grants and the access log (D156), parties,
// the conversations and the run requests (D157), and an API's provider
// slots (D159). The fields and tables the loader adds for each are left
// out (writer.withoutExpansion), and reading the declaration back adds
// them again.
func (e *emitter) emitTableFeatureDecorators(def *ir.TypeDef) {
	for _, source := range def.CalendarSources {
		e.decorator("calendarSource", def.Name, calendarSourceArgs(source))
	}
	for _, event := range def.Events {
		e.decorator("event", def.Name, eventArgs(event))
	}
	if def.Notifier != nil {
		e.decorator("notifier", "", quote(def.Notifier.Part))
	}
	if def.ChannelIdentities != nil {
		e.decorator("channelIdentities", "", "")
	}
	if refs := def.RowReferences; refs != nil {
		e.decorator("references", def.Name, e.rowReferencesArgs(refs))
	}
	if files := def.Attachments; files != nil {
		e.decorator("attachments", def.Name, e.attachmentsArgs(files))
	}
	if search := def.Searchable; search != nil {
		e.decorator("searchable", def.Name, e.searchableArgs(search))
	}
	if audience := def.Audience; audience != nil {
		e.decorator("audience", def.Name, audienceArgs(audience))
	}
	if def.AccessGrants != nil {
		e.decorator("accessGrants", "", "")
	}
	if def.AccessLog != nil {
		e.decorator("accessLog", "", "")
	}
	if party := def.Party; party != nil {
		var parts []string
		if party.Title != "" {
			parts = append(parts, "title: "+quote(party.Title))
		}
		e.decorator("party", def.Name, optionalObject(parts))
	}
	if def.Conversations != nil {
		e.decorator("conversations", "", quote(def.Conversations.Part))
	}
	if def.RunRequests != nil {
		e.decorator("runRequests", "", "")
	}
	if len(def.ProviderSlots) > 0 {
		slots := make([]string, 0, len(def.ProviderSlots))
		for _, slot := range slices.Sorted(maps.Keys(def.ProviderSlots)) {
			slots = append(slots, propertyName(slot)+": "+quote(def.ProviderSlots[slot]))
		}
		e.decorator("providers", "", objectLiteral(slots))
	}
}

// decorator writes one class decorator line, `@name<Type>(args)`, with the
// type argument when typeArg is set.
func (e *emitter) decorator(name, typeArg, args string) {
	generic := ""
	if typeArg != "" {
		generic = "<" + typeArg + ">"
	}
	fmt.Fprintf(&e.body, "@%s%s(%s)\n", e.use(name), generic, args)
}

// optionalObject renders an options object, or no argument when it sets
// nothing.
func optionalObject(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return objectLiteral(parts)
}

// classValue renders a class a decorator names as a value, importing it
// when another file of the schema declares it. A name the schema declares
// nowhere is written as a string, which the registry reads the same.
func (e *emitter) classValue(name string) string {
	if !e.declares(name) || !identifierPattern.MatchString(name) {
		return quote(name)
	}
	e.recordHeritageImports(name)
	return name
}

// calendarSourceArgs renders @calendarSource's options (D152), in the
// order its shape names them.
func calendarSourceArgs(source ir.CalendarSourceDef) string {
	var parts []string
	text := func(key, value string) {
		if value != "" {
			parts = append(parts, key+": "+quote(value))
		}
	}
	text("name", source.Name)
	text("field", source.Field)
	text("end", source.End)
	if source.Occurrences {
		parts = append(parts, "occurrences: true")
	}
	text("title", source.Title)
	text("kind", source.Kind)
	text("color", source.Color)
	text("owner", source.Owner)
	// permission is the schema's permission of the source (#601).
	text("permission", source.Permission)
	return objectLiteral(parts)
}

// eventArgs renders @event's options (D153).
func eventArgs(event *ir.EventDef) string {
	parts := []string{"kind: " + quote(event.Kind)}
	if len(event.Payload) > 0 {
		parts = append(parts, "payload: "+stringListLiteral(event.Payload))
	}
	for _, opt := range []struct{ key, value string }{
		{"title", event.Title}, {"urgency", event.Urgency}, {"description", event.Description},
	} {
		if opt.value != "" {
			parts = append(parts, opt.key+": "+quote(opt.value))
		}
	}
	return objectLiteral(parts)
}

// rowReferencesArgs renders @references's options (D154), or none for
// every option's default.
func (e *emitter) rowReferencesArgs(refs *ir.RowReferencesDef) string {
	var parts []string
	if len(refs.Tables) > 0 {
		tables := make([]string, len(refs.Tables))
		for i, table := range refs.Tables {
			tables[i] = e.classValue(table)
		}
		parts = append(parts, "tables: ["+strings.Join(tables, ", ")+"]")
	}
	if refs.Cleared != "" {
		parts = append(parts, "cleared: "+e.classValue(refs.Cleared))
	}
	if refs.MaxPerRow != 0 {
		parts = append(parts, "maxPerRow: "+strconv.Itoa(refs.MaxPerRow))
	}
	return optionalObject(parts)
}

// attachmentsArgs renders @attachments's options (D154).
func (e *emitter) attachmentsArgs(files *ir.AttachmentsDef) string {
	parts := []string{"released: " + e.classValue(files.Released)}
	for _, opt := range []struct {
		key   string
		value int64
	}{{"maxFileBytes", files.MaxFileBytes}, {"maxRowBytes", files.MaxRowBytes}, {"maxFiles", int64(files.MaxFiles)}} {
		if opt.value != 0 {
			parts = append(parts, opt.key+": "+strconv.FormatInt(opt.value, 10))
		}
	}
	if len(files.MediaTypes) > 0 {
		parts = append(parts, "mediaTypes: "+stringListLiteral(files.MediaTypes))
	}
	return objectLiteral(parts)
}

// searchableArgs renders @searchable's options (D155): body as a field when
// it holds one, which reads back as the list of it.
func (e *emitter) searchableArgs(search *ir.SearchableDef) string {
	parts := []string{"title: " + quote(search.Title)}
	switch len(search.Body) {
	case 0:
	case 1:
		parts = append(parts, "body: "+quote(search.Body[0]))
	default:
		parts = append(parts, "body: "+stringListLiteral(search.Body))
	}
	if search.Scope != "" {
		parts = append(parts, "scope: "+quote(search.Scope))
	}
	parts = append(parts, "queue: "+e.classValue(search.Queue))
	for _, opt := range []struct {
		key   string
		value *bool
	}{{"comments", search.Comments}, {"attachments", search.Attachments}} {
		if opt.value != nil {
			parts = append(parts, fmt.Sprintf("%s: %t", opt.key, *opt.value))
		}
	}
	return objectLiteral(parts)
}

// audienceArgs renders @audience's options (D156), or none for every
// option's default.
func audienceArgs(audience *ir.AudienceDef) string {
	var parts []string
	for _, opt := range []struct{ key, value string }{
		{"owner", audience.Owner}, {"inheritFrom", audience.InheritFrom}, {"default", audience.Default}, {"agents", audience.Agents},
	} {
		if opt.value != "" {
			parts = append(parts, opt.key+": "+quote(opt.value))
		}
	}
	return optionalObject(parts)
}

// tableFeatureClassRefs returns the classes def's feature declarations
// name as values, which must be declared before it: the tables its
// references keep to, the queues its references, attachments and search
// enqueue on.
func tableFeatureClassRefs(def *ir.TypeDef) []string {
	var refs []string
	if r := def.RowReferences; r != nil {
		refs = append(refs, r.Tables...)
		if r.Cleared != "" {
			refs = append(refs, r.Cleared)
		}
	}
	if def.Attachments != nil {
		refs = append(refs, def.Attachments.Released)
	}
	if def.Searchable != nil {
		refs = append(refs, def.Searchable.Queue)
	}
	return refs
}
