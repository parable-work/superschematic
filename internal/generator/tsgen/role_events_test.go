package tsgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// roleEventsFixture's Role table declares an @enqueueOnChange of its
// grants (#625) and one of its own writes.
const roleEventsFixture = "fixture-role-events-db"

// roleEventsRun is role_events.test.ts on either dialect.
var roleEventsRun = featureRun{
	fixture: roleEventsFixture,
	test:    "role_events.test.ts",
	cases: []string{
		"a grant of a role and its revocation each enqueue the role grant event",
		"the role table's own declaration enqueues on a rename, and not on a grant",
	},
}

// TestARoleGrantRaisesItsEvent (#625): the loader copies the role table's
// declaration of its grants onto UserRoleGrant, with of unset, and keeps
// it on the role table, which writes no trigger of it; create.sql holds
// the grants' trigger in both dialects. TestARoleGrantRaisesItsEventOn*
// then grant a role and revoke it through the generated repositories.
func TestARoleGrantRaisesItsEvent(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, roleEventsFixture), loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatalf("load %s: %v", roleEventsFixture, err)
	}
	grants := schema.Types[ir.IdentityRoleGrantTable]
	if grants == nil || len(grants.EnqueueOnChange) != 1 {
		t.Fatalf("UserRoleGrant's @enqueueOnChange = %+v", grants)
	}
	if got := grants.EnqueueOnChange[0]; got.Queue != "RoleGrantChanged" || got.Of != "" || got.Message["grant"] != "id" || strings.Join(got.On, ",") != "insert,delete" {
		t.Errorf("the grants' declaration on UserRoleGrant = %+v", got)
	}
	role := schema.Types["Role"]
	if len(role.EnqueueOnChange) != 2 || role.EnqueueOnChange[0].Of != ir.EnqueueOfGrants {
		t.Errorf("Role's declarations = %+v, want its grants' kept beside its own", role.EnqueueOnChange)
	}
	out, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: roleEventsFixture})
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for _, trigger := range out.EnqueueTriggers {
		tables = append(tables, trigger.Table+"/"+trigger.Queue)
	}
	if strings.Join(tables, " ") != "role/RoleChanged user_role_grant/RoleGrantChanged" {
		t.Errorf("the enqueue triggers are on %v", tables)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: roleEventsFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	sqlite, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"AFTER INSERT ON \"user_role_grant\"", "AFTER DELETE ON \"user_role_grant\""} {
		if !strings.Contains(sqlite, want) {
			t.Errorf("SQLite's create.sql lacks %s", want)
		}
	}
}

// TestARoleGrantRaisesItsEventOnPostgres runs role_events.test.ts on
// Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL.
func TestARoleGrantRaisesItsEventOnPostgres(t *testing.T) {
	runFeature(t, roleEventsRun, false)
}

// TestARoleGrantRaisesItsEventOnSQLite runs it on SQLite (D189).
func TestARoleGrantRaisesItsEventOnSQLite(t *testing.T) {
	runFeature(t, roleEventsRun, true)
}
