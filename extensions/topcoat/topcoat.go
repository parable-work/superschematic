// Package topcoat is the Topcoat extension: for an API service whose server
// is Rust, outputs.topcoat writes a crate a Topcoat (https://github.com/tokio-rs/topcoat)
// app depends on, beside the API crate.
//
// The crate mounts the service's JSON API in the app's router, calls each
// operation in-process from a page, a shard or a procedure by its route's
// rules (the caller admitted as the route admits it, the arguments checked
// as the router checks them, D43), offers a guard per operation, and
// mirrors each type an operation returns as a Topcoat record, which a
// page can hand the browser.
//
// The package uses only the public registry and ir packages, as an
// out-of-tree extension would.
package topcoat

import (
	"encoding/json"
	"path/filepath"

	"github.com/parable-work/superschematic/registry"
)

// OutputKey is the schema.config outputs key the extension reads.
const OutputKey = "topcoat"

// TopcoatVersion is the Topcoat release the generated crate depends on.
const TopcoatVersion = "0.10"

// Extension registers the topcoat generator.
type Extension struct{}

// Name implements registry.Extension.
func (Extension) Name() string { return "topcoat" }

// Register implements registry.Extension.
func (Extension) Register(r *registry.Registry) error {
	return r.RegisterGenerator(registry.GeneratorSpec{
		Name:         "topcoat",
		Extension:    "topcoat",
		Kinds:        []string{"API"},
		OutputKey:    OutputKey,
		OutputSchema: OutputSchema,
		Dirs: func(c registry.GenerateContext) []string {
			return []string{Dir(c.Options.OutputRoot, c.Config.Name)}
		},
		Enabled:  enabled,
		Generate: generate,
	})
}

// OutputSchema is the JSON Schema of outputs.topcoat.
var OutputSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "enabled": {"type": "boolean"},
    "records": {"type": "boolean", "description": "Mirror each type an operation returns as a Topcoat record (default true)."}
  }
}`)

// Config is the decoded outputs.topcoat.
type Config struct {
	Enabled bool `json:"enabled"`
	// Records is nil when the section leaves it out, which is on.
	Records *bool `json:"records,omitempty"`
}

// WritesRecords reports whether the crate mirrors the operations' result
// types as records.
func (c Config) WritesRecords() bool { return c.Records == nil || *c.Records }

// Dir is where the crate is written for a service.
func Dir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "topcoat", service)
}

// configOf decodes the service's outputs.topcoat; the zero Config without
// one.
func configOf(c registry.GenerateContext) (Config, error) {
	var cfg Config
	err := registry.DecodeOutput(c.Outputs, OutputKey, &cfg)
	return cfg, err
}

// enabled runs the generator for a service whose outputs.topcoat is
// enabled and whose API server is Rust, from its config or from build
// --api-language RUST. A service that builds its server in another
// language skips the output, so one config serves a Go build and a Rust
// one.
func enabled(c registry.GenerateContext) (bool, string) {
	cfg, err := configOf(c)
	if err != nil || !cfg.Enabled {
		return false, "topcoat: outputs.topcoat.enabled is false"
	}
	if api := c.Outputs.API; api == nil || !api.Enabled || api.Language != registry.APILanguageRust {
		return false, "topcoat: the API server is not Rust (outputs.api.language RUST, or build --api-language RUST)"
	}
	return true, ""
}

func generate(c registry.GenerateContext) error {
	cfg, err := configOf(c)
	if err != nil {
		return err
	}
	api, err := registry.RustAPIOf(c)
	if err != nil {
		return err
	}
	if api == nil {
		c.Skip(OutputKey)
		return nil
	}
	data, err := newCrate(c, api, cfg)
	if err != nil {
		return err
	}
	dir := Dir(c.Options.OutputRoot, c.Config.Name)
	if err := data.write(dir); err != nil {
		return err
	}
	c.Done(OutputKey, dir)
	return nil
}
