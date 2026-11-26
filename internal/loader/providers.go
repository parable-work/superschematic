package loader

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// The runtime provider packages an API's config lists, and the provider
// slots of its @envVars class (D159).

// providerManifest is the "superschematic": {"provider": {...}} object of
// a provider package's package.json, or the
// [package.metadata.superschematic.provider] table of a provider crate's
// Cargo.toml (D185).
type providerManifest struct {
	Name       string                          `json:"name" toml:"name"`
	Implements string                          `json:"implements" toml:"implements"`
	Operations []string                        `json:"operations" toml:"operations"`
	Config     map[string]providerManifestItem `json:"config" toml:"config"`
	Role       []string                        `json:"role" toml:"role"`
}

// providerManifestItem is one setting of a provider's config.
type providerManifestItem struct {
	Secret   bool   `json:"secret" toml:"secret"`
	Optional bool   `json:"optional" toml:"optional"`
	Doc      string `json:"description" toml:"description"`
}

// loadProviders reads each runtime provider package cfg lists, relative to
// servicePath, checks it against its interface's contract and puts it on
// schema.Providers, then checks the provider slots of the schema's
// @envVars classes against the config's fields and the providers. A
// schema whose config lists none and whose classes declare no slot is
// left as it is.
func loadProviders(servicePath string, schema *ir.Schema, cfg *schemaconfig.SchemaConfig) error {
	if schema == nil {
		return nil
	}
	if cfg != nil && len(cfg.Providers) > 0 {
		names := map[string]string{}
		// A schema loaded before, which a load may hand back, holds the
		// providers it read then.
		schema.Providers = nil
		for _, dir := range cfg.Providers {
			p, err := ReadRuntimeProvider(filepath.Join(servicePath, filepath.FromSlash(dir)))
			if err != nil {
				return fmt.Errorf("%s: providers: %s: %w", schema.Name, dir, err)
			}
			p.Path = dir
			if p.Dir, err = filepath.Abs(filepath.Join(servicePath, filepath.FromSlash(dir))); err != nil {
				return fmt.Errorf("%s: providers: %s: %w", schema.Name, dir, err)
			}
			if other, dup := names[p.Name]; dup {
				return fmt.Errorf("%s: providers %s and %s are both named %s; an environment names a provider by its name, so each is named once (D159)", schema.Name, other, dir, p.Name)
			}
			names[p.Name] = dir
			schema.Providers = append(schema.Providers, p)
		}
	}
	return checkProviderSlots(schema)
}

// ReadRuntimeProvider reads the runtime provider the directory dir holds
// and checks it against its interface's contract
// (ir.RuntimeProvider.Check). Its Path is left empty. The directory holds
// a TypeScript package whose package.json declares it under
// "superschematic": {"provider": ...} (D159), a Rust crate whose
// Cargo.toml declares it under [package.metadata.superschematic.provider]
// (D185), or both, which then declare one provider.
func ReadRuntimeProvider(dir string) (*ir.RuntimeProvider, error) {
	fromPackage, err := readPackageProvider(dir)
	if err != nil {
		return nil, err
	}
	fromCrate, err := readCrateProvider(dir)
	if err != nil {
		return nil, err
	}
	switch {
	case fromPackage == nil && fromCrate == nil:
		return nil, fmt.Errorf("no package.json or Cargo.toml in %s; a provider is a package whose package.json declares it, or a crate whose Cargo.toml does (D159, D185)", dir)
	case fromPackage == nil:
		return fromCrate, nil
	case fromCrate == nil:
		return fromPackage, nil
	}
	same := *fromCrate
	same.Package, same.Crate = fromPackage.Package, ""
	a, _ := json.Marshal(fromPackage)
	b, _ := json.Marshal(&same)
	if !bytes.Equal(a, b) {
		return nil, fmt.Errorf("%s: package %s and crate %s declare different providers (%s and %s); a directory that holds both declares one provider in each (D185)", dir, fromPackage.Package, fromCrate.Crate, a, b)
	}
	fromPackage.Crate = fromCrate.Crate
	return fromPackage, nil
}

// readPackageProvider reads the provider dir's package.json declares, or
// nil for a directory with none.
func readPackageProvider(dir string) (*ir.RuntimeProvider, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var pkg struct {
		Name           string `json:"name"`
		Superschematic struct {
			Provider json.RawMessage `json:"provider"`
		} `json:"superschematic"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("package.json: %w", err)
	}
	if len(pkg.Superschematic.Provider) == 0 {
		return nil, fmt.Errorf("package %s declares no provider; its package.json names the interface it implements under \"superschematic\": {\"provider\": {\"name\", \"implements\", ...}} (D159)", pkg.Name)
	}
	dec := json.NewDecoder(bytes.NewReader(pkg.Superschematic.Provider))
	dec.DisallowUnknownFields()
	var m providerManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("package %s: superschematic.provider: %w; it takes name, implements, operations, config and role", pkg.Name, err)
	}
	p := m.provider()
	p.Package = pkg.Name
	if err := p.Check(); err != nil {
		return nil, err
	}
	p.Normalize()
	return p, nil
}

// readCrateProvider reads the provider dir's Cargo.toml declares under
// [package.metadata.superschematic.provider] (D185), or nil for a
// directory with no Cargo.toml.
func readCrateProvider(dir string) (*ir.RuntimeProvider, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var manifest struct {
		Package struct {
			Name     string `toml:"name"`
			Metadata struct {
				Superschematic struct {
					Provider *toml.Primitive `toml:"provider"`
				} `toml:"superschematic"`
			} `toml:"metadata"`
		} `toml:"package"`
	}
	meta, err := toml.Decode(string(data), &manifest)
	if err != nil {
		return nil, fmt.Errorf("Cargo.toml: %w", err)
	}
	name := manifest.Package.Name
	if manifest.Package.Metadata.Superschematic.Provider == nil {
		return nil, fmt.Errorf("crate %s declares no provider; its Cargo.toml names the interface it implements under [package.metadata.superschematic.provider] (name, implements, ...) (D185)", name)
	}
	var m providerManifest
	if err := meta.PrimitiveDecode(*manifest.Package.Metadata.Superschematic.Provider, &m); err != nil {
		return nil, fmt.Errorf("crate %s: package.metadata.superschematic.provider: %w", name, err)
	}
	for _, key := range meta.Undecoded() {
		if len(key) > 4 && key[0] == "package" && key[1] == "metadata" && key[2] == "superschematic" && key[3] == "provider" {
			return nil, fmt.Errorf("crate %s: package.metadata.superschematic.provider: unknown key %s; it takes name, implements, operations, config and role", name, key)
		}
	}
	p := m.provider()
	p.Crate = name
	if err := p.Check(); err != nil {
		return nil, err
	}
	p.Normalize()
	return p, nil
}

// provider is the runtime provider the manifest declares, its settings
// sorted by key.
func (m providerManifest) provider() *ir.RuntimeProvider {
	p := &ir.RuntimeProvider{
		Name:       m.Name,
		Implements: m.Implements,
		Operations: append([]string(nil), m.Operations...),
		Role:       append([]string(nil), m.Role...),
	}
	keys := make([]string, 0, len(m.Config))
	for key := range m.Config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		item := m.Config[key]
		p.Config = append(p.Config, ir.ProviderConfigKey{Key: key, Secret: item.Secret, Optional: item.Optional})
	}
	return p
}

// checkProviderSlots checks each provider slot of the schema's config: a
// field of the config, a string that is no secret, and an interface one
// of the providers the config lists implements, since otherwise no
// environment can fill it.
func checkProviderSlots(schema *ir.Schema) error {
	slots, err := schema.ProviderSlotsOf()
	if err != nil || len(slots) == 0 {
		return err
	}
	config, err := schema.EnvVarsType()
	if err != nil {
		return err
	}
	fields := map[string]*ir.FieldDef{}
	if config != nil {
		for _, fd := range config.Fields {
			fields[fd.Name] = fd
		}
	}
	var problems []string
	for _, slot := range sortedSlotNames(slots) {
		iface := slots[slot]
		fd := fields[slot]
		switch {
		case fd == nil:
			problems = append(problems, fmt.Sprintf("slot %s is no field of the config %s; declare %s?: string, whose value names the provider in each environment", slot, configName(config), slot))
			continue
		case fd.Secret:
			problems = append(problems, fmt.Sprintf("slot %s is a Secret<T>; it names a provider, which is no secret, and the provider's secrets are fields of their own", slot))
			continue
		case fd.TypeRef.Name != "string" || fd.TypeRef.IsArray || fd.TypeRef.IsMap:
			problems = append(problems, fmt.Sprintf("slot %s is a %s; it is a string, the provider's name", slot, fd.TypeRef.Name))
			continue
		}
		var implementers []string
		for _, p := range schema.Providers {
			if p.Implements == iface {
				implementers = append(implementers, p.Name)
			}
		}
		if len(implementers) == 0 {
			contract, _ := ir.RuntimeContractOf(iface)
			problems = append(problems, fmt.Sprintf("slot %s takes a %s, and none of the providers the config lists implements %s; list one in the config's providers", slot, contract.Title, iface))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s: provider slots (D159): %s", schema.Name, strings.Join(problems, "; "))
	}
	return nil
}

func sortedSlotNames(slots map[string]string) []string {
	names := make([]string, 0, len(slots))
	for name := range slots {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func configName(td *ir.TypeDef) string {
	if td == nil {
		return "(none: the schema declares no @envVars class)"
	}
	return td.Name
}
