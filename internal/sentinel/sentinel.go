// Package sentinel emits the generated service sentinel files that let one
// schema service reference another as a value: src/service.generated.ts
// (export const WebDb = service({...})) plus the re-export line in the
// service's src/index.ts. Sentinels are the input to authDb-style config
// references and to any registered decorator that takes services as
// arguments. Kinds whose KindSpec sets NoSentinel are groupings, not
// members, and get none.
//
// This is the only superschematic package that writes into the authoring tree; every
// write is idempotent (read-compare-write) so committed sentinels stay
// byte-stable across builds.
package sentinel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// GeneratedFile is the sentinel path relative to the service directory.
const GeneratedFile = "src/service.generated.ts"

// indexExportLine is the line EmitService maintains in src/index.ts.
const indexExportLine = `export * from "./service.generated";`

// Options configures EnsureSiblings.
type Options struct {
	// ReadTSIdentity reads the name and kind of a TypeScript-form
	// schema.config.ts for a service directory. Wired to
	// tsreader.ReadServiceIdentity, which evaluates no other key, so a
	// config that imports a sibling's sentinel is read before that sentinel
	// exists (D34). Kept as a hook so this package does not depend on the
	// tsreader.
	ReadTSIdentity func(servicePath string) (*schemaconfig.SchemaConfig, error)

	// Registry supplies the kind set and the KindSpec.NoSentinel flag per
	// kind. nil means the core kinds, each of which gets a sentinel.
	Registry *registry.Registry

	// Log receives progress lines when set.
	Log io.Writer
}

// Skips reports whether services of kind get no sentinel: the registered
// KindSpec sets NoSentinel. A nil registry skips nothing.
func Skips(reg *registry.Registry, kind ir.SchemaKind) bool {
	if reg == nil {
		return false
	}
	spec, ok := reg.Kind(string(kind))
	return ok && spec.NoSentinel
}

// ConfigPackage is the authoring package that declares service and
// SchemaKind; the sentinel imports it through the specifier the naming's
// [package_aliases] table gives it.
const ConfigPackage = "@superschematic/schema-config"

// ConfigType is the type an API's handle carries as its config type: the
// service's @envVars class, which the sentinel imports as a type
// (docs/stack-model.md, section 4.3).
type ConfigType struct {
	// Name is the class name.
	Name string

	// Module is the class's module, relative to the sentinel
	// ("./config.schema").
	Module string
}

// ConfigTypeOf returns the config type an API schema's sentinel names: its
// one @envVars class, when a TypeScript schema file declares it. Only an
// API handle has a config type. An @envVars class from a data-form schema
// file has no TypeScript type to import, so its handle has none either.
func ConfigTypeOf(schema *ir.Schema) *ConfigType {
	if schema == nil || schema.Kind != ir.SchemaKindAPI {
		return nil
	}
	var found *ir.TypeDef
	for _, def := range schema.Types {
		if !def.EnvVars {
			continue
		}
		if found != nil {
			return nil
		}
		found = def
	}
	if found == nil || !strings.HasPrefix(found.Owner, "src/") || !strings.HasSuffix(found.Owner, ".ts") {
		return nil
	}
	return &ConfigType{Name: found.Name, Module: "./" + strings.TrimSuffix(strings.TrimPrefix(found.Owner, "src/"), ".ts")}
}

// Content renders the sentinel file for a service. The SchemaKind enum in
// the schema-config package is closed over the core kinds (DB, API,
// General; the ir constants), so those render as the enum member, which
// keeps committed sentinels byte-stable. Any other kind is an extension's
// and has no member: it renders as a string literal, which the kind slot's
// SchemaKindName type admits. Callers check the kind against their registry
// first (EmitService does).
//
// A non-nil configType makes the handle typed: the sentinel imports the
// class as a type and passes the kind and the class as service's type
// arguments, `service<"API", ShopApiConfig>({...})`. Without one, service
// infers the kind from the argument.
func Content(name string, kind ir.SchemaKind, n naming.Naming, configType *ConfigType) string {
	handle := tsutil.ToClassName(name)
	specifier := n.OrDefault().Specifier(ConfigPackage)
	imports := "service"
	kindExpr := fmt.Sprintf("%q", string(kind))
	switch kind {
	case ir.SchemaKindDB, ir.SchemaKindAPI, ir.SchemaKindGeneral:
		imports = "service, SchemaKind"
		kindExpr = "SchemaKind." + string(kind)
	}
	typeImport, typeArgs := "", ""
	if configType != nil {
		typeImport = fmt.Sprintf("import type { %s } from %q;\n", configType.Name, configType.Module)
		typeArgs = fmt.Sprintf("<%q, %s>", string(kind), configType.Name)
	}
	return fmt.Sprintf(`// Code generated by superschematic sentinel. DO NOT EDIT.
import { %s } from %q;
%sexport const %s = service%s({ name: %q, kind: %s });
`, imports, specifier, typeImport, handle, typeArgs, name, kindExpr)
}

// configTypeImport matches the type import Content writes for a typed
// handle.
var configTypeImport = regexp.MustCompile(`(?m)^import type \{ (\w+) \} from "([^"]+)";$`)

// writtenConfigType returns the config type of the sentinel at path, as a
// build wrote it, or nil.
func writtenConfigType(path string) *ConfigType {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	match := configTypeImport.FindStringSubmatch(string(data))
	if match == nil {
		return nil
	}
	return &ConfigType{Name: match[1], Module: match[2]}
}

// EmitService writes the service's sentinel file and ensures the index
// re-export. It reports whether anything on disk changed. A kind the
// registry marks NoSentinel is an error: callers decide to skip those.
// configType types an API's handle (ConfigTypeOf); it is nil for every other
// kind.
func EmitService(servicePath string, cfg *schemaconfig.SchemaConfig, reg *registry.Registry, configType *ConfigType) (bool, error) {
	if reg != nil && !reg.KnowsKind(cfg.Kind) {
		return false, fmt.Errorf("unknown schema kind %q (registered kinds: %s)", cfg.Kind, strings.Join(reg.Kinds(), ", "))
	}
	if Skips(reg, cfg.Kind) {
		return false, fmt.Errorf("%s services do not get a sentinel", cfg.Kind)
	}
	path := filepath.Join(servicePath, filepath.FromSlash(GeneratedFile))
	var n naming.Naming
	if reg != nil {
		n = reg.Naming()
	}
	if cfg.Kind != ir.SchemaKindAPI {
		configType = nil
	}
	sentinelChanged, err := writeIfChanged(path, Content(cfg.Name, cfg.Kind, n, configType))
	if err != nil {
		return false, err
	}
	indexChanged, err := ensureIndexExport(servicePath)
	if err != nil {
		return false, err
	}
	return sentinelChanged || indexChanged, nil
}

// EnsureSiblings emits sentinels for every service directory under
// servicesRoot, so a schema that imports its siblings' sentinels sees them
// on disk at program-construction time. Skipped silently: directories
// without a tsconfig.json (no TypeScript module surface to import), without
// a schema.config.*, and services of a NoSentinel kind. A sentinel newer than the
// service config is considered fresh; only the index re-export is
// re-checked for those.
//
// The sweep reads configs, not schemas, so it cannot find an API's @envVars
// class. It keeps the config type the service's own build wrote into the
// sentinel instead, so a sweep after a config edit does not strip the
// handle's type until that service builds again.
func EnsureSiblings(servicesRoot string, opts Options) error {
	entries, err := os.ReadDir(servicesRoot)
	if err != nil {
		return fmt.Errorf("scanning services root %s: %w", servicesRoot, err)
	}
	if opts.Registry == nil {
		opts.Registry = registry.New(naming.Naming{})
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		dir := filepath.Join(servicesRoot, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err != nil {
			continue
		}
		configTime, ok := newestConfigTime(dir)
		if !ok {
			continue
		}

		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(GeneratedFile))); err == nil && info.ModTime().After(configTime) {
			if _, err := ensureIndexExport(dir); err != nil {
				return fmt.Errorf("service %s: %w", entry.Name(), err)
			}
			continue
		}

		cfg, err := schemaconfig.ReadFile(dir, opts.Registry)
		if errors.Is(err, os.ErrNotExist) {
			if opts.ReadTSIdentity == nil {
				continue
			}
			cfg, err = opts.ReadTSIdentity(dir)
		}
		if err != nil {
			return fmt.Errorf("service %s: reading config: %w", entry.Name(), err)
		}
		if Skips(opts.Registry, cfg.Kind) {
			continue
		}
		changed, err := EmitService(dir, cfg, opts.Registry, writtenConfigType(filepath.Join(dir, filepath.FromSlash(GeneratedFile))))
		if err != nil {
			return fmt.Errorf("service %s: %w", entry.Name(), err)
		}
		if changed && opts.Log != nil {
			_, _ = fmt.Fprintf(opts.Log, "  + sentinel written for %s\n", cfg.Name)
		}
	}
	return nil
}

// newestConfigTime returns the most recent modification time across the
// service's schema.config.* forms, or ok=false when the directory has none
// (it is not a schema service).
func newestConfigTime(servicePath string) (newest time.Time, ok bool) {
	for _, name := range []string{"schema.config.ts", "schema.config.json", "schema.config.yaml"} {
		info, err := os.Stat(filepath.Join(servicePath, name))
		if err != nil {
			continue
		}
		ok = true
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest, ok
}

// writeIfChanged writes content to path only when the current bytes differ,
// creating parent directories as needed.
func writeIfChanged(path, content string) (bool, error) {
	if current, err := os.ReadFile(path); err == nil && string(current) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// ensureIndexExport makes sure src/index.ts re-exports the sentinel. A
// missing index is created holding just the re-export; an existing one gets
// the line appended unless an equivalent line (either quote style) is
// already present. Hand-written exports are never touched.
func ensureIndexExport(servicePath string) (bool, error) {
	path := filepath.Join(servicePath, "src", "index.ts")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		content := "// The service.generated re-export is managed by superschematic; keep it in place.\n" + indexExportLine + "\n"
		return writeIfChanged(path, content)
	}
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == indexExportLine || trimmed == `export * from './service.generated';` {
			return false, nil
		}
	}
	updated := string(data)
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	updated += indexExportLine + "\n"
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
