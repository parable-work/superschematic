package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// reactionsBehavior is the name of the core's Reactions.
const reactionsBehavior = "Reactions"

// workflowEnteredAt is the declared field of Workflow that every move of
// its status moves.
const workflowEnteredAt = "enteredAt"

// reactionsRule is one rule of a Reactions config as the build reads it.
type reactionsRule struct {
	When struct {
		Enters  *string `json:"enters"`
		Changes *struct {
			Field string `json:"field"`
		} `json:"changes"`
		AllTerminal *reactionsTerminal `json:"allTerminal"`
		AnyTerminal *reactionsTerminal `json:"anyTerminal"`
		Holds       *string            `json:"holds"`
		Revised     *struct {
			Link string `json:"link"`
		} `json:"revised"`
	} `json:"when"`
	Then struct {
		Transition string  `json:"transition"`
		Link       *string `json:"link"`
	} `json:"then"`
}

// reactionsTerminal is an allTerminal or anyTerminal rule's linking table
// and its relation to the table.
type reactionsTerminal struct {
	Schema string `json:"schema"`
	Link   string `json:"link"`
}

// checkReactionsConfig holds the config of a Reactions use a table
// implements to the table and the tables of its database (D103), in the
// core's words where the core has them (superschematic_rules_behaviors::
// reactions), each problem `type <T>: behavior Reactions config: <reason>`
// and, like the core, the first reported: the status, then each changes
// rule's field, then each holds and revised rule, which wait for step 4,
// then each rule's read of the status, then each rule's cross-table facts,
// its when's linking table and relation and its then's target. These are
// the rules of Reactions' config that read the IR, which the build checks
// before any command: a field a changes rule names is a field of the table
// a write's image carries, an allTerminal or anyTerminal rule's linking
// table is a table of the database whose to-one relation points at this
// one and which composes one Workflow, and a then's link is a to-one
// relation whose target composes one Workflow that has its state. Its
// other rules (the states a rule names, the transitions that lead there,
// the cycle) are the core's define check's, at the first command. A config
// the configSchema refuses is reported by checkTypeBehaviors and left alone
// here.
func checkReactionsConfig(schema *ir.Schema, td *ir.TypeDef, ref ir.BehaviorRef, r *Result) {
	var config struct {
		Status *string         `json:"status"`
		Rules  []reactionsRule `json:"rules"`
	}
	if len(ref.Config) == 0 || json.Unmarshal(ref.Config, &config) != nil {
		return
	}
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "type %s: behavior %s config: %s", td.Name, ref.Name, fmt.Sprintf(format, args...))
	}
	workflows := workflowUses(td)
	listed := "none"
	if len(workflows) > 0 {
		listed = strings.Join(workflows, ", ")
	}
	if config.Status != nil && !slices.Contains(workflows, *config.Status) {
		fail("status %q is not a Workflow of the type (its Workflows: %s)", *config.Status, listed)
		return
	}
	for i, rule := range config.Rules {
		if rule.When.Changes == nil {
			continue
		}
		if why := reactionsChangesField(schema, td, rule.When.Changes.Field); why != "" {
			fail("rule %d: when.changes names %s%s", i+1, rule.When.Changes.Field, why)
			return
		}
	}
	for i, rule := range config.Rules {
		switch {
		case rule.When.Holds != nil:
			fail("rule %d: when.holds names rollup %s: a rule that hears a rollup waits for Rollups on the core (D103's order of the work, step 4)", i+1, *rule.When.Holds)
			return
		case rule.When.Revised != nil:
			fail("rule %d: when.revised names link %s: a rule that hears a revision or a release waits for Revisions and Branches on the core (D103's order of the work, step 4)", i+1, rule.When.Revised.Link)
			return
		}
	}
	if config.Status == nil {
		for i, rule := range config.Rules {
			var places []string
			if rule.When.Enters != nil {
				places = append(places, "when.enters")
			}
			if rule.Then.Link == nil {
				places = append(places, "then")
			}
			for _, place := range places {
				switch {
				case len(workflows) == 0:
					fail("rule %d: %s reads the status of Workflow, which the type does not list", i+1, place)
					return
				case len(workflows) > 1:
					fail("rule %d: %s reads the status of Workflow, and the type has several (%s): status names the field whose Workflow it reads", i+1, place, listed)
					return
				}
			}
		}
	}
	for i, rule := range config.Rules {
		for _, form := range []struct {
			name     string
			terminal *reactionsTerminal
		}{{"allTerminal", rule.When.AllTerminal}, {"anyTerminal", rule.When.AnyTerminal}} {
			if form.terminal == nil {
				continue
			}
			if why := reactionsLinking(schema, td, form.name, form.terminal); why != "" {
				fail("rule %d: %s", i+1, why)
				return
			}
		}
		if rule.Then.Link == nil {
			continue
		}
		if why := reactionsThenLink(schema, td, *rule.Then.Link, rule.Then.Transition); why != "" {
			fail("rule %d: %s", i+1, why)
			return
		}
	}
}

// workflowUses are the ids of td's Workflow uses on the table, in list
// order: its anchor fields.
func workflowUses(td *ir.TypeDef) []string {
	var out []string
	for _, use := range td.TableBehaviors() {
		if use.Name == workflowBehavior {
			out = append(out, use.UseID())
		}
	}
	return out
}

// reactionsChangesField is why a changes rule cannot name name on td, the
// reason after "when.changes names <name>", or "" when it names a field of
// the table a write's image carries: an own field, a to-one relation by
// its name, or a field anchor, whose value field it hears.
func reactionsChangesField(schema *ir.Schema, td *ir.TypeDef, name string) string {
	if strings.Contains(name, ".") {
		return ": a table's rule names a field of the table by its name, and a member path below one, or a field a behavior computes, waits for a later step (D103)"
	}
	// The own fields as a rule names them: a relation by its name, not
	// the column's <relation>Id the core's spec holds.
	relations := map[string]string{}
	for _, fd := range td.Fields {
		if fd != nil && !fd.Owned() && !fd.TypeRef.IsArray && sqlutil.IsTableRelation(schema, fd) {
			relations[fd.Name+"Id"] = fd.Name
		}
	}
	var names []string
	var named *behaviorlayout.Column
	own := behaviorlayout.OwnFields(schema, td)
	for i, c := range own {
		field := c.Field
		if relation, ok := relations[c.Field]; ok {
			field = relation
		}
		names = append(names, field)
		if field == name {
			named = &own[i]
		}
	}
	for _, use := range td.FieldBehaviors() {
		names = append(names, use.Field)
	}
	if named != nil {
		if !slices.Contains(behaviorlayout.ImageColumns(schema, td), named.Column) {
			return fmt.Sprintf(", a %s field, which a write's image leaves out: a rule hears the fields a write carries (D103)", named.Type)
		}
		return ""
	}
	if _, anchored := td.FieldBehavior(name); anchored {
		return ""
	}
	// A Workflow's enteredAt moves with every move of its status. The
	// field is named by the layout rule, since the loader adds it after
	// this check's first pass.
	for _, use := range td.FieldBehaviors() {
		if use.Name == workflowBehavior && name == behaviorlayout.OwnedFieldName(use.Field, workflowEnteredAt, false) {
			return fmt.Sprintf(", which every move of the status moves; a rule on %s hears those", use.Field)
		}
	}
	known := "none"
	if len(names) > 0 {
		known = strings.Join(names, ", ")
	}
	return fmt.Sprintf(", which is not a field of %s (%s)", td.Name, known)
}

// reactionsLinking is why an allTerminal or anyTerminal rule (form) cannot
// read its linking table on td, or "": the table is one of the database,
// its link is a to-one relation of it that points at td, and it composes
// one Workflow, whose terminal states the rule reads.
func reactionsLinking(schema *ir.Schema, td *ir.TypeDef, form string, terminal *reactionsTerminal) string {
	linking := schema.Types[terminal.Schema]
	if linking == nil || linking.Role != ir.RoleDBTable || linking.JsonField || linking.IsEngineType() {
		return fmt.Sprintf("when.%s names %s, which is not a table of %s", form, terminal.Schema, schema.Name)
	}
	link := fieldNamedIn(linking, terminal.Link)
	switch {
	case link != nil && !link.Owned() && sqlutil.IsTableRelation(schema, link) && link.TypeRef.IsArray:
		return fmt.Sprintf("when.%s names link %s of %s, which holds a list of targets, and the rule reads the one instance it points at", form, terminal.Link, terminal.Schema)
	case link == nil || link.Owned() || !sqlutil.IsTableRelation(schema, link) || link.TypeRef.Name != td.Name:
		return fmt.Sprintf("when.%s names link %s of %s, which has no such link to %s", form, terminal.Link, terminal.Schema, td.Name)
	}
	switch workflows := workflowUses(linking); len(workflows) {
	case 0:
		return fmt.Sprintf("when.%s names %s, which does not compose Workflow, so its rows have no terminal state", form, terminal.Schema)
	case 1:
		return ""
	default:
		return fmt.Sprintf("when.%s names %s, which has several Workflows (%s): a rule reads a linking table's one Workflow", form, terminal.Schema, strings.Join(workflows, ", "))
	}
}

// reactionsThenLink is why a then that moves the row td's to-one relation
// link points at to transition cannot, or "": link is a to-one relation of
// td, whose target composes one Workflow, which has the state.
func reactionsThenLink(schema *ir.Schema, td *ir.TypeDef, link, transition string) string {
	field := fieldNamedIn(td, link)
	if field == nil || field.Owned() || field.TypeRef.IsArray || !sqlutil.IsTableRelation(schema, field) {
		var relations []string
		for _, fd := range td.Fields {
			if fd != nil && !fd.Owned() && !fd.TypeRef.IsArray && sqlutil.IsTableRelation(schema, fd) {
				relations = append(relations, fd.Name)
			}
		}
		known := "none"
		if len(relations) > 0 {
			known = strings.Join(relations, ", ")
		}
		return fmt.Sprintf("then.link %s is not a to-one relation of %s (%s)", link, td.Name, known)
	}
	target := schema.Types[field.TypeRef.Name]
	var uses []ir.BehaviorRef
	for _, use := range target.TableBehaviors() {
		if use.Name == workflowBehavior {
			uses = append(uses, use)
		}
	}
	switch len(uses) {
	case 0:
		return fmt.Sprintf("then.link %s points at %s, which does not compose Workflow", link, target.Name)
	case 1:
	default:
		return fmt.Sprintf("then.link %s points at %s, which has several Workflows (%s): a rule moves a table's one Workflow", link, target.Name, strings.Join(workflowUses(target), ", "))
	}
	var flow struct {
		States []string `json:"states"`
	}
	if json.Unmarshal(uses[0].Config, &flow) != nil || len(flow.States) == 0 || slices.Contains(flow.States, transition) {
		return ""
	}
	return fmt.Sprintf("then.transition %q is not a state of %s's Workflow (%s)", transition, target.Name, strings.Join(flow.States, ", "))
}

// fieldNamedIn is td's field named name, or nil.
func fieldNamedIn(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd != nil && fd.Name == name {
			return fd
		}
	}
	return nil
}
