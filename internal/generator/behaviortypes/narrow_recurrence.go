package behaviortypes

import (
	"fmt"
	"slices"
)

// recurrenceAnchors are Recurrence's anchors (D152), in the declaration's
// order.
var recurrenceAnchors = []string{"calendar", "afterDone", "relative"}

// recurrenceMembers are setRecurrence's members that name a rule, by the
// anchors that take each: a config that takes none of a member's anchors
// takes no member of it (#601).
var recurrenceMembers = map[string][]string{
	"rule":   {"calendar"},
	"start":  {"calendar", "afterDone"},
	"every":  {"afterDone", "relative"},
	"offset": {"relative"},
}

// Recurrence (D152, #601): the config's anchors are the alias <Base>Anchor
// (FilingRecurrenceAnchor), which setRecurrence's anchor names, and
// setRecurrence takes no member only anchors the config leaves out take
// (rule, start, every, offset). Only the input narrows: a row's recurrence
// set under an anchor a new config dropped still reads. nextDueAt and
// lastDoneAt are epoch milliseconds, absent until a row has an occurrence
// and a completion.
func narrowRecurrence(n *Narrowing, config map[string]any, target Target) error {
	taken := stringsOf(config["anchors"])
	var anchors []string
	for _, anchor := range recurrenceAnchors {
		if slices.Contains(taken, anchor) {
			anchors = append(anchors, anchor)
		}
	}
	anchor := target.Base + "Anchor"
	n.Aliases = append(n.Aliases, Alias{Name: anchor, Doc: fmt.Sprintf("An anchor %s's Recurrence takes for a row's recurrence.", An(target.Base)), Schema: EnumOf(anchors)})
	if err := setOperationProperty(n.Params, "setRecurrence", nil, "anchor", ref(anchor, "")); err != nil {
		return err
	}
	for _, member := range SortedKeys(recurrenceMembers) {
		if slices.ContainsFunc(recurrenceMembers[member], func(a string) bool { return slices.Contains(anchors, a) }) {
			continue
		}
		if err := deleteProperty(n.Params["setRecurrence"], member); err != nil {
			return fmt.Errorf("operation setRecurrence: %w", err)
		}
	}
	// A create's recurrence (#729) takes what setRecurrence takes.
	if n.CreateParams != nil {
		recurrence, err := at(n.CreateParams, "properties", "recurrence")
		if err != nil {
			return fmt.Errorf("its create parameters: %w", err)
		}
		if err := setProperty(recurrence, "anchor", ref(anchor, "")); err != nil {
			return fmt.Errorf("its create parameters: %w", err)
		}
		for _, member := range SortedKeys(recurrenceMembers) {
			if slices.ContainsFunc(recurrenceMembers[member], func(a string) bool { return slices.Contains(anchors, a) }) {
				continue
			}
			if err := deleteProperty(recurrence, member); err != nil {
				return fmt.Errorf("its create parameters: %w", err)
			}
		}
	}
	return setFields(n,
		typedField{"nextDueAt", integer(), false},
		typedField{"lastDoneAt", integer(), false},
	)
}
