package rustgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestTheNotifierPartiesAndAgentsOnSQLite (D153, D157, D158, #618): a Rust
// types crate that lists the sqlite dialect carries each table's events,
// its notifier part, its party, its conversations part and its agents
// part in the SQLite definition as in the Postgres one, which the runtime's
// notify, inbound, parties and agents modules read on either dialect.
func TestTheNotifierPartiesAndAgentsOnSQLite(t *testing.T) {
	for fixture, wants := range map[string][]string{
		"fixture-notifications-db": {`"events": [`, `"notifier": "inbox"`, `"notifier": "deliveries"`, `"notifier": "channelIdentities"`},
		"fixture-parties-db":       {`"party": {`, `"conversations": "conversations"`, `"conversations": "messages"`},
		"fixture-agent-runs-db":    {`"agents": "definitions"`, `"agents": "runs"`, `"agents": "approvals"`},
	} {
		reg := registry.New(naming.Default())
		schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", fixture, err)
		}
		output, err := Generate(schema, Options{SchemaName: fixture, Registry: reg, SQLite: true})
		if err != nil {
			t.Fatalf("Generate %s on SQLite: %v", fixture, err)
		}
		if output.ORM == nil || output.ORM.SQLite == "" {
			t.Fatalf("%s has no SQLite definition", fixture)
		}
		for _, want := range wants {
			for dialect, definition := range map[string]string{"postgres": output.ORM.Postgres, "sqlite": output.ORM.SQLite} {
				if !strings.Contains(definition, want) {
					t.Errorf("%s's %s definition lacks %s", fixture, dialect, want)
				}
			}
		}
	}
}
