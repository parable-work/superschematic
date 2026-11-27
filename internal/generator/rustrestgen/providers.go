package rustrestgen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The runtime providers of a Rust API (D159, D185): src/providers.rs holds
// the API's @providers slots, the manifests the build read of the provider
// crates its config lists, their definitions, and from_config, which reads
// the providers an environment's EnvConfig names; Cargo.toml depends on
// each crate by path; and Deps holds the Providers, which the server's
// entrypoint builds from the API's config (servergen).

// ProvidersFile is the file of the API crate that holds its providers.
const ProvidersFile = "providers.rs"

// ProvidersInfo is what src/providers.rs writes.
type ProvidersInfo struct {
	// Slots are the API's provider slots, by config field, with the
	// interface each takes.
	Slots []ProviderSlotInfo
	// Settings are the fields of the API's config a slot or a provider's
	// setting is, in order: the field's name, its EnvConfig member and
	// whether it is a Secret<T>.
	Settings []ProviderSettingInfo
	// Crates are the provider crates the API's config lists, by name.
	Crates []ProviderCrateInfo
}

// ProviderSlotInfo is one slot.
type ProviderSlotInfo struct {
	Slot      string
	Interface string
}

// ProviderSettingInfo is one config field a provider reads.
type ProviderSettingInfo struct {
	Field     string
	RustField string
	Secret    bool
}

// ProviderCrateInfo is one provider crate: the provider's name, the
// crate's package name and its directory relative to the API crate's, and
// its manifest as the build read it, a Rust raw string literal of JSON.
type ProviderCrateInfo struct {
	Name     string
	Crate    string
	Path     string
	Manifest string
}

// Ident is the crate as Rust source names it.
func (c ProviderCrateInfo) Ident() string {
	return strings.ReplaceAll(c.Crate, "-", "_")
}

// providerManifestJSON is a provider's manifest as the runtime's
// check_provider_manifest reads it.
type providerManifestJSON struct {
	Name       string                         `json:"name"`
	Implements string                         `json:"implements"`
	Operations []string                       `json:"operations"`
	Config     map[string]providerSettingJSON `json:"config,omitempty"`
	Role       []string                       `json:"role,omitempty"`
}

type providerSettingJSON struct {
	Secret   bool `json:"secret,omitempty"`
	Optional bool `json:"optional,omitempty"`
}

// providersOf is the providers of the API's schema, nil for one with no
// slot and no listed provider. Each listed provider is a crate the
// loader found (RuntimeProvider.Crate and Dir), named relative to
// outputDir, the API crate's directory.
func providersOf(schema *ir.Schema, outputDir string) (*ProvidersInfo, error) {
	slots, err := schema.ProviderSlotsOf()
	if err != nil {
		return nil, err
	}
	if len(slots) == 0 && len(schema.Providers) == 0 {
		return nil, nil
	}
	info := &ProvidersInfo{}
	names := make([]string, 0, len(slots))
	for slot := range slots {
		names = append(names, slot)
	}
	sort.Strings(names)
	for _, slot := range names {
		info.Slots = append(info.Slots, ProviderSlotInfo{Slot: slot, Interface: slots[slot]})
	}
	for _, p := range schema.Providers {
		if p == nil {
			continue
		}
		if p.Crate == "" {
			return nil, fmt.Errorf("provider %s (%s) has no Rust crate; a Rust server links a crate whose Cargo.toml declares it under [package.metadata.superschematic.provider] (D185)", p.Name, p.Path)
		}
		if p.Dir == "" {
			return nil, fmt.Errorf("provider %s: the build does not know the directory of crate %s; load the API through the loader, which finds it (D185)", p.Name, p.Crate)
		}
		rel, err := filepath.Rel(outputDir, p.Dir)
		if err != nil {
			return nil, fmt.Errorf("provider %s: crate %s at %s: %w", p.Name, p.Crate, p.Dir, err)
		}
		manifest := providerManifestJSON{Name: p.Name, Implements: p.Implements, Operations: append([]string{}, p.Operations...), Role: p.Role}
		for _, c := range p.Config {
			if manifest.Config == nil {
				manifest.Config = map[string]providerSettingJSON{}
			}
			manifest.Config[c.Key] = providerSettingJSON{Secret: c.Secret, Optional: c.Optional}
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		info.Crates = append(info.Crates, ProviderCrateInfo{Name: p.Name, Crate: p.Crate, Path: filepath.ToSlash(rel), Manifest: rustutil.RawString(string(data))})
	}
	sort.Slice(info.Crates, func(i, j int) bool { return info.Crates[i].Name < info.Crates[j].Name })

	// The fields the providers read: each slot, and each setting of a
	// listed provider of the slot's interface that the config declares.
	config, err := schema.EnvVarsType()
	if err != nil {
		return nil, err
	}
	fields := map[string]*ir.FieldDef{}
	if config != nil {
		for _, fd := range config.Fields {
			if fd != nil {
				fields[fd.Name] = fd
			}
		}
	}
	seen := map[string]bool{}
	add := func(field string) {
		fd := fields[field]
		if fd == nil || seen[field] {
			return
		}
		seen[field] = true
		info.Settings = append(info.Settings, ProviderSettingInfo{Field: field, RustField: envgen.RustFieldName(field), Secret: fd.Secret})
	}
	for _, slot := range info.Slots {
		add(slot.Slot)
		for _, p := range schema.Providers {
			if p == nil || p.Implements != slot.Interface {
				continue
			}
			for _, c := range p.Config {
				add(ir.ProviderConfigField(slot.Slot, c.Key))
			}
		}
	}
	return info, nil
}
