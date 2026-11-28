package apigen

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	ir "github.com/parable-work/superschematic/ir"
)

// The runtime providers of a Go server (D159, D184): an API whose
// @envVars class names provider slots gets providers.go, whose
// ProvidersOf reads the providers an environment's config names from its
// EnvConfig, over the definitions of the provider modules the API's config
// lists, and Deps.Providers, which the entrypoint fills with it. go.mod
// requires and replaces each provider's module.

// DepsProviders are the runtime providers Deps holds.
type DepsProviders struct {
	// Slots are the API's provider slots, sorted, each with its interface.
	Slots []DepsProviderSlot

	// Fields are the EnvConfig fields From reads: each slot's and each
	// setting's, sorted.
	Fields []DepsProviderField

	// Modules are the provider modules the server links, by provider name.
	Modules []DepsProviderModule

	// Bind is how a call into a provider binds the ORM to its principal:
	// "principal" for an ORM with per-record access (orm.WithPrincipal),
	// "" for none, which binds the session's caller alone.
	Bind string

	// CallerKeys is set when a provider the config lists calls the app
	// with a key of its own, its CALLER_KEY setting (#744): providers.go
	// has ExtensionKeysOf and CallerAuthenticator, which a server wraps
	// its routes' auth step in, outermost.
	CallerKeys bool
}

// DepsProviderSlot is one provider slot: its config field and the
// interface it takes.
type DepsProviderSlot struct {
	Slot      string
	Interface string
}

// DepsProviderField is one EnvConfig field a provider reads: its config
// name, its Go member, whether it is a Secret<string>, and whether its Go
// type is a pointer, nil when unset.
type DepsProviderField struct {
	Name    string
	GoName  string
	Secret  bool
	Pointer bool
}

// DepsProviderModule is one provider's Go module: its name, its module
// path and the alias providers.go imports it as, the manifest the build
// read, as JSON, which From holds its definition to, and its directory,
// absolute, which go.mod replaces the module with.
type DepsProviderModule struct {
	Name     string
	Module   string
	Alias    string
	Manifest string
	Dir      string

	// ReplacePath is Dir relative to the API module's directory, which the
	// dispatch layer sets.
	ReplacePath string
}

// ProvidersOf is what Deps holds of the API schema's runtime providers,
// given its EnvConfig and whether its database binds a principal (D180):
// nil for an API whose @envVars class names no slot. It fails for a
// listed provider with no Go module, which the loader refuses a Go server
// already.
func ProvidersOf(schema *ir.Schema, env *envgen.ConfigOutput, principal bool) (*DepsProviders, error) {
	slots, err := schema.ProviderSlotsOf()
	if err != nil || len(slots) == 0 {
		return nil, err
	}
	out := &DepsProviders{}
	if principal {
		out.Bind = "principal"
	}
	names := make([]string, 0, len(slots))
	for slot := range slots {
		names = append(names, slot)
	}
	sort.Strings(names)
	for _, slot := range names {
		out.Slots = append(out.Slots, DepsProviderSlot{Slot: slot, Interface: slots[slot]})
	}
	fields := map[string]bool{}
	for _, slot := range names {
		fields[slot] = true
		for _, p := range schema.Providers {
			if p != nil && p.Implements == slots[slot] {
				for _, c := range p.Config {
					fields[ir.ProviderConfigField(slot, c.Key)] = true
				}
			}
		}
	}
	if env != nil {
		for _, f := range env.Fields {
			if !fields[f.Key] {
				continue
			}
			if strings.TrimPrefix(f.GoType, "*") != "string" {
				return nil, fmt.Errorf("apigen: %s's config field %s is a %s; a provider slot or setting is a string (D159)", schema.Name, f.Key, f.GoType)
			}
			out.Fields = append(out.Fields, DepsProviderField{Name: f.Key, GoName: f.GoName, Secret: f.Secret, Pointer: strings.HasPrefix(f.GoType, "*")})
		}
	}
	sort.Slice(out.Fields, func(i, j int) bool { return out.Fields[i].Name < out.Fields[j].Name })
	providers := append([]*ir.RuntimeProvider(nil), schema.Providers...)
	sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
	aliases := map[string]bool{}
	for _, p := range providers {
		if p == nil {
			continue
		}
		if p.Module == "" {
			return nil, fmt.Errorf("apigen: the Go server of %s cannot link runtime provider %s (%s): its directory holds no Go module (D184)", schema.Name, p.Name, p.Path)
		}
		alias := strings.NewReplacer("-", "", ".", "").Replace(path.Base(p.Module)) + "provider"
		for i := 2; aliases[alias]; i++ {
			alias = fmt.Sprintf("%s%d", strings.TrimRight(alias, "0123456789"), i)
		}
		aliases[alias] = true
		manifest := struct {
			Name       string                     `json:"name"`
			Implements string                     `json:"implements"`
			Operations []string                   `json:"operations,omitempty"`
			Config     map[string]map[string]bool `json:"config,omitempty"`
			Role       []string                   `json:"role,omitempty"`
		}{Name: p.Name, Implements: p.Implements, Operations: p.Operations, Role: p.Role}
		for _, c := range p.Config {
			if manifest.Config == nil {
				manifest.Config = map[string]map[string]bool{}
			}
			item := map[string]bool{}
			if c.Secret {
				item["secret"] = true
			}
			if c.Optional {
				item["optional"] = true
			}
			manifest.Config[c.Key] = item
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		out.Modules = append(out.Modules, DepsProviderModule{Name: p.Name, Module: p.Module, Alias: alias, Manifest: string(data), Dir: p.Dir})
		out.CallerKeys = out.CallerKeys || p.CallsTheApp()
	}
	return out, nil
}
