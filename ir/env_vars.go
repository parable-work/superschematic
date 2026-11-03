package ir

import (
	"fmt"
	"strings"
)

// EnvVarsType returns the schema's @envVars class, the service's config
// (D94): the one no other @envVars class of the schema extends, directly or
// through the classes between them. Its Fields are its own and those of
// every class it extends, as every type's are, so they hold every @envVars
// field the schema declares: each other @envVars class is one it extends.
// The Go, TypeScript and Rust loaders, values-schema.json, a stack, the
// API's handle and the secrets its config names all read this one class.
//
// It is nil, with no error, for a schema that declares no @envVars class.
// Two or more that no other extends make no one config, and the error, an
// *EnvVarsTypesError, names them. The answer is the schema's alone: it never
// depends on the order a walk of Types takes.
func (s *Schema) EnvVarsType() (*TypeDef, error) {
	if s == nil {
		return nil, nil
	}
	var classes []*TypeDef
	for _, name := range sortedStringMapKeys(s.Types) {
		if td := s.Types[name]; td != nil && td.EnvVars {
			classes = append(classes, td)
		}
	}
	if len(classes) == 0 {
		return nil, nil
	}
	extended := map[string]bool{}
	for _, td := range classes {
		for _, base := range s.basesOf(td) {
			extended[base] = true
		}
	}
	var leaves []*TypeDef
	for _, td := range classes {
		if !extended[td.Name] {
			leaves = append(leaves, td)
		}
	}
	switch len(leaves) {
	case 1:
		return leaves[0], nil
	case 0:
		// Each extends another: an extends cycle, which only a data form
		// can write.
		return nil, &EnvVarsTypesError{Schema: s.Name, Types: classes, Cycle: true}
	}
	return nil, &EnvVarsTypesError{Schema: s.Name, Types: leaves}
}

// basesOf names the classes of the schema td extends, nearest first: its
// Extends, that class's, and so on while each is a type of the schema. A
// base from another service ends the chain; its fields are already td's.
func (s *Schema) basesOf(td *TypeDef) []string {
	var bases []string
	seen := map[string]bool{td.Name: true}
	for base := td.Extends; base != "" && !seen[base]; {
		seen[base] = true
		bases = append(bases, base)
		next := s.Types[base]
		if next == nil {
			break
		}
		base = next.Extends
	}
	return bases
}

// EnvVarsTypesError reports a schema whose @envVars classes make no one
// config (D94): two or more of them no other extends, or, from a data form,
// classes that extend one another.
type EnvVarsTypesError struct {
	// Schema is the service's name.
	Schema string

	// Types are the @envVars classes no other extends, sorted by name; with
	// Cycle, every @envVars class of the schema.
	Types []*TypeDef

	// Cycle reports that every @envVars class extends another.
	Cycle bool
}

func (e *EnvVarsTypesError) Error() string {
	names := make([]string, len(e.Types))
	for i, td := range e.Types {
		names[i] = td.Name
		if td.Owner != "" {
			names[i] += " (" + td.Owner + ")"
		}
	}
	listed := joinAnd(names)
	switch {
	case e.Cycle:
		return fmt.Sprintf("the @envVars classes of %s, %s, extend one another, so none is its config; a service's config is the one @envVars class no other extends (D94)", e.Schema, listed)
	case len(e.Types) == 2:
		return fmt.Sprintf("%s has two @envVars classes, %s, and neither extends the other; a service's config is the one @envVars class no other extends, which holds the fields of every class it extends, so make one of them extend the other, or remove @envVars from one (D94)", e.Schema, listed)
	}
	return fmt.Sprintf("%s has %d @envVars classes that no other extends, %s; a service's config is the one @envVars class no other extends, which holds the fields of every class it extends, so make them one chain, each extending the next, or remove @envVars from all but one (D94)", e.Schema, len(e.Types), listed)
}

// joinAnd joins items as prose: "a", "a and b", "a, b and c".
func joinAnd(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
