package stack

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The runtime providers of an environment (D159). An API's @envVars class
// names its provider slots, each a config field whose value names a
// runtime provider, one of the packages its config lists. The environment
// binds each slot as it binds any field, so environment.json shows which
// provider each deployable uses, and the provider's own settings are the
// fields named after the slot (ir.ProviderConfigField), each bound as any
// other: a literal, a parameter or, for a secret, the platform's secret.
// Swapping one provider for another is a change of the slot's value and
// of the settings the new one reads.

// resolveProviders reads, for each deployable that runs code, the provider
// each slot of the APIs it serves names, checks it against the API's
// providers and the deployable's bindings, and records it on the
// deployable. It then refuses a deployable whose API uses a feature that
// needs a runtime interface no slot of the API fills (checkCoverage).
func (r *resolver) resolveProviders() {
	for _, name := range sortedKeys(r.deployables) {
		d := r.deployables[name]
		if !d.res.Kind.RunsCode() || d.res.Kind == ir.DeployableSite {
			continue
		}
		bindings := map[string]*ir.Binding{}
		for _, b := range d.res.Bindings {
			bindings[b.Field] = b
		}
		var resolved []ir.ResolvedProvider
		for _, ref := range d.res.Services {
			svc := r.services[ref.Name]
			if svc == nil || svc.Kind != ir.SchemaKindAPI {
				continue
			}
			var filled []string
			for _, slot := range sortedKeys(svc.ProviderSlots) {
				p, ok := r.slotProvider(d, svc, slot, bindings)
				if !ok {
					continue
				}
				resolved = append(resolved, ir.ResolvedProvider{Slot: slot, Interface: svc.ProviderSlots[slot], Provider: p.Name, Package: p.Package, Service: svc.Name})
				filled = append(filled, svc.ProviderSlots[slot])
			}
			r.checkCoverage(d, svc, filled)
		}
		slices.SortFunc(resolved, func(a, b ir.ResolvedProvider) int { return strings.Compare(a.Slot, b.Slot) })
		d.res.Providers = resolved
	}
}

// slotProvider returns the provider slot of svc names in deployable d's
// bindings, or false when the slot is unset or names a provider the
// environment cannot use, which it reports.
func (r *resolver) slotProvider(d *deployable, svc *Service, slot string, bindings map[string]*ir.Binding) (*ir.RuntimeProvider, bool) {
	iface := svc.ProviderSlots[slot]
	contract, _ := ir.RuntimeContractOf(iface)
	b := bindings[slot]
	if b == nil {
		return nil, false
	}
	where := fmt.Sprintf("environment %s: %s %s: provider slot %s of %s", r.envName(), d.res.Kind, d.res.Name, slot, svc.Name)
	if b.Source != ir.BindingLiteral {
		r.fail(CodeProvider, "%s is bound to %s %s; a slot takes a literal, the name of the provider the environment uses, so the build can check it (D159)", where, b.Source, b.Parameter)
		return nil, false
	}
	name, _ := b.Value.(string)
	if name == "" {
		// An empty value leaves the slot unset, as an optional field with
		// no binding does.
		return nil, false
	}
	p := providerNamed(svc.Providers, name)
	if p == nil {
		r.fail(CodeProvider, "%s names provider %q, which is none of the providers %s lists (%s) (D159)", where, name, svc.Name, providerNames(svc.Providers, ""))
		return nil, false
	}
	if p.Implements != iface {
		r.fail(CodeProvider, "%s takes a %s, and provider %s (%s) implements %s; the slot names one of %s (D159)", where, contract.Title, p.Name, p.Package, p.Implements, providerNames(svc.Providers, iface))
		return nil, false
	}
	fields := map[string]ConfigField{}
	if svc.Config != nil {
		for _, f := range svc.Config.Fields {
			fields[f.Name] = f
		}
	}
	ok := true
	for _, key := range p.Config {
		fieldName := ir.ProviderConfigField(slot, key.Key)
		f, declared := fields[fieldName]
		switch {
		case !declared && key.Optional:
		case !declared:
			r.fail(CodeProvider, "%s names provider %s, which reads setting %s from config field %s, and %s declares no such field; declare %s?: %s (D159)", where, p.Name, key.Key, fieldName, svc.Name, fieldName, settingType(key))
			ok = false
		case f.Secret != key.Secret:
			r.fail(CodeProvider, "%s names provider %s, whose setting %s is %s, and config field %s of %s is %s (D159)", where, p.Name, key.Key, secrecy(key.Secret), fieldName, svc.Name, secrecy(f.Secret))
			ok = false
		case !key.Optional && bindings[fieldName] == nil:
			r.fail(CodeProvider, "%s names provider %s, which needs setting %s, and the environment binds no value to %s; set it in the deployable's env, or with `stack secrets set` for a secret (D159)", where, p.Name, key.Key, fieldName)
			ok = false
		}
	}
	return p, ok
}

// unreadProviderSetting reports whether field is a setting of a provider
// slot of an API deployable d serves, the field named after the slot and
// a key that one of the API's providers of the slot's interface reads
// (ir.ProviderConfigField), that no provider d's environment names reads:
// the slot is unset, or names a provider with no such key. Such a field
// is no secret of the environment, so `stack secrets set` does not ask for
// it and a deploy does not wait on it (D159, #608). A slot bound to a
// parameter or to no string names no provider the build can tell, and
// keeps every setting; resolveProviders refuses it.
func (r *resolver) unreadProviderSetting(d *deployable, field string) bool {
	setting := false
	for _, served := range d.res.Services {
		svc := r.services[served.Name]
		if svc == nil || svc.Kind != ir.SchemaKindAPI {
			continue
		}
		for _, slot := range sortedKeys(svc.ProviderSlots) {
			named, known := r.slotValue(d, slot)
			for _, p := range svc.Providers {
				if p == nil || p.Implements != svc.ProviderSlots[slot] {
					continue
				}
				for _, key := range p.Config {
					if ir.ProviderConfigField(slot, key.Key) != field {
						continue
					}
					if !known || p.Name == named {
						return false
					}
					setting = true
				}
			}
		}
	}
	return setting
}

// slotValue returns the provider deployable d's environment names in
// slot, its literal or the field's default, "" when it names none, and
// false when the environment binds it to something else.
func (r *resolver) slotValue(d *deployable, slot string) (string, bool) {
	value, set := d.settings.env[slot]
	switch {
	case set && value.Parameter != "":
		return "", false
	case set:
		if value.Value == nil {
			return "", true
		}
		name, ok := value.Value.(string)
		return name, ok
	}
	if f := d.fields[slot]; f != nil && f.def != nil {
		return *f.def, true
	}
	return "", true
}

// checkCoverage refuses deployable d of API svc when the API uses a
// feature that needs a runtime interface none of the slots d fills
// provides (D159): notifications with no channel adapter, agents with no
// sandbox, attachments with no file processor.
func (r *resolver) checkCoverage(d *deployable, svc *Service, filled []string) {
	for _, use := range svc.Features {
		if slices.Contains(filled, use.Interface) {
			continue
		}
		contract, _ := ir.RuntimeContractOf(use.Interface)
		var slots []string
		for _, slot := range sortedKeys(svc.ProviderSlots) {
			if svc.ProviderSlots[slot] == use.Interface {
				slots = append(slots, slot)
			}
		}
		fix := fmt.Sprintf("declare a slot of %s with @providers on its @envVars class, list a %s in its config's providers, and name it here", use.Interface, contract.Title)
		if len(slots) > 0 {
			fix = fmt.Sprintf("set %s to one of %s in its env", strings.Join(slots, " or "), providerNames(svc.Providers, use.Interface))
		}
		r.fail(CodeNoProvider, "environment %s runs %s %s, whose API %s uses %s, which needs a %s, and the environment configures none: %s (D159)",
			r.envName(), d.res.Kind, d.res.Name, svc.Name, use.Feature, contract.Title, fix)
	}
}

// providerNamed returns the provider of providers named name, or nil.
func providerNamed(providers []*ir.RuntimeProvider, name string) *ir.RuntimeProvider {
	for _, p := range providers {
		if p != nil && p.Name == name {
			return p
		}
	}
	return nil
}

// providerNames lists the names of providers that implement iface, every
// one for an empty iface, for a message.
func providerNames(providers []*ir.RuntimeProvider, iface string) string {
	var names []string
	for _, p := range providers {
		if p != nil && (iface == "" || p.Implements == iface) {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func settingType(key ir.ProviderConfigKey) string {
	if key.Secret {
		return "Secret<string>"
	}
	return "string"
}

func secrecy(secret bool) string {
	if secret {
		return "a secret"
	}
	return "no secret"
}
