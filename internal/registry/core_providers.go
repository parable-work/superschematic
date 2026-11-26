package registry

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The runtime extension points (D159): @providers on an API's @envVars
// class names its provider slots, and a feature that needs a runtime
// provider registers itself with the interface it needs, so the stack
// refuses an environment that uses the feature with no provider of that
// interface.

// configFieldName is the form of an @envVars field a slot names.
var configFieldName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// providersDecorator is @providers({ SLOT: "interface", ... }) of
// @superschematic/schema-config, on the @envVars class of an API. Apply
// reads the map; the loader checks it against the class's fields and the
// provider packages the config lists.
func providersDecorator() DecoratorSpec {
	return DecoratorSpec{
		Name: "providers", Packages: []string{pkgSchemaConfig}, Target: TargetType,
		Kinds: []string{string(ir.SchemaKindAPI)},
		Apply: func(n Node, args []any, _ Site) error {
			const shape = `@providers({ SLOT: "channel" | "credentials" | "sandbox" | "model" | "files", ... })`
			if n.Type.ProviderSlots != nil {
				return fmt.Errorf("a class takes one @providers")
			}
			if len(args) != 1 {
				return fmt.Errorf("@providers on %s takes one object: %s", n.Type.Name, shape)
			}
			obj, ok := args[0].(map[string]any)
			if !ok || len(obj) == 0 {
				return ArgErrorf(0, "@providers on %s takes an object of one slot or more: %s", n.Type.Name, shape)
			}
			slots := map[string]string{}
			for _, slot := range sortedKeys(obj) {
				iface, ok := obj[slot].(string)
				if !ok {
					return ArgErrorf(0, "@providers on %s: slot %s takes an interface's name as a string; got %v", n.Type.Name, slot, obj[slot])
				}
				if !configFieldName.MatchString(slot) {
					return ArgErrorf(0, "@providers on %s: slot %q is a config field's name, upper-case letters, digits and underscores", n.Type.Name, slot)
				}
				if _, ok := ir.RuntimeContractOf(iface); !ok {
					return ArgErrorf(0, "@providers on %s: slot %s takes %q, which is no runtime interface; it is one of %s (D159)", n.Type.Name, slot, iface, strings.Join(ir.RuntimeInterfaceNames(), ", "))
				}
				slots[slot] = iface
			}
			n.Type.ProviderSlots = slots
			return nil
		},
	}
}

// RuntimeFeature is a feature of the platform that needs a runtime
// provider (D159): notifications need a channel adapter, attachments a
// file processor, agents a sandbox and a model provider. A feature
// registers itself once per interface it needs, and Uses says whether an
// API service uses it. The stack refuses an environment that runs a
// deployable of such an API whose config names no provider of Interface.
type RuntimeFeature struct {
	// Extension is the registering extension's Name(), empty for the core.
	Extension string
	// Name names the feature in a refusal: "notifications".
	Name string
	// Interface is the runtime interface the feature needs.
	Interface string
	// Uses reports whether the API service uses the feature, from its
	// schema and the DB schemas it connects to.
	Uses func(in RuntimeFeatureInput) bool
}

// RuntimeFeatureInput is what RuntimeFeature.Uses reads.
type RuntimeFeatureInput struct {
	// API is the API service's schema.
	API *ir.Schema
	// Databases are the DB schemas the API connects to: its authDb and its
	// DB dependencies, by name.
	Databases map[string]*ir.Schema
}

// RegisterRuntimeFeature adds a feature that needs a runtime provider.
// A feature names an interface the core defines and has a Uses, and one
// name and interface is registered once.
func (r *Registry) RegisterRuntimeFeature(f RuntimeFeature) error {
	if err := r.registrable("runtime feature " + f.Name); err != nil {
		return err
	}
	if f.Name == "" {
		return fmt.Errorf("registry: a runtime feature needs a name")
	}
	if _, ok := ir.RuntimeContractOf(f.Interface); !ok {
		return fmt.Errorf("registry: runtime feature %s needs %q, which is no runtime interface; it is one of %s", f.Name, f.Interface, strings.Join(ir.RuntimeInterfaceNames(), ", "))
	}
	if f.Uses == nil {
		return fmt.Errorf("registry: runtime feature %s has no Uses", f.Name)
	}
	for _, existing := range r.runtimeFeatures {
		if existing.Name == f.Name && existing.Interface == f.Interface {
			return fmt.Errorf("registry: runtime feature %s is already registered for %s", f.Name, f.Interface)
		}
	}
	r.runtimeFeatures = append(r.runtimeFeatures, f)
	r.noteExtension(f.Extension)
	return nil
}

// RuntimeFeatures returns the registered runtime features in registration
// order.
func (r *Registry) RuntimeFeatures() []RuntimeFeature {
	return append([]RuntimeFeature(nil), r.runtimeFeatures...)
}

// RuntimeFeaturesUsed returns the runtime features the API service in
// uses, each with the interface it needs, sorted by feature and
// interface. Nil for none.
func (r *Registry) RuntimeFeaturesUsed(in RuntimeFeatureInput) []ir.RuntimeFeatureUse {
	if in.API == nil {
		return nil
	}
	var used []ir.RuntimeFeatureUse
	for _, f := range r.runtimeFeatures {
		if f.Uses(in) {
			used = append(used, ir.RuntimeFeatureUse{Feature: f.Name, Interface: f.Interface})
		}
	}
	slices.SortFunc(used, func(a, b ir.RuntimeFeatureUse) int {
		if c := strings.Compare(a.Feature, b.Feature); c != 0 {
			return c
		}
		return strings.Compare(a.Interface, b.Interface)
	})
	return used
}
