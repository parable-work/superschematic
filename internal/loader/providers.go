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

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// The runtime provider packages an API's config lists, and the provider
// slots of its @envVars class (D159).

// providerManifest is the "superschematic": {"provider": {...}} object of
// a provider package's package.json.
type providerManifest struct {
	Name       string                          `json:"name"`
	Implements string                          `json:"implements"`
	Operations []string                        `json:"operations"`
	Config     map[string]providerManifestItem `json:"config"`
	Role       []string                        `json:"role"`
}

// providerManifestItem is one setting of a provider's config.
type providerManifestItem struct {
	Secret   bool   `json:"secret"`
	Optional bool   `json:"optional"`
	Doc      string `json:"description"`
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
		for _, dir := range cfg.Providers {
			p, err := ReadRuntimeProvider(filepath.Join(servicePath, filepath.FromSlash(dir)))
			if err != nil {
				return fmt.Errorf("%s: providers: %s: %w", schema.Name, dir, err)
			}
			p.Path = dir
			if other, dup := names[p.Name]; dup {
				return fmt.Errorf("%s: providers %s and %s are both named %s; an environment names a provider by its name, so each is named once (D159)", schema.Name, other, dir, p.Name)
			}
			names[p.Name] = dir
			schema.Providers = append(schema.Providers, p)
		}
	}
	return checkProviderSlots(schema)
}

// ReadRuntimeProvider reads the runtime provider the package in dir
// declares in its package.json and checks it against its interface's
// contract (ir.RuntimeProvider.Check). Its Path is left empty.
func ReadRuntimeProvider(dir string) (*ir.RuntimeProvider, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no package.json in %s; a provider is a package whose package.json declares it", dir)
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
	p := &ir.RuntimeProvider{
		Name:       m.Name,
		Implements: m.Implements,
		Package:    pkg.Name,
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
	if err := p.Check(); err != nil {
		return nil, err
	}
	p.Normalize()
	return p, nil
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
