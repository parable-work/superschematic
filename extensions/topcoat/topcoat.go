// Package topcoat is the Topcoat extension: for an API service whose server
// is Rust, outputs.topcoat writes a crate a Topcoat (https://github.com/tokio-rs/topcoat)
// app depends on, beside the API crate.
//
// The crate mounts the service's JSON API in the app's router, calls each
// operation in-process from a page, a shard or a procedure by its route's
// rules (the caller admitted as the route admits it, the arguments checked
// as the router checks them, D43), save a webhook and one the service
// mounts itself, offers a guard per operation, and mirrors each type such
// a call returns as a Topcoat record, which a page can hand the browser. A
// form per input type such a call takes, nested objects and lists
// included, parses what the browser sends by the input type's rules (D14)
// and renders its fields at their paths with the attributes those rules
// give them, a list's rows with buttons that add and remove them, and a
// form per such call whose other arguments a form holds does the same by
// the router's rules, a GET's a filter read from the query. A procedure
// per operation lets browser code call it, its arguments and its result
// records, its refusal a record the browser reads, under its route's
// traffic controls. An operation whose route a browser's request cannot
// meet, a webhook's or one only a service may call, has none. A detail and
// a table component per record render it as HTML, labeled by the schema's
// titles and its types' @display.
//
// The package uses only the public registry and ir packages, as an
// out-of-tree extension would.
package topcoat

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

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
	table, err := decodeTable(r.ExtensionConfig("topcoat"))
	if err != nil {
		return err
	}
	return r.RegisterGenerator(registry.GeneratorSpec{
		Name:         "topcoat",
		Extension:    "topcoat",
		Kinds:        []string{"API"},
		OutputKey:    OutputKey,
		OutputSchema: OutputSchema,
		Dirs: func(c registry.GenerateContext) []string {
			return []string{Dir(c.Options.OutputRoot, c.Config.Name)}
		},
		Enabled: func(c registry.GenerateContext) (bool, string) {
			return enabled(c, table)
		},
		Generate: func(c registry.GenerateContext) error {
			return generate(c, table)
		},
	})
}

// Table is the decoded [extension.topcoat] table of superschematic.toml.
// Services lists services whose crate is written as if their config
// enabled outputs.topcoat, which a binary without the extension never
// reads: a project builds the same configs with the core binary and with
// this one.
type Table struct {
	Services []string
}

// decodeTable decodes the [extension.topcoat] table, refusing a key it
// does not declare.
func decodeTable(table map[string]any) (Table, error) {
	var out Table
	for key, value := range table {
		switch key {
		case "services":
			list, ok := value.([]any)
			if !ok {
				return out, fmt.Errorf("[extension.topcoat] services must be a list of service names, got %T", value)
			}
			for _, item := range list {
				name, ok := item.(string)
				if !ok {
					return out, fmt.Errorf("[extension.topcoat] services must be a list of service names, got %T in it", item)
				}
				out.Services = append(out.Services, name)
			}
		default:
			return out, fmt.Errorf("[extension.topcoat]: unknown key %q (the table takes services)", key)
		}
	}
	return out, nil
}

// OutputSchema is the JSON Schema of outputs.topcoat.
var OutputSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "enabled": {"type": "boolean"},
    "records": {"type": "boolean", "description": "Mirror each type an in-process call returns as a Topcoat record (default true)."},
    "forms": {"type": "boolean", "description": "A form per input type an in-process call takes, and per in-process call whose other arguments a form holds (default true)."},
    "procedures": {"type": "boolean", "description": "A procedure per operation the browser calls, its arguments and result records (default true; needs records)."},
    "views": {"type": "boolean", "description": "Components that render each record as a description list and a table (default true; needs records)."}
  }
}`)

// Config is the decoded outputs.topcoat.
type Config struct {
	Enabled bool `json:"enabled"`
	// Records, Forms, Procedures and Views are nil when the section leaves
	// them out, which is on.
	Records    *bool `json:"records,omitempty"`
	Forms      *bool `json:"forms,omitempty"`
	Procedures *bool `json:"procedures,omitempty"`
	Views      *bool `json:"views,omitempty"`
}

// WritesRecords reports whether the crate mirrors the operations' result
// types as records.
func (c Config) WritesRecords() bool { return c.Records == nil || *c.Records }

// WritesForms reports whether the crate has a form per input type a form
// holds, and per call whose other arguments a form holds.
func (c Config) WritesForms() bool { return c.Forms == nil || *c.Forms }

// WritesProcedures reports whether the crate has a procedure per
// operation. A procedure's arguments and result are records, so it needs
// them.
func (c Config) WritesProcedures() bool {
	return c.WritesRecords() && (c.Procedures == nil || *c.Procedures)
}

// WritesViews reports whether the crate has the display components of
// each record. They render records, so they need them.
func (c Config) WritesViews() bool {
	return c.WritesRecords() && (c.Views == nil || *c.Views)
}

// Dir is where the crate is written for a service.
func Dir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "topcoat", service)
}

// configOf decodes the service's outputs.topcoat. Without the section, a
// service table lists is enabled with every default; any other is the
// zero Config.
func configOf(c registry.GenerateContext, table Table) (Config, error) {
	var cfg Config
	if c.Outputs == nil || c.Outputs.Raw[OutputKey] == nil {
		cfg.Enabled = slices.Contains(table.Services, c.Config.Name)
		return cfg, nil
	}
	err := registry.DecodeOutput(c.Outputs, OutputKey, &cfg)
	return cfg, err
}

// enabled runs the generator for a service whose outputs.topcoat is
// enabled, or which [extension.topcoat] lists, and whose API server is
// Rust, from its config or from build --api-language RUST. A service that
// builds its server in another language skips the output, so one config
// serves a Go build and a Rust one.
func enabled(c registry.GenerateContext, table Table) (bool, string) {
	cfg, err := configOf(c, table)
	if err != nil || !cfg.Enabled {
		return false, "topcoat: outputs.topcoat.enabled is false"
	}
	if api := c.Outputs.API; api == nil || !api.Enabled || api.Language != registry.APILanguageRust {
		return false, "topcoat: the API server is not Rust (outputs.api.language RUST, or build --api-language RUST)"
	}
	return true, ""
}

func generate(c registry.GenerateContext, table Table) error {
	cfg, err := configOf(c, table)
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
