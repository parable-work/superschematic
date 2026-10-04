package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
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

// languageNames spells each target language for messages.
var languageNames = map[string]string{
	LangGo:         "Go",
	LangTypeScript: "TypeScript",
	LangPython:     "Python",
	LangRust:       "Rust",
}

// LanguageName returns the display name of a target language ("Rust" for
// "rust"), or lang itself when it is not one.
func LanguageName(lang string) string {
	if name, ok := languageNames[lang]; ok {
		return name
	}
	return lang
}

// sdkTypesUse says what the SDK of each language does with the types
// package of the same language, for the error that refuses an SDK whose
// types are not enabled.
var sdkTypesUse = map[string]string{
	LangGo:         "the Go SDK's methods take and return the Go types",
	LangTypeScript: "the TypeScript SDK decodes responses and validates inputs with the TypeScript types",
	LangPython:     "the Python SDK validates with the Python types and skips validation without them",
	LangRust:       "the Rust SDK depends on the Rust types crate and its methods take and return its types",
}

// apiTypes gives, for each API server language, the target language of the
// types package the server imports and what it does with it, for the error
// that refuses an API whose types are not enabled.
var apiTypes = map[string]struct{ lang, use string }{
	APILanguageGo:         {LangGo, "the Go API server decodes requests into the Go types and its handler interfaces take and return them"},
	APILanguageRust:       {LangRust, "the Rust API server's crate depends on the Rust types crate"},
	APILanguageTypeScript: {LangTypeScript, "the TypeScript API server validates requests with the TypeScript types and its handler interfaces take and return them"},
}

// TargetOutputConfig is a per-language enable switch.
type TargetOutputConfig struct {
	Enabled bool `json:"enabled"`
}

// APIOutputConfig configures the REST API server output.
type APIOutputConfig struct {
	Enabled bool `json:"enabled"`

	// Language is the API server implementation language (GO, RUST, or
	// TYPESCRIPT). Defaults to GO when omitted.
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

	// Dialects are the databases the service is built for (D27): postgres,
	// and sqlite for sqlite/create.sql and SQLite migration plans. Unset is
	// ["postgres"]. ParseOutputs refuses a list without postgres, since the
	// DB kind always generates the Go ORM, which runs on Postgres.
	Dialects []string `json:"dialects,omitempty"`
}

// SQL dialects outputs.sql.dialects may list (the SqlDialect type in
// @superschematic/schema-config), in the order messages name them.
const (
	SQLDialectPostgres = "postgres"
	SQLDialectSQLite   = "sqlite"
)

// SQLDialectNames lists every SQL dialect.
var SQLDialectNames = []string{SQLDialectPostgres, SQLDialectSQLite}

// checkSQLDialects refuses an outputs.sql.dialects list with an unknown or
// repeated dialect, or without postgres.
func checkSQLDialects(dialects []string) error {
	seen := map[string]bool{}
	for _, dialect := range dialects {
		if !containsString(SQLDialectNames, dialect) {
			return fmt.Errorf("outputs.sql.dialects: unknown dialect %q (want %s)", dialect, strings.Join(SQLDialectNames, " or "))
		}
		if seen[dialect] {
			return fmt.Errorf("outputs.sql.dialects lists %s twice", dialect)
		}
		seen[dialect] = true
	}
	if !seen[SQLDialectPostgres] {
		return errors.New("outputs.sql.dialects must list postgres: the DB kind always generates the Go ORM, which runs on Postgres")
	}
	return nil
}

// API server language and protocol values.
const (
	APILanguageGo         = "GO"
	APILanguageRust       = "RUST"
	APILanguageTypeScript = "TYPESCRIPT"
	APIProtocolREST       = "REST_JSON"
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
// typed form, rejecting keys no registered generator claims, sections that
// fail their generator's OutputSchema, unknown target languages, and an API
// server or SDK whose language has no types output (each imports that
// package). The Go ORM's need for the Go types depends on the kind, so
// generator.Run and generator.ExpectedOutputDirs check it.
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
	if err := json.Unmarshal(data, &outputs.Raw); err != nil {
		return nil, fmt.Errorf("decoding outputs block: %w", err)
	}
	if err := reg.validateOutputSections(outputs.Raw); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, outputs); err != nil {
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
	// The API server and each SDK import the types package of their
	// language, which only outputs.types generates. The API language is
	// read after its default; an unsupported one is left to the API
	// generator, which names the supported ones.
	outputs.applyAPIDefaults()
	var missingTypes []error
	if outputs.APIEnabled() {
		if need, ok := apiTypes[outputs.API.Language]; ok && !outputs.TypesEnabled(need.lang) {
			missingTypes = append(missingTypes, fmt.Errorf("outputs.api with language %s needs outputs.types.%s: %s", outputs.API.Language, need.lang, need.use))
		}
	}
	for _, lang := range outputs.EnabledSDKLanguages() {
		if !outputs.TypesEnabled(lang) {
			missingTypes = append(missingTypes, fmt.Errorf("outputs.sdk.%s needs outputs.types.%s: %s", lang, lang, sdkTypesUse[lang]))
		}
	}
	if err := errors.Join(missingTypes...); err != nil {
		return nil, err
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
		// An empty list is given and lacks postgres; only an absent one
		// takes the default.
		if sql.Dialects != nil {
			if err := checkSQLDialects(sql.Dialects); err != nil {
				return nil, err
			}
		}
	}

	return outputs, nil
}

// validateOutputSections checks each section of the outputs block against
// the OutputSchema of the generator that claims its key, in registration
// order so the first error is stable.
func (r *Registry) validateOutputSections(sections map[string]json.RawMessage) error {
	for _, name := range r.generatorOrder {
		spec := r.generators[name]
		section, ok := sections[spec.OutputKey]
		if !ok || spec.compiledOutput == nil {
			continue
		}
		instance, err := validator.UnmarshalJSON(bytes.NewReader(section))
		if err != nil {
			return fmt.Errorf("outputs.%s: %w", spec.OutputKey, err)
		}
		if err := spec.compiledOutput.Validate(instance); err != nil {
			return fmt.Errorf("outputs.%s: %w", spec.OutputKey, err)
		}
	}
	return nil
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

// SQLDialects returns outputs.sql.dialects, or ["postgres"] when unset.
func (o *Outputs) SQLDialects() []string {
	if o == nil || o.SQL == nil || o.SQL.Dialects == nil {
		return []string{SQLDialectPostgres}
	}
	return append([]string(nil), o.SQL.Dialects...)
}

// SQLDialect reports whether outputs.sql.dialects lists dialect.
func (o *Outputs) SQLDialect(dialect string) bool {
	return containsString(o.SQLDialects(), dialect)
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
