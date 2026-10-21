package permcatalog

import (
	"slices"
	"strings"
	"testing"
)

// TestBuild: each permission is listed once, by name, with the operations
// that name it, sorted and each once; it is the identity routes' when one
// of them names it, whoever else does.
func TestBuild(t *testing.T) {
	c, ok := Build("shop-api", "shop-db", []Operation{
		{ID: "OrdersListHandler", Permissions: []string{"orders.read", "orders"}},
		{ID: "AdminListUsersHandler", Permissions: []string{"identity.users.read"}, Identity: true},
		{ID: "OrdersCreateHandler", Permissions: []string{"orders.write", "orders.write"}},
		{ID: "ReportsUsersHandler", Permissions: []string{"identity.users.read"}},
		{ID: "HealthHandler"},
		{ID: "OrdersExportHandler", Permissions: []string{"orders.read"}},
	})
	if !ok {
		t.Fatal("Build found no permission")
	}
	if c.Version != Version || c.API != "shop-api" || c.AuthDB != "shop-db" {
		t.Errorf("header = %d %s %s", c.Version, c.API, c.AuthDB)
	}
	want := []Permission{
		{Name: "identity.users.read", Identity: true, Operations: []string{"AdminListUsersHandler", "ReportsUsersHandler"}},
		{Name: "orders", Operations: []string{"OrdersListHandler"}},
		{Name: "orders.read", Operations: []string{"OrdersExportHandler", "OrdersListHandler"}},
		{Name: "orders.write", Operations: []string{"OrdersCreateHandler"}},
	}
	if !slices.EqualFunc(c.Permissions, want, func(a, b Permission) bool {
		return a.Name == b.Name && a.Identity == b.Identity && slices.Equal(a.Operations, b.Operations)
	}) {
		t.Errorf("permissions = %+v\nwant %+v", c.Permissions, want)
	}

	if _, ok := Build("plain-api", "", []Operation{{ID: "PingHandler"}}); ok {
		t.Error("Build made a catalog for an API whose operations name no permission")
	}
}

// TestJSONRoundTrip: the catalog encodes with its members in order,
// leaves out an empty authDb, and parses back.
func TestJSONRoundTrip(t *testing.T) {
	c, _ := Build("plain-api", "", []Operation{{ID: "PingHandler", Permissions: []string{"ping"}}})
	data, err := c.JSON()
	if err != nil {
		t.Fatal(err)
	}
	const want = `{
  "version": 1,
  "api": "plain-api",
  "permissions": [
    {
      "name": "ping",
      "identity": false,
      "operations": [
        "PingHandler"
      ]
    }
  ]
}
`
	if string(data) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", data, want)
	}
	back, err := Parse(data)
	if err != nil || !slices.Equal(back.Names(), []string{"ping"}) {
		t.Errorf("Parse = %+v, %v", back, err)
	}
}

// TestParseRefuses: a catalog of another version, with an unknown member,
// with an engine member in version 1, or naming no API is refused.
func TestParseRefuses(t *testing.T) {
	for name, data := range map[string]string{
		"another version":          `{"version": 3, "api": "a", "permissions": []}`,
		"an unknown member":        `{"version": 1, "api": "a", "permissions": [], "extra": true}`,
		"an engine member in v1":   `{"version": 1, "api": "a", "permissions": [{"name": "T.read", "identity": false, "operations": [], "engine": {"database": "d", "namespace": "d", "schema": "T", "action": "read"}}]}`,
		"an unknown engine member": `{"version": 2, "api": "a", "permissions": [{"name": "T.read", "identity": false, "operations": [], "engine": {"database": "d", "namespace": "d", "schema": "T", "action": "read", "extra": 1}}]}`,
		"no API":                   `{"version": 1, "permissions": []}`,
		"not JSON":                 `{`,
	} {
		if _, err := Parse([]byte(data)); err == nil || !strings.HasPrefix(err.Error(), "permcatalog: ") {
			t.Errorf("%s: Parse = %v", name, err)
		}
	}
}

// TestEnginePermissions: what the engine's permission policy asks of an
// engine type is listed under its name with the question it answers, and
// with the operations that name it too when one does; a catalog that lists
// one is version 2, which parses back, and one that lists none stays
// version 1. An engine alone makes a catalog of an API whose operations
// name no permission.
func TestEnginePermissions(t *testing.T) {
	engine := []EnginePermission{
		{Database: "desk-db", Namespace: "desk", Schema: "Ticket", Action: "write"},
		{Database: "desk-db", Namespace: "desk", Schema: "Ticket", Action: "read"},
	}
	c, ok := Build("desk-api", "desk-db", []Operation{{ID: "ReportHandler", Permissions: []string{"Ticket.read"}}}, engine...)
	if !ok {
		t.Fatal("Build found no permission")
	}
	if c.Version != EngineVersion {
		t.Errorf("version = %d, want %d", c.Version, EngineVersion)
	}
	if got := c.Names(); !slices.Equal(got, []string{"Ticket.read", "Ticket.write"}) {
		t.Errorf("names = %v", got)
	}
	read, write := c.Permissions[0], c.Permissions[1]
	if !slices.Equal(read.Operations, []string{"ReportHandler"}) || read.Engine == nil || *read.Engine != engine[1] {
		t.Errorf("Ticket.read = %+v", read)
	}
	if len(write.Operations) != 0 || write.Operations == nil || write.Engine == nil || *write.Engine != engine[0] {
		t.Errorf("Ticket.write = %+v", write)
	}
	data, err := c.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 2`) || !strings.Contains(string(data), `"operations": [],`) {
		t.Errorf("JSON =\n%s", data)
	}
	back, err := Parse(data)
	if err != nil || back.Version != EngineVersion || back.Permissions[1].Engine == nil {
		t.Errorf("Parse = %+v, %v", back, err)
	}

	alone, ok := Build("desk-api", "", nil, engine[0])
	if !ok || alone.Version != EngineVersion || !slices.Equal(alone.Names(), []string{"Ticket.write"}) {
		t.Errorf("an engine's catalog alone = %+v, %v", alone, ok)
	}
	plain, _ := Build("plain-api", "", []Operation{{ID: "PingHandler", Permissions: []string{"ping"}}})
	if plain.Version != Version {
		t.Errorf("a catalog without the engine is version %d", plain.Version)
	}
}
