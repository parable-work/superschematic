package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader/behaviorops"
	ir "github.com/parable-work/superschematic/ir"
)

// workflowTableMembers are the members of Workflow's config a table's
// Workflow alone takes (D104): a transition's by, overridePermission and
// repeat. An engine type's Workflow refuses each.
type workflowTableMembers struct {
	Transitions []struct {
		From string          `json:"from"`
		To   string          `json:"to"`
		By   json.RawMessage `json:"by"`
	} `json:"transitions"`
	OverridePermission json.RawMessage `json:"overridePermission"`
	Repeat             json.RawMessage `json:"repeat"`
}

// subjectTypes are the IR types of a field that holds a subject, which a
// Workflow transition's by may name (D104): the core's
// superschematic_rules_behaviors::subjects::SUBJECT_TYPES, which
// TestSubjectTypesAreTheCores holds them to.
var subjectTypes = []string{"string", "Identity.UUID", "Identity.UserID"}

// subjectTypesNamed is subjectTypes as a reason names them: "string,
// Identity.UUID or Identity.UserID".
func subjectTypesNamed() string {
	return strings.Join(subjectTypes[:len(subjectTypes)-1], ", ") + " or " + subjectTypes[len(subjectTypes)-1]
}

// checkEngineWorkflowConfig refuses, on an engine type, each member of
// Workflow's config that a table's Workflow alone takes (D104): each
// transition's by, in config order, then overridePermission, then repeat.
// The declaration is shared, so the configSchema accepts them; the
// engine's Workflow refuses them too, and the build says so first, naming
// the table form. A config the configSchema refuses is reported by
// checkTypeBehaviors and left alone here.
func checkEngineWorkflowConfig(td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config workflowTableMembers
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	refuse := func(member string) {
		r.errorf(td.Owner, "type %s: @behavior Workflow names %s, which a table's Workflow takes (status: Workflow<{ ... }> on a DB table, D104); an engine type's Workflow takes none of by, overridePermission and repeat, so leave it out", td.Name, member)
	}
	for _, transition := range config.Transitions {
		if len(transition.By) > 0 {
			refuse(fmt.Sprintf("by on the transition from %q to %q", transition.From, transition.To))
		}
	}
	if len(config.OverridePermission) > 0 {
		refuse("overridePermission")
	}
	if len(config.Repeat) > 0 {
		refuse("repeat")
	}
}

// checkWorkflowConfig holds the by of each transition of a Workflow use on
// a field of the table td to the table (D104): each entry names an own
// field of td that holds one subject (string, Identity.UUID or
// Identity.UserID), or createdBy, the audit field the repositories stamp
// with an operation's caller (D87). These are the rules of Workflow's
// config that read the IR, which the build can check before any command;
// the seat, the missing field and the type are the core's define issues
// (superschematic_rules_behaviors::workflow) word for word, and the
// relation, the other audit fields, the list and the encrypted field are
// what only the IR shows. Each is `type <T>: field <f>: behavior Workflow
// config: <reason>`, and like the core it reports the first: each
// transition in config order, each entry in order. The fields are the
// layout's own and audit fields (behaviorlayout.OwnFields and
// AuditFields), which the core's spec lists, so both name the same fields.
// A config the configSchema refuses is reported by checkTypeBehaviors and
// left alone here.
func checkWorkflowConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Transitions []struct {
			From string   `json:"from"`
			To   string   `json:"to"`
			By   []string `json:"by"`
		} `json:"transitions"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	var fields []behaviorlayout.Column
	read := false
	for _, transition := range config.Transitions {
		if len(transition.By) == 0 {
			continue
		}
		if !read {
			fields = append(behaviorlayout.OwnFields(schema, td), behaviorlayout.AuditFields(schema, td)...)
			read = true
		}
		for _, entry := range transition.By {
			if reason := byProblem(schema, td, fields, entry); reason != "" {
				r.errorf(td.Owner, "type %s: field %s: behavior Workflow config: the transition from %q to %q names %q in by, %s", td.Name, ref.Field, transition.From, transition.To, entry, reason)
				return
			}
		}
	}
}

// byProblem is why entry, an entry of a Workflow transition's by on td,
// names no field of td that holds a subject, or "" when it names one.
// fields are td's own and audit fields as the layout holds them.
func byProblem(schema *ir.Schema, td *ir.TypeDef, fields []behaviorlayout.Column, entry string) string {
	if strings.HasPrefix(entry, "seat:") {
		return "a seat of Seats, which the core does not run yet (D104, step 3)"
	}
	fd := sqlutil.FieldNamed(td, entry)
	if fd != nil && sqlutil.AuditField(entry) && entry != sqlutil.AuditCreatedBy {
		return "an audit field the writes stamp: by takes createdBy among the audit fields (D104)"
	}
	// A createdBy that is a relation holds no column of the layout's audit,
	// and so is no field by names, as the core's spec lists none.
	if fd != nil && !fd.Owned() && entry != sqlutil.AuditCreatedBy && sqlutil.IsTableRelation(schema, fd) && !fd.TypeRef.IsArray {
		return fmt.Sprintf("a relation: name the field that holds its key, %sId, when its target's key is a subject (D104)", entry)
	}
	var found *behaviorlayout.Column
	names := make([]string, 0, len(fields))
	for i := range fields {
		names = append(names, fields[i].Field)
		if fields[i].Field == entry {
			found = &fields[i]
		}
	}
	if found == nil {
		known := "none"
		if len(names) > 0 {
			known = strings.Join(names, ", ")
		}
		return fmt.Sprintf("which is not a field of %s (its fields: %s)", td.Name, known)
	}
	if fd != nil && (fd.TypeRef.IsArray || fd.TypeRef.IsMap) {
		return "a list: by names a field that holds one subject (" + subjectTypesNamed() + ")"
	}
	if fd != nil && fd.EncryptedAtRest {
		return "which is encrypted at rest: the core reads no value of it"
	}
	if !slices.Contains(subjectTypes, found.Type) {
		return fmt.Sprintf("a %s field of %s: by names a field that holds a subject (%s)", found.Type, td.Name, subjectTypesNamed())
	}
	return ""
}

// checkWritableBy warns when an operation of the API updates a field of a
// table that a by on the table names (D104): a Workflow transition's by,
// which names who may make the move, or a StateRules state's by, which
// names who may change the fields the state freezes. A by is only as strong
// as its field, and a caller who may write the field may name itself and
// then make the move or change what the state freezes. What the build sees
// of such an update is an operation, in any set but a @behaviorOperations
// one, that updates a row of the table (behaviorops.UpdatesARow), with an
// argument of a type of the API that is @source of the table and carries
// the field, not @virtual. The table is the @source target as the
// TypeScript frontend's compiler resolves it (Input.ExternalTypes), or the
// API's database's when the loader read it (Input.Database), so an API
// warns whether or not it serves the table's behavior operations. A create
// is not warned of: whoever creates a row names whom its fields hold.
// createdBy is never warned of: no input carries it, and the repositories
// stamp it. Nor is a field the table's Constants lists in fields: a
// constant changes for no update, so naming oneself in it is no way past
// by. It is a warning, not a refusal: the escape hatch is the author's, and
// the docs and the agent skill say what the build cannot see (an input of a
// plain type, code that calls the repository's update, raw SQL, an API in a
// data format that reads no database). One warning is given per operation,
// table, use and field, in set, operation, argument and use order (the
// table's list, its implements uses before its field uses), each use's
// fields in config order.
func checkWritableBy(schema *ir.Schema, in Input, r *Result) {
	uses := map[*ir.TypeDef][]byUse{}
	for _, set := range schema.OperationSets {
		if set == nil || set.IsBehaviorOperations() {
			continue
		}
		for _, op := range set.Operations {
			if op == nil || strings.EqualFold(op.HTTPMethod, "GET") {
				continue
			}
			warned := map[string]bool{}
			for _, arg := range op.Arguments {
				if arg == nil {
					continue
				}
				view, td := sourceTableOf(schema, in, arg.TypeRef.Name)
				if td == nil || !behaviorops.UpdatesARow(op, td) {
					continue
				}
				byUses, read := uses[td]
				if !read {
					byUses = writableByUses(td)
					uses[td] = byUses
				}
				for _, use := range byUses {
					for _, field := range use.fields {
						fd := sqlutil.FieldNamed(view, field)
						at := td.Name + "." + use.id + "." + field
						if fd == nil || fd.Virtual || warned[at] {
							continue
						}
						warned[at] = true
						if use.behavior == stateRulesBehavior {
							states := use.moves[field]
							verb := "freezes"
							if len(states) > 1 {
								verb = "freeze"
							}
							r.warnf("", "%s: %s.%s takes %s.%s (%s is @source(%s)), and %s's StateRules names %s in by for %s: a caller who may call %s can set %s to itself and then change the fields %s %s. Keep %s out of the operation's input, or freeze it: list it in the fields of %s's Constants (D104)",
								schema.Name, set.Name, op.Name, td.Name, field, view.Name, td.Name, td.Name, field, joinAnd(states), op.Name, field, joinAnd(states), verb, field, td.Name)
							continue
						}
						r.warnf("", "%s: %s.%s takes %s.%s (%s is @source(%s)), and the Workflow on %s.%s names %s in by for %s: a caller who may call %s can set %s to itself and then make that move. Keep %s out of the operation's input, or freeze it: list it in the fields of %s's Constants (D104)",
							schema.Name, set.Name, op.Name, td.Name, field, view.Name, td.Name, td.Name, use.id, field, joinAnd(use.moves[field]), op.Name, field, field, td.Name)
					}
				}
			}
		}
	}
}

// byUse is a use of a table whose by names own fields (D104): a Workflow
// on a field, whose id is the field and whose moves are each field's
// transitions, "<from> to <to>" in transition order; or a StateRules,
// whose moves are each field's states, in config order. fields are the
// fields in config order.
type byUse struct {
	id       string
	behavior string
	fields   []string
	moves    map[string][]string
}

// add records that the use's by names entry for the move or state what.
func (u *byUse) add(entry, what string) {
	if !slices.Contains(u.fields, entry) {
		u.fields = append(u.fields, entry)
	}
	if !slices.Contains(u.moves[entry], what) {
		u.moves[entry] = append(u.moves[entry], what)
	}
}

// writableByUses are td's uses whose by name a field an input may carry, in
// the table's list order (D104): each Workflow use on a field, by its
// transitions' by, and each StateRules use, by its states' by; every field
// a by names but createdBy, which the repositories stamp, and a field the
// table's Constants lists in fields, which no update changes.
func writableByUses(td *ir.TypeDef) []byUse {
	constant := map[string]bool{}
	for _, ref := range td.ImplementsBehaviors() {
		if ref.Name != constantsBehavior || len(ref.Config) == 0 {
			continue
		}
		var config struct {
			Fields []string `json:"fields"`
		}
		if json.Unmarshal(ref.Config, &config) == nil {
			for _, field := range config.Fields {
				constant[field] = true
			}
		}
	}
	writable := func(entry string) bool { return entry != sqlutil.AuditCreatedBy && !constant[entry] }
	var uses []byUse
	for _, ref := range td.TableBehaviors() {
		if len(ref.Config) == 0 {
			continue
		}
		use := byUse{id: ref.UseID(), behavior: ref.Name, moves: map[string][]string{}}
		switch {
		case ref.Name == workflowBehavior && ref.FieldAnchored():
			var config struct {
				Transitions []struct {
					From string   `json:"from"`
					To   string   `json:"to"`
					By   []string `json:"by"`
				} `json:"transitions"`
			}
			if json.Unmarshal(ref.Config, &config) != nil {
				continue
			}
			for _, transition := range config.Transitions {
				for _, entry := range transition.By {
					if writable(entry) {
						use.add(entry, transition.From+" to "+transition.To)
					}
				}
			}
		case ref.Name == stateRulesBehavior && !ref.FieldAnchored():
			var config struct {
				States map[string]struct {
					By []string `json:"by"`
				} `json:"states"`
			}
			if json.Unmarshal(ref.Config, &config) != nil {
				continue
			}
			for _, state := range slices.Sorted(maps.Keys(config.States)) {
				for _, entry := range config.States[state].By {
					if writable(entry) {
						use.add(entry, state)
					}
				}
			}
		}
		if len(use.fields) > 0 {
			uses = append(uses, use)
		}
	}
	return uses
}

// sourceTableOf is the type of schema named name when it is a @source view,
// and its target as the build sees it: resolved as checkSourceProjections
// resolves it (resolveSourceType), else the type of that name of the API's
// database when the loader read it and the target names it. Both are nil
// for any other type, and for a target neither holds.
func sourceTableOf(schema *ir.Schema, in Input, name string) (view, table *ir.TypeDef) {
	view = schema.Types[name]
	if view == nil {
		view = schema.Inputs[name]
	}
	if view == nil || view.Source == nil {
		return nil, nil
	}
	if td, ok := resolveSourceType(schema, in, view.Source.Target); ok && td != nil {
		return view, td
	}
	service, typeName := splitTarget(view.Source.Target)
	if db := in.Database; db != nil && (service == "" || service == db.Name) {
		if td := db.Types[typeName]; td != nil {
			return view, td
		}
	}
	return nil, nil
}

// warnOverrideCovered warns when every permission set's routes require
// covers the overridePermission of a Workflow use on a field of td (D104):
// a caller the routes admit holds one of them, and the core matches the
// overridePermission as it matches any permission, a held one covering
// itself and each one nested under it (questions covers
// questions.moderate), so every such caller moves the use's rows past every
// transition's by, which names no one for them. It is a warning, not a
// refusal: a set for moderators alone is the author's to serve.
func warnOverrideCovered(set *ir.OperationSet, td *ir.TypeDef, r *Result) {
	if len(set.Permissions) == 0 {
		return
	}
	for _, ref := range td.FieldBehaviors() {
		if ref.Name != workflowBehavior || len(ref.Config) == 0 {
			continue
		}
		var config struct {
			OverridePermission string `json:"overridePermission"`
		}
		if json.Unmarshal(ref.Config, &config) != nil || config.OverridePermission == "" {
			continue
		}
		override := config.OverridePermission
		every := true
		for _, required := range set.Permissions {
			every = every && covers(required, override)
		}
		if !every {
			continue
		}
		held := set.Permissions[0] + ", which covers"
		if len(set.Permissions) > 1 {
			held = "one of " + strings.Join(set.Permissions, ", ") + ", each of which covers"
		}
		r.warnf("", "%s: every caller its routes admit holds %s %s, the overridePermission of the Workflow on %s.%s, so each moves its rows past every transition's by, which names no one for them; require a permission that does not cover %s, or name another overridePermission (D104)",
			set.Name, held, override, td.Name, ref.Field, override)
	}
}
