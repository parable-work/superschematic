// Package ext is the acme extension for superschematic: the acceptance test
// of the extension model. It adds, without editing a core file,
//
//   - a schema kind, Catalog, with its own generator (kind.go);
//   - a decorator, @shelf from @acme/schema, that writes into the open
//     extensions slot of a field (decorator.go);
//   - a scalar catalog: the core scalars plus Acme.Photo, a file-upload
//     scalar the Catalog service bounds with uploadMaxBytes (scalars.go);
//   - a sidecar document, catalog.config.yaml, with a generator (document.go);
//   - a generator on the core kinds, the acme manifest (manifest.go);
//   - a build-all hook that merges every service's manifest into one
//     inventory (inventory.go);
//   - a check on the core DB kind: every projection view binds the shop
//     scope setting first (projection_policy.go);
//   - an auth provider, "apikey", that the api generator renders with when
//     superschematic.toml selects it (auth/);
//   - a policy over the core documentation decorators: checks on the @docs
//     audience and the @icon name, and an OpenAPI hook that renames the
//     vendor key (docs.go);
//   - a policy over the core @mcp decorator: every operation of the shop
//     API is classified, the tool documents carry acme's vendor keys, and
//     acme's confirm key replaces the core invocation policy (mcp.go);
//   - two subcommands through cli.CommandProvider: describe (command.go) and
//     fields, which type-checks a declaration file with the loader's
//     compiler (fields.go).
//
// A binary is cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})
// (cmd/acme-schematic). Every file here imports only the public packages an
// out-of-tree extension has: registry, loader, cli and ir, plus the pinned
// TypeScript compiler's shim for the node kinds fields.go matches and the
// superscalar Go package whose rows scalars.go extends.
package ext

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/registry"

	"example.com/acme/schematic/ext/auth"
)

// Name is the extension name: the key of the acme slot under every
// "extensions" object in the IR and of the [extension.acme] table in
// superschematic.toml.
const Name = "acme"

// Package is the npm package the acme decorators are imported from in
// TypeScript schemas. Registering a decorator from it makes it an authoring
// package.
const Package = "@acme/schema"

// Config is the [extension.acme] table of superschematic.toml.
type Config struct {
	// Region is stamped into every acme manifest.
	Region string

	// ProjectionScopeSetting is the Postgres setting every projection view
	// must bind in its first where rule. Empty registers no policy.
	ProjectionScopeSetting string
}

// Extension is what a superschematic binary passes to cli.New.
type Extension struct{}

// Name implements registry.Extension.
func (Extension) Name() string { return Name }

// Register implements registry.Extension: one call per registration surface.
func (Extension) Register(r *registry.Registry) error {
	cfg, err := decodeConfig(r.ExtensionConfig(Name))
	if err != nil {
		return err
	}
	if err := registerKind(r); err != nil {
		return err
	}
	if err := registerDecorator(r); err != nil {
		return err
	}
	if err := registerScalars(r); err != nil {
		return err
	}
	if err := registerDocument(r); err != nil {
		return err
	}
	if err := registerManifest(r, cfg); err != nil {
		return err
	}
	if err := registerInventory(r); err != nil {
		return err
	}
	if err := registerDocsPolicy(r); err != nil {
		return err
	}
	if err := registerMCPPolicy(r); err != nil {
		return err
	}
	if err := registerProjectionPolicy(r, cfg); err != nil {
		return err
	}
	return r.RegisterAuthProvider(auth.Provider{})
}

// Commands implements cli.CommandProvider.
func (Extension) Commands() []*cobra.Command {
	return []*cobra.Command{describeCommand(), fieldsCommand()}
}

// decodeConfig reads the extension's table. The core keeps the table
// undecoded; the extension owns its keys and rejects the ones it does not
// know, so a typo in superschematic.toml fails the build instead of being
// ignored.
func decodeConfig(table map[string]any) (Config, error) {
	var cfg Config
	for key, value := range table {
		switch key {
		case "region", "projection_scope_setting":
			s, ok := value.(string)
			if !ok {
				return cfg, fmt.Errorf("[extension.%s] %s must be a string, got %T", Name, key, value)
			}
			if key == "region" {
				cfg.Region = s
			} else {
				cfg.ProjectionScopeSetting = s
			}
		default:
			return cfg, fmt.Errorf("[extension.%s] has unknown key %q (known: region, projection_scope_setting)", Name, key)
		}
	}
	return cfg, nil
}
