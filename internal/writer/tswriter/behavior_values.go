package tswriter

import (
	"sync"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// coreBehaviors is the core registry's behaviors, which the writer looks a
// use's declaration up in when its Context names no registry.
var coreBehaviors = sync.OnceValue(func() *registry.Registry {
	return registry.New(naming.Active())
})

// behavior returns the declaration of the behavior name: the Context's
// registry's, or the core's.
func (e *emitter) behavior(name string) (registry.Behavior, bool) {
	if e.ctx.Registry != nil {
		return e.ctx.Registry.Behavior(name)
	}
	return coreBehaviors().Behavior(name)
}

// valueFromCreate reports whether the behavior name's value field is one a
// create gives (registry.BehaviorField.FromCreate, D147), whose field type
// takes the value's type before its config: `name: Aliased<string, C>`.
func (e *emitter) valueFromCreate(name string) bool {
	behavior, ok := e.behavior(name)
	if !ok {
		return false
	}
	for _, declared := range behavior.Fields {
		if declared.Name == behavior.Value {
			return declared.FromCreate
		}
	}
	return false
}
