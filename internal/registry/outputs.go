package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Target languages accepted in the outputs block (the TargetLanguage enum in
// @superschematic/schema-config).
const (
	LangGo         = "go"
	LangTypeScript = "typescript"
	LangPython     = "python"
	LangRust       = "rust"
)

var knownTargetLanguages = map[string]bool{
	LangGo:         true,
	LangTypeScript: true,
	LangPython:     true,
	LangRust:       true,
}

// TargetOutputConfig is a per-language enable switch.
type TargetOutputConfig struct {
	Enabled bool `json:"enabled"`
}

// APIOutputConfig configures the REST API server output.
type APIOutputConfig struct {
	Enabled bool `json:"enabled"`

	// Language is the API server implementation language (GO or RUST).
	// Defaults to GO when omitted.
	Language string `json:"language,omitempty"`

	// Protocol is the API wire protocol. Defaults to REST_JSON when omitted.
	Protocol string `json:"protocol,omitempty"`

	// ScaffoldsOutputDir is where route-impl scaffolds are written, relative
	// to the service directory. Empty disables scaffold generation.
	ScaffoldsOutputDir string `json:"scaffoldsOutputDir,omitempty"`
}

// SQLOutputConfig configures the DB kind's sql output. The DDL and the ORM
// are implied by the kind; this block places and owns the generated
// projection view migrations.
type SQLOutputConfig struct {
	// MigrationsDir is where the projection view migrations are written,
	// relative to the service directory. Empty keeps them under
	// <out>/sql/<service>/projections/migrations.
	MigrationsDir string `json:"migrationsDir,omitempty"`

	// ViewOwner is the Postgres role the migrations create the views as
	// (SET ROLE around the view DDL). Empty creates them as the runner.
	ViewOwner string `json:"viewOwner,omitempty"`
}

// API server language and protocol values.
const (
	APILanguageGo   = "GO"
	APILanguageRust = "RUST"
	APIProtocolREST = "REST_JSON"
)

// Outputs is the typed interpretation of SchemaConfig.Outputs (the
// SchemaOutputs shape declared by @superschematic/schema-config). The loader carries
// the block as raw data; the generators own its meaning.
type Outputs struct {
	// Types holds per-language type-library switches.
	Types map[string]TargetOutputConfig `json:"types,omitempty"`

	// API configures the REST API server output.
	API *APIOutputConfig `json:"api,omitempty"`

	// SDK holds per-language client SDK switches.
	SDK map[string]TargetOutputConfig `json:"sdk,omitempty"`

	// SQL configures the DB kind's sql output.
	SQL *SQLOutputConfig `json:"sql,omitempty"`

	// Raw holds every key of the block verbatim so extension generators can
	// decode their own section with DecodeOutput.
	Raw map[string]json.RawMessage `json:"-"`
}

// ParseOutputs decodes the raw outputs block from schema.config into its
// typed form, rejecting keys no registered generator claims and unknown
// target languages.
func ParseOutputs(raw map[string]any, reg *Registry) (*Outputs, error) {
	if raw == nil {
		return &Outputs{}, nil
	}

	known := reg.OutputKeys()
	for key := range raw {
		if !containsString(known, key) {
			return nil, fmt.Errorf("outputs block has unknown key %q (expected %s)", key, strings.Join(known, ", "))
		}
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encoding outputs block: %w", err)
	}

	outputs := &Outputs{}
	if err := json.Unmarshal(data, outputs); err != nil {
		return nil, fmt.Errorf("decoding outputs block: %w", err)
	}
	if err := json.Unmarshal(data, &outputs.Raw); err != nil {
		return nil, fmt.Errorf("decoding outputs block: %w", err)
	}

	for lang := range outputs.Types {
		if !knownTargetLanguages[lang] {
			return nil, fmt.Errorf("outputs.types has unknown target language %q", lang)
		}
	}
	for lang := range outputs.SDK {
		if !knownTargetLanguages[lang] {
			return nil, fmt.Errorf("outputs.sdk has unknown target language %q", lang)
		}
	}

	// outputs.sql is read strictly: a misspelt key would otherwise leave
	// the migrations in the output root or the views owned by the runner
	// without a word.
	if section, ok := outputs.Raw["sql"]; ok {
		dec := json.NewDecoder(bytes.NewReader(section))
		dec.DisallowUnknownFields()
		var sql SQLOutputConfig
		if err := dec.Decode(&sql); err != nil {
			return nil, fmt.Errorf("outputs.sql: %w", err)
		}
	}

	outputs.applyAPIDefaults()

	return outputs, nil
}

// DecodeOutput decodes outputs.<key> into v. A missing key leaves v
// untouched and returns nil.
func DecodeOutput(o *Outputs, key string, v any) error {
	if o == nil {
		return nil
	}
	section, ok := o.Raw[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(section, v); err != nil {
		return fmt.Errorf("decoding outputs.%s: %w", key, err)
	}
	return nil
}

func (o *Outputs) applyAPIDefaults() {
	if o == nil || o.API == nil || !o.API.Enabled {
		return
	}
	if o.API.Protocol == "" {
		o.API.Protocol = APIProtocolREST
	}
	if o.API.Language == "" {
		o.API.Language = APILanguageGo
	}
}

// TypesEnabled reports whether the type library for lang is enabled.
func (o *Outputs) TypesEnabled(lang string) bool {
	return o != nil && o.Types[lang].Enabled
}

// SDKEnabled reports whether the client SDK for lang is enabled.
func (o *Outputs) SDKEnabled(lang string) bool {
	return o != nil && o.SDK[lang].Enabled
}

// SQLMigrationsDir returns outputs.sql.migrationsDir, relative to the
// service directory ("" when unset).
func (o *Outputs) SQLMigrationsDir() string {
	if o == nil || o.SQL == nil {
		return ""
	}
	return o.SQL.MigrationsDir
}

// SQLViewOwner returns outputs.sql.viewOwner ("" when unset).
func (o *Outputs) SQLViewOwner() string {
	if o == nil || o.SQL == nil {
		return ""
	}
	return o.SQL.ViewOwner
}

// APIEnabled reports whether the REST API server output is enabled.
func (o *Outputs) APIEnabled() bool {
	return o != nil && o.API != nil && o.API.Enabled
}

// EnabledTypeLanguages returns the sorted list of languages with type output enabled.
func (o *Outputs) EnabledTypeLanguages() []string {
	return enabledLanguages(o.Types)
}

// EnabledSDKLanguages returns the sorted list of languages with SDK output enabled.
func (o *Outputs) EnabledSDKLanguages() []string {
	return enabledLanguages(o.SDK)
}

func enabledLanguages(m map[string]TargetOutputConfig) []string {
	var langs []string
	for lang, cfg := range m {
		if cfg.Enabled {
			langs = append(langs, lang)
		}
	}
	sort.Strings(langs)
	return langs
}
