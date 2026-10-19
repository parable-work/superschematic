package topcoat

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/registry"
)

// engineModel is the engine of the API's engine database (D58), which the
// crate opens in-process on SQLite and mounts at /engine: src/engine.rs
// opens it with the database's engine document, which its Rust types
// hold, and the behaviors of the core or of the project's engine crate,
// deploys the document, and serves the engine's routes behind the API's
// authenticator.
type engineModel struct {
	// Database is the engine database and Namespace the namespace its
	// engine types live in; Policy is the access policy's JSON.
	Database  string
	Namespace string
	Policy    string

	// The crates the Topcoat crate depends on for it, each by its path
	// relative to the crate's directory: the engine's, its behaviors' (the
	// core's, or the project's engine crate's) and the database's Rust
	// types.
	EngineCrate, EnginePath       string
	BehaviorsCrate, BehaviorsPath string
	BehaviorsIdent                string
	TypesCrate, TypesPath         string
}

// EngineMount is where the crate mounts the engine's routes.
const EngineMount = "/engine"

// engineOf is the engine model of the API, nil when its database is no
// engine database; it says, in one line of the build's log, why it mounts
// none when the [paths] the crates need are unset.
func engineOf(c registry.GenerateContext, api *registry.RustAPI, dir string) (*engineModel, error) {
	if api.Deps == nil || api.Deps.Engine == nil {
		return nil, nil
	}
	e := api.Deps.Engine
	paths := c.Options.Paths
	if paths.EngineRust == "" || (e.Project == nil && paths.EngineBehaviorsRust == "") {
		c.Logf("  - topcoat: no engine of %s at %s: the naming file's [paths] engine_rust and engine_behaviors_rust name the engine's crates, and one is unset\n", e.Database, EngineMount)
		return nil, nil
	}
	rel := func(target string) (string, error) {
		r, err := filepath.Rel(dir, target)
		if err != nil {
			return "", fmt.Errorf("topcoat: path to %s: %w", target, err)
		}
		return filepath.ToSlash(r), nil
	}
	m := &engineModel{
		Database:    e.Database,
		Namespace:   e.Namespace,
		Policy:      e.Policy,
		EngineCrate: e.Crate,
		TypesCrate:  c.Options.Naming.RustTypesCrate(e.Database),
	}
	var err error
	if m.EnginePath, err = rel(paths.EngineRust); err != nil {
		return nil, err
	}
	if m.TypesPath, err = rel(registry.TypesDir(c.Options.OutputRoot, "rust", e.Database)); err != nil {
		return nil, err
	}
	if e.Project != nil {
		m.BehaviorsCrate, m.BehaviorsIdent = e.Project.Crate, e.Project.Ident
		apiDir := registry.APIDir(c.Options.OutputRoot, c.Config.Name)
		if m.BehaviorsPath, err = rel(filepath.Join(apiDir, filepath.FromSlash(e.Project.Path))); err != nil {
			return nil, err
		}
	} else {
		m.BehaviorsCrate, m.BehaviorsIdent = "superschematic-engine-behaviors", "superschematic_engine_behaviors"
		if m.BehaviorsPath, err = rel(paths.EngineBehaviorsRust); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// EngineIdent is the engine's crate as Rust source spells it.
func (m *engineModel) EngineIdent() string { return strings.ReplaceAll(m.EngineCrate, "-", "_") }

// TypesIdent is the database's Rust types crate as Rust source spells it.
func (m *engineModel) TypesIdent() string { return strings.ReplaceAll(m.TypesCrate, "-", "_") }

// PolicyLiteral is the policy as a Rust raw string literal, or a plain one
// when its JSON holds the raw string's end.
func (m *engineModel) PolicyLiteral() string {
	if strings.Contains(m.Policy, `"#`) {
		return rustString(m.Policy)
	}
	return `r#"` + m.Policy + `"#`
}
