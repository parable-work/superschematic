// Package deploy is the reference deploy extension for superschematic: one
// sidecar document, deploy.values.yaml, that maps a schema's @envVars fields
// to a Helm values file for any chart.
//
// The document lists the value of each environment variable, either inline
// or as a reference to a Kubernetes Secret, and may overlay those values per
// environment. The generator checks the document against the schema's
// @envVars type (every key is a declared field, every @secret field is a
// secretRef, every required field without a default is set) and writes the
// values as a Kubernetes container env list, the shape most charts accept
// under `env:` or `extraEnv:`.
//
// It is deliberately small: no chart templates, no repository URLs, no
// cloud-specific resources. It shows what a document extension looks like
// (docs/extension-model.md section 3.5) and is a starting point for a
// deployment integration of your own. The package uses only the public
// registry package, as any out-of-tree extension would.
package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/registry"
)

// Name is the extension name and the key of the document under
// Schema.Documents and the data form's `documents` section.
const Name = "deploy.values"

// File is the sidecar the loader looks for next to schema.config.*.
const File = "deploy.values.yaml"

// Extension registers the deploy.values document.
type Extension struct{}

// Name implements registry.Extension.
func (Extension) Name() string { return "deploy" }

// Register implements registry.Extension.
func (Extension) Register(r *registry.Registry) error {
	return r.RegisterDocument(registry.DocumentSpec{
		Name:      Name,
		Extension: "deploy",
		File:      File,
		Schema:    Schema,
		Loader: func(_ context.Context, lc registry.LoadContext) (json.RawMessage, []string, error) {
			doc, err := lc.DecodeData(File, Schema)
			return doc, nil, err
		},
		Dirs: func(c registry.GenerateContext) []string {
			return []string{OutDir(c.Options.OutputRoot, c.Config.Name)}
		},
		Generate: generate,
	})
}

// envSchema is the JSON Schema of one env map: each value an inline scalar
// or a secretRef. It is inlined into Schema at both sites rather than shared
// through $defs: the loader composes a document's Schema into the data-form
// schema under documents.<name>, where a root-relative $ref would no longer
// resolve.
const envSchema = `{
      "type": "object",
      "additionalProperties": {
        "oneOf": [
          {"type": "string"},
          {"type": "number"},
          {"type": "boolean"},
          {
            "type": "object",
            "additionalProperties": false,
            "required": ["secretRef"],
            "properties": {
              "secretRef": {
                "type": "object",
                "additionalProperties": false,
                "required": ["name", "key"],
                "properties": {"name": {"type": "string"}, "key": {"type": "string"}}
              }
            }
          }
        ]
      }
    }`

// Schema is the JSON Schema of deploy.values.yaml. The loader validates the
// file against it before the document reaches the IR; the generator then
// checks the keys against the schema's @envVars type, which a static schema
// cannot express.
var Schema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "env": ` + envSchema + `,
    "environments": {
      "type": "object",
      "additionalProperties": {
        "type": "object",
        "additionalProperties": false,
        "properties": {"env": ` + envSchema + `}
      }
    }
  }
}`)

// Document is the decoded deploy.values.yaml.
type Document struct {
	// Env is the base layer: values every environment starts from.
	Env map[string]Value `json:"env,omitempty"`
	// Environments overlays the base layer by environment name.
	Environments map[string]Layer `json:"environments,omitempty"`
}

// Layer is one environment's overlay.
type Layer struct {
	Env map[string]Value `json:"env,omitempty"`
}

// Value is an environment variable's source: an inline scalar or a
// Kubernetes Secret reference. Exactly one is set.
type Value struct {
	Inline    *string
	SecretRef *SecretRef
}

// SecretRef names a key of a Kubernetes Secret.
type SecretRef struct {
	Name string `json:"name" yaml:"name"`
	Key  string `json:"key" yaml:"key"`
}

// UnmarshalJSON accepts a string, number, boolean or {"secretRef": {...}}.
// Inline scalars become strings because a container env value is a string.
func (v *Value) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '{' {
		var ref struct {
			SecretRef *SecretRef `json:"secretRef"`
		}
		if err := json.Unmarshal(data, &ref); err != nil {
			return err
		}
		*v = Value{SecretRef: ref.SecretRef}
		return nil
	}
	var scalar any
	if err := json.Unmarshal(data, &scalar); err != nil {
		return err
	}
	var s string
	switch x := scalar.(type) {
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		s = strconv.FormatBool(x)
	default:
		return fmt.Errorf("env value must be a string, number, boolean or secretRef, got %s", data)
	}
	*v = Value{Inline: &s}
	return nil
}

// OutDir is where the values files for a service are written.
func OutDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "deploy", "values", service)
}

// envVar is one entry of the emitted env list, in Kubernetes EnvVar shape.
type envVar struct {
	Name      string     `yaml:"name"`
	Value     *string    `yaml:"value,omitempty"`
	ValueFrom *valueFrom `yaml:"valueFrom,omitempty"`
}

type valueFrom struct {
	SecretKeyRef SecretRef `yaml:"secretKeyRef"`
}

type valuesFile struct {
	Env []envVar `yaml:"env"`
}

// generate writes values.yaml for the base layer and values.<env>.yaml for
// every environment, each the base layer with the environment's overlay
// applied. When the document declares environments, completeness (every
// required field without a default is set) is checked per environment;
// otherwise the base layer must be complete on its own.
func generate(c registry.GenerateContext, raw json.RawMessage) error {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", File, err)
	}
	cfg, err := c.EnvConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("%s: schema %s declares no @envVars type to map values onto", File, c.Config.Name)
	}
	fields := map[string]registry.EnvConfigField{}
	for _, f := range cfg.Fields {
		fields[f.Key] = f
	}

	if err := checkKeys(fields, cfg.TypeName, "env", doc.Env); err != nil {
		return err
	}
	layers := map[string]map[string]Value{"values.yaml": doc.Env}
	if len(doc.Environments) == 0 {
		if err := checkComplete(cfg.Fields, "env", doc.Env); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(doc.Environments)) {
		where := "environments." + name + ".env"
		overlay := doc.Environments[name].Env
		if err := checkKeys(fields, cfg.TypeName, where, overlay); err != nil {
			return err
		}
		merged := make(map[string]Value, len(doc.Env)+len(overlay))
		for k, v := range doc.Env {
			merged[k] = v
		}
		for k, v := range overlay {
			merged[k] = v
		}
		if err := checkComplete(cfg.Fields, where, merged); err != nil {
			return err
		}
		layers["values."+name+".yaml"] = merged
	}

	dir := OutDir(c.Options.OutputRoot, c.Config.Name)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, file := range slices.Sorted(maps.Keys(layers)) {
		out, err := render(layers[file])
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, file), out, 0o644); err != nil {
			return err
		}
	}
	c.Done(Name, dir)
	return nil
}

// checkKeys rejects keys that are not @envVars fields and inline values for
// @secret fields.
func checkKeys(fields map[string]registry.EnvConfigField, typeName, where string, env map[string]Value) error {
	for _, key := range slices.Sorted(maps.Keys(env)) {
		f, ok := fields[key]
		if !ok {
			return fmt.Errorf("%s: %s.%s is not a field of %s", File, where, key, typeName)
		}
		if f.Secret && env[key].SecretRef == nil {
			return fmt.Errorf("%s: %s.%s is @secret and must be a secretRef, not an inline value", File, where, key)
		}
	}
	return nil
}

// checkComplete requires a value for every required field without a default.
func checkComplete(fields []registry.EnvConfigField, where string, env map[string]Value) error {
	var missing []string
	for _, f := range fields {
		if _, ok := env[f.Key]; !ok && f.Required && !f.HasDefault {
			missing = append(missing, f.Key)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("%s: %s is missing required fields with no default: %v", File, where, missing)
	}
	return nil
}

// render encodes one layer as a values file with a sorted env list.
func render(env map[string]Value) ([]byte, error) {
	vf := valuesFile{Env: []envVar{}}
	for _, key := range slices.Sorted(maps.Keys(env)) {
		v := env[key]
		entry := envVar{Name: key, Value: v.Inline}
		if v.SecretRef != nil {
			entry.ValueFrom = &valueFrom{SecretKeyRef: *v.SecretRef}
		}
		vf.Env = append(vf.Env, entry)
	}
	body, err := yaml.Marshal(vf)
	if err != nil {
		return nil, err
	}
	header := "# Generated by superschematic (extensions/deploy) from " + File + ". Do not edit.\n"
	return append([]byte(header), body...), nil
}
