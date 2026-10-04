package sqlite_test

import (
	"testing"

	"github.com/parable-work/superschematic/runtime/migrate/go/sqlite"
)

func TestDSN(t *testing.T) {
	for url, want := range map[string]string{
		"sqlite:///var/lib/app.db":        "/var/lib/app.db",
		"sqlite://app.db":                 "app.db",
		"sqlite:app.db":                   "app.db",
		"SQLite:/tmp/app.db":              "/tmp/app.db",
		"sqlite:///tmp/app.db?mode=rw":    "file:/tmp/app.db?mode=rw",
		"file:app.db?mode=rwc&cache=priv": "file:app.db?mode=rwc&cache=priv",
		"/var/lib/app.db":                 "/var/lib/app.db",
		"app.db":                          "app.db",
		":memory:":                        ":memory:",
	} {
		if got := sqlite.DSN(url); got != want {
			t.Errorf("DSN(%q) = %q, want %q", url, got, want)
		}
	}
}
