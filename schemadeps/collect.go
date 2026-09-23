package schemadeps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

var cargoPathRE = regexp.MustCompile(`(?m)^([a-zA-Z0-9_-]+)\s*=\s*\{\s*path\s*=\s*"([^"]+)"`)

// CollectFromDist builds a Graph by scanning generated manifests under distRoot.
// Only edges under the configured npm scope / Go module root are recorded;
// superscalar, axios, js-yaml, and other third-party deps are ignored.
//
// producers maps an output directory, relative to distRoot in slash form
// ("types/go/orders"), to the service whose build writes it; build-all
// builds it from every service's output directories. Each package's Service
// is its directory's producer. nil leaves Service empty. A non-nil map that
// lacks a collected package's directory is an error: the graph would name a
// package no service owns, which is what an output directory left behind by
// a removed or renamed service looks like.
func CollectFromDist(distRoot string, producers map[string]string) (*Graph, error) {
	abs, err := filepath.Abs(distRoot)
	if err != nil {
		return nil, err
	}
	g := &Graph{Version: GraphVersion}
	index := make(map[string]*Package) // language/id

	if err := collectTypeScript(abs, index); err != nil {
		return nil, err
	}
	if err := collectGo(abs, index); err != nil {
		return nil, err
	}
	if err := collectPython(abs, index); err != nil {
		return nil, err
	}
	if err := collectRust(abs, index); err != nil {
		return nil, err
	}

	// Ensure SDK -> types edges when both packages exist for a schema.
	ensureSDKTypesEdges(index)

	var orphans []string
	for _, p := range index {
		if producers != nil {
			service, ok := producers[p.Path]
			if !ok {
				orphans = append(orphans, p.Language+"/"+p.ID+" at "+p.Path)
			}
			p.Service = service
		}
		g.Packages = append(g.Packages, *p)
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return nil, fmt.Errorf("schemadeps: %d package(s) under %s were not produced by any schema service (an output directory left from a removed service? delete it and rebuild):\n  %s",
			len(orphans), abs, strings.Join(orphans, "\n  "))
	}
	g.normalize()
	return g, nil
}

// EmitFromDist collects the graph with producers (see CollectFromDist) and
// writes dist/.deps.json. When copyPath is not empty the same bytes are
// written there too.
func EmitFromDist(distRoot string, producers map[string]string, copyPath string) error {
	g, err := CollectFromDist(distRoot, producers)
	if err != nil {
		return err
	}
	if err := Write(DepsPath(distRoot), g); err != nil {
		return err
	}
	if copyPath == "" {
		return nil
	}
	return Write(copyPath, g)
}

func upsert(index map[string]*Package, p Package) *Package {
	key := p.Key()
	if existing, ok := index[key]; ok {
		existing.Deps = uniqueStrings(append(existing.Deps, p.Deps...))
		if existing.Name == "" {
			existing.Name = p.Name
		}
		if existing.Path == "" {
			existing.Path = p.Path
		}
		return existing
	}
	cp := p
	index[key] = &cp
	return &cp
}

func collectTypeScript(distRoot string, index map[string]*Package) error {
	typeDirs := []struct {
		rel  string
		kind string
	}{
		{"types/typescript", "types"},
		{"sdk/typescript", "sdk"},
	}
	for _, td := range typeDirs {
		root := filepath.Join(distRoot, filepath.FromSlash(td.rel))
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, ent := range entries {
			if !ent.IsDir() || ent.Name() == "node_modules" {
				continue
			}
			pkgPath := filepath.Join(root, ent.Name(), "package.json")
			data, err := os.ReadFile(pkgPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			var manifest struct {
				Name             string            `json:"name"`
				Dependencies     map[string]string `json:"dependencies"`
				PeerDependencies map[string]string `json:"peerDependencies"`
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return fmt.Errorf("schemadeps: parse %s: %w", pkgPath, err)
			}
			id := tsPackageID(manifest.Name, td.kind, ent.Name())
			relPath := filepath.ToSlash(filepath.Join(td.rel, ent.Name()))
			deps := tsSchemaDeps(manifest.Dependencies)
			deps = append(deps, tsSchemaDeps(manifest.PeerDependencies)...)
			upsert(index, Package{
				ID:       id,
				Language: "typescript",
				Kind:     td.kind,
				Name:     manifest.Name,
				Path:     relPath,
				Deps:     deps,
			})
		}
	}
	return nil
}

func tsPackageID(npmName, kind, schemaDir string) string {
	if m := npmScopeRE().FindStringSubmatch(npmName); len(m) == 2 {
		return m[1]
	}
	if kind == "sdk" {
		return schemaDir + "-sdk"
	}
	if strings.HasSuffix(schemaDir, "-types") {
		return schemaDir
	}
	return schemaDir + "-types"
}

func tsSchemaDeps(deps map[string]string) []string {
	var out []string
	for name := range deps {
		if m := npmScopeRE().FindStringSubmatch(name); len(m) == 2 {
			out = append(out, m[1])
		}
	}
	return out
}

func collectGo(distRoot string, index map[string]*Package) error {
	groups := []struct {
		rel  string
		kind string
	}{
		{"types/go", "types"},
		{"sdk/go", "sdk"},
		{"api", "api"},
		{"orm", "orm"},
	}
	for _, g := range groups {
		root := filepath.Join(distRoot, filepath.FromSlash(g.rel))
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			modPath := filepath.Join(root, ent.Name(), "go.mod")
			data, err := os.ReadFile(modPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			moduleLine, requires := parseGoMod(string(data))
			id := goPackageID(g.kind, ent.Name())
			relPath := filepath.ToSlash(filepath.Join(g.rel, ent.Name()))
			var deps []string
			for _, req := range requires {
				if depID, ok := goModuleToID(req); ok {
					deps = append(deps, depID)
				}
			}
			upsert(index, Package{
				ID:       id,
				Language: "go",
				Kind:     g.kind,
				Name:     moduleLine,
				Path:     relPath,
				Deps:     deps,
			})
		}
	}
	return nil
}

func parseGoMod(content string) (module string, requires []string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			break
		}
	}
	for _, m := range goRequireRE().FindAllStringSubmatch(content, -1) {
		requires = append(requires, m[1])
	}
	return module, requires
}

func typesPackageID(schemaDir string) string {
	if strings.HasSuffix(schemaDir, "-types") {
		return schemaDir
	}
	return schemaDir + "-types"
}

func goPackageID(kind, schemaDir string) string {
	switch kind {
	case "types":
		return typesPackageID(schemaDir)
	case "sdk":
		return schemaDir + "-sdk"
	case "api":
		return schemaDir + "-api"
	case "orm":
		return schemaDir + "-orm"
	default:
		return schemaDir + "-" + kind
	}
}

func goModuleToID(module string) (string, bool) {
	if !strings.HasPrefix(module, goModulePrefix()) {
		return "", false
	}
	rest := strings.TrimPrefix(module, goModulePrefix())
	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 3 && parts[0] == "types" && parts[1] == "go":
		return typesPackageID(parts[2]), true
	case len(parts) == 3 && parts[0] == "sdk" && parts[1] == "go":
		return parts[2] + "-sdk", true
	case len(parts) == 2 && parts[0] == "api":
		return parts[1] + "-api", true
	case len(parts) == 2 && parts[0] == "orm":
		return parts[1] + "-orm", true
	default:
		return "", false
	}
}

func collectPython(distRoot string, index map[string]*Package) error {
	groups := []struct {
		rel  string
		kind string
	}{
		{"types/python", "types"},
		{"sdk/python", "sdk"},
	}
	for _, g := range groups {
		root := filepath.Join(distRoot, filepath.FromSlash(g.rel))
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, ent := range entries {
			if !ent.IsDir() {
				continue
			}
			pyPath := filepath.Join(root, ent.Name(), "pyproject.toml")
			data, err := os.ReadFile(pyPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			name := pyProjectName(string(data))
			if name == "" {
				stem := strings.ReplaceAll(ent.Name(), "-", "_")
				if g.kind == "sdk" {
					name = naming.Active().PythonSDKModule(stem)
				} else {
					name = naming.Active().PythonTypesModule(stem)
				}
			}
			id := pyPackageID(name, g.kind, ent.Name())
			relPath := filepath.ToSlash(filepath.Join(g.rel, ent.Name()))
			deps := pySchemaDeps(string(data))
			upsert(index, Package{
				ID:       id,
				Language: "python",
				Kind:     g.kind,
				Name:     name,
				Path:     relPath,
				Deps:     deps,
			})
		}
	}
	return nil
}

func pyProjectName(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name") && strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		}
	}
	return ""
}

func pyPackageID(name, kind, schemaDir string) string {
	if stem, ok := pyTypesStem(name); ok {
		return typesPackageID(stem)
	}
	if strings.HasSuffix(name, naming.Active().PythonSDKModuleSuffix) {
		n := naming.Active()
		stem := strings.TrimSuffix(strings.TrimPrefix(name, n.PythonSDKModulePrefix), n.PythonSDKModuleSuffix)
		return strings.ReplaceAll(stem, "_", "-") + "-sdk"
	}
	if kind == "sdk" {
		return schemaDir + "-sdk"
	}
	return typesPackageID(schemaDir)
}

func pyDepToID(name string) string {
	if stem, ok := pyTypesStem(name); ok {
		return typesPackageID(stem)
	}
	if stem, ok := pySDKStem(name); ok {
		return stem + "-sdk"
	}
	return ""
}

func pySchemaDeps(content string) []string {
	var out []string
	for _, m := range pyDepNameRE().FindAllStringSubmatch(content, -1) {
		if id := pyDepToID(m[1]); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func collectRust(distRoot string, index map[string]*Package) error {
	groups := []struct {
		rel  string
		kind string
	}{
		{"types/rust", "types"},
		{"sdk/rust", "sdk"},
		// Rust API crates live beside Go API modules under dist/api/<schema>.
		{"api", "api"},
	}
	for _, g := range groups {
		root := filepath.Join(distRoot, filepath.FromSlash(g.rel))
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		for _, ent := range entries {
			if !ent.IsDir() || ent.Name() == "target" {
				continue
			}
			cargoPath := filepath.Join(root, ent.Name(), "Cargo.toml")
			data, err := os.ReadFile(cargoPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			// dist/api also holds Go modules; only index crates with Cargo.toml.
			name := cargoPackageName(string(data))
			if name == "" {
				continue
			}
			id := rustPackageID(name, g.kind, ent.Name())
			relPath := filepath.ToSlash(filepath.Join(g.rel, ent.Name()))
			deps := rustSchemaDeps(string(data), distRoot, filepath.Join(root, ent.Name()))
			upsert(index, Package{
				ID:       id,
				Language: "rust",
				Kind:     g.kind,
				Name:     name,
				Path:     relPath,
				Deps:     deps,
			})
		}
	}
	return nil
}

func cargoPackageName(content string) string {
	inPackage := false
	for _, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "[package]" {
			inPackage = true
			continue
		}
		if strings.HasPrefix(trim, "[") {
			inPackage = false
			continue
		}
		if inPackage && strings.HasPrefix(trim, "name") {
			parts := strings.SplitN(trim, "=", 2)
			if len(parts) == 2 {
				return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
			}
		}
	}
	return ""
}

func rustPackageID(name, kind, schemaDir string) string {
	if name == naming.Active().ScalarRustCrate {
		// The scalar crate is a dependency, not a generated schema crate.
		return ""
	}
	if stem, ok := rustCrateStem(name, "-types"); ok {
		// Generators append -types even when the schema is already *-types
		// (e.g. connectors-web-types-types). Collapse to one suffix.
		stem = strings.TrimSuffix(stem, "-types")
		if stem == "" {
			return ""
		}
		return typesPackageID(stem)
	}
	if stem, ok := rustCrateStem(name, "-sdk"); ok {
		if stem == "" {
			return ""
		}
		return stem + "-sdk"
	}
	if stem, ok := rustCrateStem(name, "-api"); ok {
		if stem == "" {
			return ""
		}
		return stem + "-api"
	}
	if kind != "" && schemaDir != "" {
		return goPackageID(kind, schemaDir)
	}
	// Not a generated schema crate.
	return ""
}

func rustSchemaDeps(content, distRoot, crateDir string) []string {
	var out []string
	for _, m := range cargoPathRE.FindAllStringSubmatch(content, -1) {
		crateName := m[1]
		if !strings.HasPrefix(crateName, naming.Active().RustCratePrefix) {
			continue
		}
		// Only record generated schema crates (*-types / *-sdk / *-api).
		if id := rustPackageID(crateName, "", ""); id != "" {
			out = append(out, id)
		}
	}
	_ = distRoot
	_ = crateDir
	return out
}

func ensureSDKTypesEdges(index map[string]*Package) {
	for _, p := range index {
		if p.Kind != "sdk" {
			continue
		}
		typesID := strings.TrimSuffix(p.ID, "-sdk") + "-types"
		typesKey := p.Language + "/" + typesID
		if _, ok := index[typesKey]; !ok {
			continue
		}
		has := false
		for _, d := range p.Deps {
			if d == typesID {
				has = true
				break
			}
		}
		if !has {
			p.Deps = append(p.Deps, typesID)
		}
	}
}
