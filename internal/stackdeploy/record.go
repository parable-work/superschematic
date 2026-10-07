package stackdeploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Bootstrap's last step (docs/stack-model.md, section 7.3, step 5; D47): a
// target's bootstrap returns values only the cloud knows, such as the GCP
// project's number, and the core records each in the schema source, in the
// target values of the environment whose declaration sets the value it
// belongs beside. A TypeScript schema changes in that property's text
// alone, through the edit the caller hands in; for a JSON or YAML schema
// bootstrap says what to add.

// SchemaSource is the stack's schema as the loader read it, and where its
// files are: each class's TypeDef names the file that declares it (Owner),
// relative to Dir, the stack service's directory.
type SchemaSource struct {
	Schema *ir.Schema
	Dir    string

	// EditTypeScript writes a value into a TypeScript schema file. The CLI
	// hands in the TypeScript reader's (tsreader.SetEnvironmentValue), as
	// it hands a deploy the Planner, so this package and the stack package
	// extensions import never link the compiler. Without it, a TypeScript
	// schema is changed by hand, as a JSON or YAML one is.
	EditTypeScript TypeScriptEdit
}

// TypeScriptEdit sets v's key to v.Value in the target values of an
// @environment class of the TypeScript schema file fileName, whose text is
// src. It returns the file's new text and the value the key held, with
// found false when the file set none, and src itself when the value
// matches. It refuses, with what to change by hand, a file it cannot edit
// in place.
type TypeScriptEdit func(fileName string, src []byte, v EnvironmentValue) (out []byte, previous string, found bool, err error)

// EnvironmentValue names one target value of an @environment class: the
// class, the key its decorator's object holds the target's values under
// (`gcp`), the value's key and value, and the key whose property a new one
// is written after. Its fields are tsreader.EnvironmentValue's, in order,
// so one converts to the other.
type EnvironmentValue struct {
	Class  string
	Target string
	Key    string
	Value  string
	Beside string
}

// RecordOutcome is what bootstrap did with a value in the schema.
type RecordOutcome string

const (
	// ValueRecorded is a value the schema had none of, which bootstrap
	// wrote.
	ValueRecorded RecordOutcome = "recorded"

	// ValueUpdated is a value bootstrap wrote over another the schema
	// held.
	ValueUpdated RecordOutcome = "updated"

	// ValueMatches is a value the schema holds already: its file is
	// unchanged.
	ValueMatches RecordOutcome = "matches"

	// ValueByHand is a value bootstrap did not write: Hand says what to
	// change.
	ValueByHand RecordOutcome = "by hand"
)

// RecordedValue is what bootstrap did with one value its target returned.
type RecordedValue struct {
	registry.BootstrapValue

	// Environment is the environment whose declaration holds the value,
	// and File the schema file that declares it, relative to the stack
	// service's directory. Both are empty when bootstrap had no schema
	// source to find them in.
	Environment string
	File        string

	Outcome RecordOutcome

	// Previous is the value the schema held before an update.
	Previous string

	// Hand says what to change by hand, for ValueByHand.
	Hand string

	// Overriding are the environments between the bootstrapped one and
	// Environment that set the value too, nearest first: the nearest one's
	// value is what the bootstrapped environment resolves to.
	Overriding []string
}

// String is the line bootstrap prints for the value:
// `projectNumber 123456789012: recorded on Staging in src/stack.schema.ts`.
func (r RecordedValue) String() string {
	where := r.Environment
	if r.File != "" {
		where += " in " + r.File
	}
	var what string
	switch r.Outcome {
	case ValueRecorded:
		what = "recorded on " + where
	case ValueUpdated:
		what = fmt.Sprintf("updated from %s on %s", r.Previous, where)
	case ValueMatches:
		what = "matches " + where
	default:
		what = "not recorded: " + r.Hand
	}
	if len(r.Overriding) > 0 {
		what += fmt.Sprintf("; %s sets %s too, which overrides it", r.Overriding[0], r.Key)
	}
	return r.Key + " " + r.Value + ": " + what
}

// record records each value the target's bootstrap returned in the schema
// source, and returns what it did with each.
func (s *session) record(src *SchemaSource, values []registry.BootstrapValue) ([]RecordedValue, error) {
	var out []RecordedValue
	seen := map[string]bool{}
	for _, v := range values {
		if v.Key == "" || v.Beside == "" {
			return out, fmt.Errorf("the bootstrap of target %s returned the value %q with key %q and beside %q; a value names both", s.target.Name, v.Value, v.Key, v.Beside)
		}
		if seen[v.Key] {
			return out, fmt.Errorf("the bootstrap of target %s returned %s twice", s.target.Name, v.Key)
		}
		seen[v.Key] = true
		r, err := s.recordValue(src, v)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// recordValue records one value on the environment whose declaration sets
// v.Beside: the bootstrapped environment, or the nearest one up its
// extends chain.
func (s *session) recordValue(src *SchemaSource, v registry.BootstrapValue) (RecordedValue, error) {
	r := RecordedValue{BootstrapValue: v}
	target := s.env.Target
	if src == nil || src.Schema == nil {
		r.Outcome = ValueByHand
		r.Hand = fmt.Sprintf("no schema source to record it in; add %s: %s beside %s in the %s values of %s, or of the environment it inherits %s from",
			v.Key, jsonString(v.Value), v.Beside, target, s.env.Environment, v.Beside)
		return r, nil
	}
	st := ir.StackOf(src.Schema)
	if st == nil {
		return r, fmt.Errorf("record %s: stack %s declares no @stack class", v.Key, src.Schema.Name)
	}
	var decl *ir.Environment
	seen := map[string]bool{}
	for env := st.Environment(s.env.Environment); env != nil && !seen[env.Name]; env = st.Environment(env.Extends) {
		seen[env.Name] = true
		if _, ok := env.Values[v.Beside]; ok {
			decl = env
			break
		}
		if _, ok := env.Values[v.Key]; ok {
			r.Overriding = append(r.Overriding, env.Name)
		}
	}
	if decl == nil {
		return r, fmt.Errorf("record %s: neither environment %s nor any it extends sets %s, which %s is recorded beside", v.Key, s.env.Environment, v.Beside, v.Key)
	}
	if decl.Target != "" {
		target = decl.Target
	}
	r.Environment = decl.Name
	if td := src.Schema.Types[decl.Name]; td != nil {
		r.File = td.Owner
	}
	current, has := decl.Values[v.Key]
	matches := has && current == any(v.Value)
	byHand := func(format string, args ...any) (RecordedValue, error) {
		r.Outcome = ValueByHand
		r.Hand = fmt.Sprintf(format, args...)
		return r, nil
	}
	switch {
	case strings.HasSuffix(r.File, ".schema.ts") && src.EditTypeScript != nil:
		return s.recordTypeScript(src, r, target)
	case matches:
		r.Outcome = ValueMatches
		return r, nil
	case strings.HasSuffix(r.File, ".schema.json"):
		if has {
			return byHand("bootstrap edits no JSON schema; in %s, change %q in the environment values of %s from %v to %s", r.File, v.Key, decl.Name, current, jsonString(v.Value))
		}
		return byHand("bootstrap edits no JSON schema; in %s, add %q: %s beside %q in the environment values of %s", r.File, v.Key, jsonString(v.Value), v.Beside, decl.Name)
	case strings.HasSuffix(r.File, ".schema.yaml"), strings.HasSuffix(r.File, ".schema.yml"):
		if has {
			return byHand("bootstrap edits no YAML schema; in %s, change %s in the environment values of %s from %v to %s", r.File, v.Key, decl.Name, current, jsonString(v.Value))
		}
		return byHand("bootstrap edits no YAML schema; in %s, add %s: %s beside %s in the environment values of %s", r.File, v.Key, jsonString(v.Value), v.Beside, decl.Name)
	case strings.HasSuffix(r.File, ".schema.ts"):
		if has {
			return byHand("bootstrap was handed no TypeScript edit; in %s, change %s in the %s values of %s from %v to %s", r.File, v.Key, target, decl.Name, current, jsonString(v.Value))
		}
		return byHand("bootstrap was handed no TypeScript edit; in %s, add %s: %s beside %s in the %s values of %s", r.File, v.Key, jsonString(v.Value), v.Beside, target, decl.Name)
	}
	return byHand("the schema does not say which file declares %s; add %s: %s beside %s in its %s values", decl.Name, v.Key, jsonString(v.Value), v.Beside, target)
}

// recordTypeScript writes r's value into the TypeScript schema file that
// declares r.Environment with the caller's edit, which changes that
// property's text alone.
func (s *session) recordTypeScript(src *SchemaSource, r RecordedValue, target string) (RecordedValue, error) {
	path := filepath.Join(src.Dir, filepath.FromSlash(r.File))
	data, err := os.ReadFile(path)
	if err != nil {
		return r, fmt.Errorf("record %s: %w", r.Key, err)
	}
	out, previous, found, err := src.EditTypeScript(r.File, data, EnvironmentValue{
		Class: r.Environment, Target: target, Key: r.Key, Value: r.Value, Beside: r.Beside,
	})
	switch {
	case err != nil:
		r.Outcome, r.Hand = ValueByHand, err.Error()
		return r, nil
	case found && previous == r.Value:
		r.Outcome = ValueMatches
		return r, nil
	case found:
		r.Outcome, r.Previous = ValueUpdated, previous
	default:
		r.Outcome = ValueRecorded
	}
	info, err := os.Stat(path)
	if err != nil {
		return r, fmt.Errorf("record %s: %w", r.Key, err)
	}
	if err := os.WriteFile(path, out, info.Mode().Perm()); err != nil {
		return r, fmt.Errorf("record %s: %w", r.Key, err)
	}
	return r, nil
}

// jsonString writes s as a JSON string, which a YAML schema reads as the
// same string.
func jsonString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
