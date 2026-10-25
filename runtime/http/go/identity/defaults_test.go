package identity_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
)

// TestStoreCreateUserWithADefaultedColumn: registration writes a user's
// login and name alone, so a required column of the User table it leaves
// out takes its Default<T, V> from the column's DEFAULT (D78). The DDL is
// the fixture's with a required active: Default<boolean, false>
// (testdata/identity/defaults), as the TypeScript store's defaults.test.ts
// reads it.
func TestStoreCreateUserWithADefaultedColumn(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "identity.sqlite")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(string(readFixture(t, "defaults/sqlite/create.sql"))); err != nil {
		t.Fatalf("apply defaults/sqlite/create.sql: %v", err)
	}
	store, err := identity.NewSQLStore(db, identity.SQLite, descriptorJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	user := mustCreateUser(t, store, "ada@example.com", "Ada")
	var active bool
	if err := db.QueryRow(`SELECT active FROM "user" WHERE email = ?`, user.Login).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active {
		t.Error("active = true, want its default false")
	}
}
