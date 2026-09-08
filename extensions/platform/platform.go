// Package platform is a worked example of a psgen extension that adds a
// schema kind: Platform, a service whose schema files declare named
// platforms, each grouping other services under a visibility and listing
// the resources those services share. It registers the kind, one decorator
// (@platform from @superschematic/platform), a verify rule and a generator
// that writes a JSON catalog of the platforms it found.
//
// It exists to show users the whole shape of an extension in one file, so
// it stays small: three constants, one struct, one Register function, one
// verify rule, one generator. A production version of the same kind would
// reference member services through their sentinels (service({...}) values
// from the config package) instead of bare names and close the set of
// shared resource kinds; both are DecoratorSpec.Args and KindSpec.Verify
// changes, not engine changes.
//
// Authoring, TypeScript form:
//
//	import { platform } from "@superschematic/platform";
//
//	@platform({ visibility: "public", services: ["api", "db"], shared: { database: ["api", "db"] } })
//	export abstract class Core {}
//
// The same in the data form:
//
//	types:
//	  Core:
//	    name: Core
//	    role: EmbeddedStruct
//	    extensions:
//	      platform:
//	        platform: { visibility: public, services: [api, db], shared: { database: [api, db] } }
//
// Design: docs/extension-model.md sections 3, 4 and 10.
package platform

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/parable-work/superschematic/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Name is the extension name and the key of its slot under every
// "extensions" object in the IR.
const Name = "platform"

// Kind is the schema kind this extension adds.
const Kind = "Platform"

// Package is the npm package @platform is imported from in TypeScript
// schemas. Registering a decorator from it makes it an authoring package.
const Package = "@superschematic/platform"

// Platform is one named platform: what @platform's argument decodes to and
// what the IR stores under TypeDef.Extensions[Name].platform.
type Platform struct {
	Description string `json:"description,omitempty"`
	// Visibility is "public" or "internal"; Args closes the set.
	Visibility string `json:"visibility"`
	// Services names the member services.
	Services []string `json:"services"`
	// Shared maps a resource kind, any name the author chooses, to the
	// members that share one instance of it.
	Shared map[string][]string `json:"shared,omitempty"`
}

// typeExt is the extension's slot on a type.
type typeExt struct {
	Platform *Platform `json:"platform,omitempty"`
}

// Args is @platform's argument schema. The loader validates every use
// against it before Apply runs, in both forms, so Apply sees only shapes
// that decode into Platform.
var Args = json.RawMessage(`{
	"type": "object",
	"required": ["visibility", "services"],
	"additionalProperties": false,
	"properties": {
		"description": {"type": "string"},
		"visibility": {"type": "string", "enum": ["public", "internal"]},
		"services": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}},
		"shared": {
			"type": "object",
			"additionalProperties": {"type": "array", "items": {"type": "string", "minLength": 1}}
		}
	}
}`)

// Extension is what a psgen binary passes to registry.Assemble.
type Extension struct{}

// Name implements registry.Extension.
func (Extension) Name() string { return Name }

// Register adds the kind, the decorator and the generator.
func (Extension) Register(r *registry.Registry) error {
	if err := r.RegisterKind(registry.KindSpec{
		Name:       Kind,
		Extension:  Name,
		StructRole: ir.RoleEmbeddedStruct,
		// A platform schema groups services; it is not a service others
		// import, so it gets no service.generated.ts sentinel.
		NoSentinel: true,
		Pipeline:   []string{"platformCatalog"},
		Verify:     verify,
	}); err != nil {
		return err
	}
	if err := r.RegisterDecorator(registry.DecoratorSpec{
		Name:      "platform",
		Extension: Name,
		Packages:  []string{Package},
		Target:    registry.TargetType,
		Kinds:     []string{Kind},
		Args:      Args,
		Apply: func(n registry.Node, args []any, _ registry.Site) error {
			var p Platform
			if err := registry.DecodeArgs(args, &p); err != nil {
				return err
			}
			return ir.UpdateExtension(n.Type, Name, func(t *typeExt) { t.Platform = &p })
		},
	}); err != nil {
		return err
	}
	return r.RegisterGenerator(registry.GeneratorSpec{
		Name:      "platformCatalog",
		Extension: Name,
		Kinds:     []string{Kind},
		Dirs: func(c registry.GenerateContext) []string {
			return []string{OutDir(c.Options.OutputRoot, c.Config.Name)}
		},
		Generate: generate,
	})
}

// Of returns the platform declared on td, if any.
func Of(td *ir.TypeDef) (*Platform, bool, error) {
	t, ok, err := ir.GetExtension[typeExt](td, Name)
	if err != nil || !ok || t.Platform == nil {
		return nil, false, err
	}
	return t.Platform, true, nil
}

// verify is the kind's rule: every service a platform shares a resource
// among must be one of its members. The argument schema already closed the
// visibility set and required at least one member.
func verify(schema *ir.Schema, r registry.VerifyReporter) {
	for _, name := range sortedTypes(schema) {
		td := schema.Types[name]
		p, ok, err := Of(td)
		if err != nil {
			r.Errorf(td.Owner, "platform %s: %v", name, err)
			continue
		}
		if !ok {
			continue
		}
		members := map[string]bool{}
		for _, s := range p.Services {
			members[s] = true
		}
		for _, kind := range sortedKeys(p.Shared) {
			for _, s := range p.Shared[kind] {
				if !members[s] {
					r.Errorf(td.Owner, "platform %s: shared %s lists %q, which is not one of its services", name, kind, s)
				}
			}
		}
	}
}

// Catalog is the generator's output: the service's platforms by name.
type Catalog struct {
	Service   string               `json:"service"`
	Platforms map[string]*Platform `json:"platforms"`
}

// OutDir is where the catalog for service is written under outputRoot.
func OutDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "platforms", service)
}

func generate(c registry.GenerateContext) error {
	out := Catalog{Service: c.Config.Name, Platforms: map[string]*Platform{}}
	for name, td := range c.Schema.Types {
		p, ok, err := Of(td)
		if err != nil {
			return fmt.Errorf("platform %s: %w", name, err)
		}
		if ok {
			out.Platforms[name] = p
		}
	}
	dir := OutDir(c.Options.OutputRoot, c.Config.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	c.Result.Outputs["platformCatalog"] = dir
	return nil
}

func sortedTypes(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
