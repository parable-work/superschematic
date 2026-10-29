package ir

import (
	"encoding/json"
	"fmt"
)

// Deriver is a deriver an API service declares with `@deriver` on a class
// of its schema: a deployable that fills the Derived fields of the engine
// types of the API's engine database (D73, D74). It pulls the fields that
// are stale (staleDerivations), hands each to the project's generated
// Derivers interface, which calls a model, and settles the answers
// (settleDerivations). It runs until it is stopped, as a worker does, and
// claims from no queue. The class's name is the deriver's, and the class
// holds nothing else, so a deriver is no type: the loaders record it in
// Schema.Derivers and in no other place, as they do a worker (Worker).
type Deriver struct {
	// Name is the class's name: the member of the API's generated Derivers
	// that holds the methods of its fields.
	Name string `json:"name" yaml:"name"`

	// Comment stores the node-attached comment of the class declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Schemas are the engine types whose Derived fields the deriver fills,
	// types of the engine database the API connects to that compose
	// Derived. Empty is every such type of that database.
	Schemas []string `json:"schemas,omitempty" yaml:"schemas,omitempty"`

	// Concurrency is how many answers an instance runs at a time. Zero is
	// DefaultDeriverConcurrency; an environment's settings change it.
	Concurrency int `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`

	// BatchSize is how many stale fields a pull asks for, 1 to
	// MaxDeriverBatchSize. Zero is DefaultDeriverBatchSize.
	BatchSize int `json:"batchSize,omitempty" yaml:"batchSize,omitempty"`
}

// The defaults and the limits of a deriver that leaves an argument out
// (D74). They are the reference loops' (runDeriver, derive::run_deriver).
const (
	// DefaultDeriverConcurrency is how many answers an instance runs at a
	// time.
	DefaultDeriverConcurrency = 4

	// DefaultDeriverBatchSize is how many stale fields a pull asks for.
	DefaultDeriverBatchSize = 50

	// MaxDeriverBatchSize is the most stale fields a pull may ask for.
	MaxDeriverBatchSize = 100

	// DefaultDeriverInstances is how many instances of a deriver an
	// environment runs unless its settings say otherwise.
	DefaultDeriverInstances = 1
)

// DerivedBehavior is the name of the core behavior whose fields a deriver
// fills (D73).
const DerivedBehavior = "Derived"

// Deriver returns the deriver named name, or nil.
func (s *Schema) Deriver(name string) *Deriver {
	for _, deriver := range s.Derivers {
		if deriver != nil && deriver.Name == name {
			return deriver
		}
	}
	return nil
}

// DeriverDeployableName names the deployable of deriver of the API
// service api as a worker's is named (WorkerDeployableName): the API's
// name, a hyphen, and the deriver's name in kebab case
// (`shop-cart-triage-tickets`).
func DeriverDeployableName(api, deriver string) string {
	return JobDeployableName(api, deriver)
}

// DerivedConfig is the part of a type's Derived config a deriver reads
// (D73): the own fields a model fills, each with the fields it is derived
// from, and the permission a deriver holds to pull and settle them. The
// engine checks the rest of the config when it defines the type.
type DerivedConfig struct {
	// Fields are the derived fields, sorted by name.
	Fields []DerivedField

	// Permission is the permission a deriver needs.
	Permission string
}

// DerivedField is one field of a Derived config.
type DerivedField struct {
	// Name is the own field the model fills.
	Name string

	// From are the own fields it is derived from, in the config's order.
	From []string

	// Instruction and Model are the config's instruction and model label.
	Instruction string
	Model       string
}

// ComposesDerived reports whether td composes the Derived behavior.
func (td *TypeDef) ComposesDerived() bool {
	if td == nil {
		return false
	}
	for _, b := range td.Behaviors {
		if b.Name == DerivedBehavior {
			return true
		}
	}
	return false
}

// DerivedConfig reads td's Derived config. It returns nil and no error
// when td does not compose Derived.
func (td *TypeDef) DerivedConfig() (*DerivedConfig, error) {
	if td == nil {
		return nil, nil
	}
	for _, b := range td.Behaviors {
		if b.Name != DerivedBehavior {
			continue
		}
		var raw struct {
			Fields map[string]struct {
				From        []string `json:"from"`
				Instruction string   `json:"instruction"`
				Model       string   `json:"model"`
			} `json:"fields"`
			Permission string `json:"permission"`
		}
		if len(b.Config) > 0 {
			if err := json.Unmarshal(b.Config, &raw); err != nil {
				return nil, fmt.Errorf("type %s: Derived config: %w", td.Name, err)
			}
		}
		config := &DerivedConfig{Permission: raw.Permission}
		for _, name := range sortedStringMapKeys(raw.Fields) {
			field := raw.Fields[name]
			config.Fields = append(config.Fields, DerivedField{Name: name, From: field.From, Instruction: field.Instruction, Model: field.Model})
		}
		return config, nil
	}
	return nil, nil
}
