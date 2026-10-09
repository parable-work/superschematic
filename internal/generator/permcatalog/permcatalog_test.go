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
// or naming no API is refused.
func TestParseRefuses(t *testing.T) {
	for name, data := range map[string]string{
		"another version":   `{"version": 2, "api": "a", "permissions": []}`,
		"an unknown member": `{"version": 1, "api": "a", "permissions": [], "extra": true}`,
		"no API":            `{"version": 1, "permissions": []}`,
		"not JSON":          `{`,
	} {
		if _, err := Parse([]byte(data)); err == nil || !strings.HasPrefix(err.Error(), "permcatalog: ") {
			t.Errorf("%s: Parse = %v", name, err)
		}
	}
}
