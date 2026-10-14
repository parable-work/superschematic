// Command linkcheck links the three static archives a generated Go server
// can link into one binary, superscalar's, the version graph's and the
// engine's (D58), and calls each: a binary that defined a symbol twice, or
// linked archives two Rust releases built, would not link or would fail
// here. scripts/engine-link-check.sh builds the archives and runs it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	scalars "github.com/parable-work/superscalar/go"
	engine "github.com/parable-work/superschematic/runtime/engine/go"
	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
)

const descriptor = `{"version":3,"root":{"table":"recipe","key":"id"},` +
	`"refTable":"recipe_ref","commitTable":"recipe_commit","patchTable":"recipe_patch",` +
	`"releaseTable":"recipe_release","snapshotTable":"recipe_snapshot_entry",` +
	`"kinds":[{"kind":"step","table":"step","historyTable":"step_history",` +
	`"key":"entity_key","id":"id","ref":"ref","tombstone":"deleted_on_ref","version":"_version",` +
	`"history":{"exclude":[]},` +
	`"columns":{"id":"uuid","entity_key":"uuid","ref":"uuid","deleted_on_ref":"boolean","_version":"integer","title":"string"}}]}`

const schema = `{"kind": "General", "name": "Note", "types": {"Note": {"name": "Note", "role": "EmbeddedStruct",
	"fields": [{"name": "body", "typeRef": {"name": "string"}, "required": true}]}}}`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "linkcheck:", err)
		os.Exit(1)
	}
}

func run() error {
	email, err := scalars.Parse("Contact.Email", "  Ada@Example.COM ")
	if err != nil {
		return fmt.Errorf("superscalar: %w", err)
	}
	fmt.Println("superscalar: Contact.Email", email)

	hashed, err := versiongraph.ContentHash(versiongraph.TreeRequest{
		Descriptor: json.RawMessage(descriptor),
		Tree:       json.RawMessage(`{"step":[]}`),
	})
	if err != nil {
		return fmt.Errorf("versiongraph: %w", err)
	}
	fmt.Println("versiongraph: content hash", hashed.ContentHash)

	dir, err := os.MkdirTemp("", "linkcheck")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	e, err := engine.Open(engine.Config{Path: filepath.Join(dir, "engine.db"), Policy: engine.AllowAll()})
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer e.Close()
	ctx := context.Background()
	if _, err := e.Deploy(ctx, []json.RawMessage{json.RawMessage(schema)}, engine.DeployOptions{}); err != nil {
		return fmt.Errorf("engine: deploy: %w", err)
	}
	created, err := e.Call(ctx, "instances.create", &engine.Principal{Subject: "linkcheck"}, "Note", map[string]string{"body": email})
	if err != nil {
		return fmt.Errorf("engine: create: %w", err)
	}
	info := e.Info()
	fmt.Printf("engine: %s on %s, %d behaviors, created %s\n", info.Project, info.Dialect, len(info.Behaviors), created)
	return nil
}
