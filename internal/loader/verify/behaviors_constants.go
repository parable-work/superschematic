package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// constantsBehavior is the name of the core's Constants.
const constantsBehavior = "Constants"

// oneWayBooleans and oneWayNumbers are the IR types of a field that moves
// one way and holds a boolean, and a number (D104): the core's
// superschematic_rules_behaviors::constants::ONE_WAY_BOOLEANS and
// ONE_WAY_NUMBERS, which TestOneWayTypesAreTheCores holds them to. A
// date-time is the third kind: a field the layout converts as epoch
// milliseconds (behaviorlayout.ConvertEpochMillis), as the core's spec
// marks it.
var (
	oneWayBooleans = []string{"boolean"}
	oneWayNumbers  = []string{"number", "Generic.Int64", "Generic.Float64"}
)

// fieldClass is what a name a config gives as a field of a table is, as the
// build sees it (D104): one of the table's own fields, which the core's spec
// lists; a field a use of the table owns; a to-one relation named by its
// field rather than its key's; an audit field; or none of the table's.
type fieldClass int

const (
	fieldOwn fieldClass = iota
	fieldOwned
	fieldRelation
	fieldAudit
	fieldMissing
)

// tableFields are what a config of a use of td names fields against: the
// fields the core's spec lists (behaviorlayout.SpecFields), and the fields
// td's uses own by name, with the behavior of each, which the loader's pass
// adds after the checks (an IR read back holds them as owned fields too).
type tableFields struct {
	schema *ir.Schema
	td     *ir.TypeDef
	spec   []behaviorlayout.Column
	owned  map[string]string
}

// tableFieldsOf reads td's fields for the config checks, uses being the
// registered behavior of each of its table uses by id.
func tableFieldsOf(schema *ir.Schema, td *ir.TypeDef, uses map[string]registry.Behavior) *tableFields {
	f := &tableFields{schema: schema, td: td, spec: behaviorlayout.SpecFields(schema, td), owned: map[string]string{}}
	for _, ref := range td.TableBehaviors() {
		behavior, ok := uses[ref.UseID()]
		if !ok {
			continue
		}
		member := behaviorlayout.UseMember(ref)
		for _, field := range behavior.Fields {
			if field.Volatile || (field.Type == "" && !ref.FieldAnchored()) {
				continue
			}
			value := ref.FieldAnchored() && field.Name == behavior.Value
			f.owned[behaviorlayout.OwnedFieldName(member, field.Name, value)] = ref.Name
		}
	}
	return f
}

// names are the spec's fields, as the core's refusal of a field the type
// lacks names them: "its fields: a, b", or none.
func (f *tableFields) names() string {
	if len(f.spec) == 0 {
		return "none"
	}
	names := make([]string, 0, len(f.spec))
	for _, c := range f.spec {
		names = append(names, c.Field)
	}
	return strings.Join(names, ", ")
}

// classify is what name is of td (fieldClass), with the behavior that owns
// it for fieldOwned, and its spec column and field for fieldOwn. createdBy
// is an audit field unless own is true, when it is one of td's own fields
// if the spec lists it.
func (f *tableFields) classify(name string, createdByIsOwn bool) (fieldClass, string, *behaviorlayout.Column, *ir.FieldDef) {
	fd := sqlutil.FieldNamed(f.td, name)
	if owner, ok := f.owned[name]; ok {
		return fieldOwned, owner, nil, fd
	}
	if fd.Owned() {
		return fieldOwned, fd.Behavior.Behavior, nil, fd
	}
	if fd != nil && sqlutil.AuditField(name) && (!createdByIsOwn || name != sqlutil.AuditCreatedBy) {
		return fieldAudit, "", nil, fd
	}
	if fd != nil && sqlutil.IsTableRelation(f.schema, fd) && !fd.TypeRef.IsArray {
		return fieldRelation, "", nil, fd
	}
	for i := range f.spec {
		if f.spec[i].Field == name {
			return fieldOwn, "", &f.spec[i], fd
		}
	}
	return fieldMissing, "", nil, fd
}

// unread is what fd, an own field of a table whose spec column is column,
// is when the core reads no value of it, or "" (D104): a field encrypted at
// rest, whose envelope the repositories keep from the core, and a list or a
// map of date-times, which no adapter converts. Neither ORM gives the core
// such a field in a command's own fields (ormgen's CoreOwn, orm.ts's
// coreOwn), so a rule that holds one never sees it change: a constant or a
// frozen field would change, a required one would read as never given, and
// a resetOn field would consume nothing. It reads "<what>: the core reads
// no value of it", after "is" or "which is".
func unread(column *behaviorlayout.Column, fd *ir.FieldDef) string {
	switch {
	case fd == nil:
		return ""
	case fd.EncryptedAtRest:
		return "encrypted at rest: the core reads no value of it"
	case column != nil && behaviorlayout.ConvertOf(column.Type) == behaviorlayout.ConvertEpochMillis && fd.TypeRef.IsArray:
		return "a list of date-times, which no adapter converts: the core reads no value of it"
	case column != nil && behaviorlayout.ConvertOf(column.Type) == behaviorlayout.ConvertEpochMillis && fd.TypeRef.IsMap:
		return "a map of date-times, which no adapter converts: the core reads no value of it"
	}
	return ""
}

// unreadFields are the fields of the spec of fields' table the core reads
// no value of (unread), in the spec's order, each as "<name> (<what>)".
func (f *tableFields) unreadFields() []string {
	var out []string
	for i := range f.spec {
		column := &f.spec[i]
		if why := unread(column, sqlutil.FieldNamed(f.td, column.Field)); why != "" {
			what, _, _ := strings.Cut(why, ":")
			what, _, _ = strings.Cut(what, ",")
			out = append(out, column.Field+" ("+what+")")
		}
	}
	return out
}

// checkConstantsConfig holds the config of a Constants use a table
// implements to the table (D104), in the core's words where the core sees
// the problem (superschematic_rules_behaviors::constants): each of fields
// names an own field of the table, and each oneWay member one that fields
// does not list and that holds a boolean, a number or a date-time. What only
// the IR shows is the build's: a field a behavior owns, which only its
// operations change; a relation named by its field; an audit field, which
// the writes stamp; a field the core reads no value of (unread), in fields
// or oneWay; and a oneWay list and a Generic.Decimal, which reaches the
// core as text. Each problem is `type <T>: behavior Constants config: <reason>`,
// and like the core it reports the first: each of fields in order, then
// each oneWay member in the canonical config's order. A config the
// configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkConstantsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, uses map[string]registry.Behavior, r *Result) {
	var config struct {
		Fields []string          `json:"fields"`
		OneWay map[string]string `json:"oneWay"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	fields := tableFieldsOf(schema, td, uses)
	// kept is why name, at the config's place at, is no own field a
	// Constants rule holds, or "".
	kept := func(at, name string) (string, *behaviorlayout.Column, *ir.FieldDef) {
		class, owner, column, fd := fields.classify(name, false)
		switch class {
		case fieldOwned:
			return fmt.Sprintf("%s: %s is a field %s owns, which only its operations change: Constants keeps the table's own fields", at, name, owner), nil, nil
		case fieldRelation:
			return fmt.Sprintf("%s: %s is a relation: name the field that holds its key, %sId", at, name, name), nil, nil
		case fieldAudit:
			return fmt.Sprintf("%s: %s is an audit field the writes stamp, which no update changes", at, name), nil, nil
		case fieldMissing:
			return fmt.Sprintf("%s: %s is not a field of %s (its fields: %s)", at, name, td.Name, fields.names()), nil, nil
		}
		return "", column, fd
	}
	for _, name := range config.Fields {
		why, column, fd := kept("fields", name)
		if why == "" && column != nil {
			if what := unread(column, fd); what != "" {
				why = fmt.Sprintf("fields: %s is %s", name, what)
			}
		}
		if why != "" {
			fail("%s", why)
			return
		}
	}
	for _, name := range slices.Sorted(maps.Keys(config.OneWay)) {
		why, column, fd := kept("oneWay", name)
		switch {
		case why != "":
		case slices.Contains(config.Fields, name):
			why = fmt.Sprintf("oneWay: %s is in fields, which keep the value their create gives them: a field moves one way or stays, not both", name)
		case fd != nil && (fd.TypeRef.IsArray || fd.TypeRef.IsMap):
			why = fmt.Sprintf("oneWay: %s is a list: a field that moves one way holds one boolean, number or date-time", name)
		case column.Type == "Generic.Decimal":
			why = fmt.Sprintf("oneWay: %s is a Generic.Decimal field of %s, which reaches the core as text: a field that moves one way holds a boolean, a number or a date-time", name, td.Name)
		case fd != nil && fd.EncryptedAtRest:
			why = fmt.Sprintf("oneWay: %s is encrypted at rest: the core reads no value of it", name)
		case !movesOneWay(*column):
			why = fmt.Sprintf("oneWay: %s is a %s field of %s: a field that moves one way holds a boolean, a number or a date-time", name, column.Type, td.Name)
		}
		if why != "" {
			fail("%s", why)
			return
		}
	}
}

// movesOneWay reports whether the core orders the values of column, a field
// of a table's spec, for oneWay (D104): a boolean, a number or a date-time.
func movesOneWay(column behaviorlayout.Column) bool {
	return slices.Contains(oneWayBooleans, column.Type) || slices.Contains(oneWayNumbers, column.Type) || column.Convert == behaviorlayout.ConvertEpochMillis
}

// checkEngineConstantsConfig refuses, on an engine type, oneWay, which a
// table's Constants alone takes (D104). The declaration is shared, so the
// configSchema accepts it; the engine's Constants refuses it too, and the
// build says so first, naming the table form. A config the configSchema
// refuses is reported by checkTypeBehaviors and left alone here.
func checkEngineConstantsConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	if configNames(ref.Config, "oneWay") {
		r.errorf(td.Owner, "type %s: @behavior Constants names oneWay, which a table's Constants takes (implements Constants<%s, C>, D104); an engine type's Constants takes fields and permission alone, so leave it out", td.Name, td.Name)
	}
}
