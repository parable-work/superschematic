package identity

import "testing"

// TestBind: Postgres placeholders are numbered in order, and a ? inside a
// quoted name or a literal is left alone.
func TestBind(t *testing.T) {
	const query = `SELECT "a?b", 'c?d' FROM "t""?" WHERE x = ? AND y = ?`
	if got, want := (&SQLStore{dialect: Postgres}).bind(query), `SELECT "a?b", 'c?d' FROM "t""?" WHERE x = $1 AND y = $2`; got != want {
		t.Errorf("Postgres bind = %s, want %s", got, want)
	}
	if got := (&SQLStore{dialect: SQLite}).bind(query); got != query {
		t.Errorf("SQLite bind = %s, want the query as written", got)
	}
}

// TestQuote: a name is quoted with each double quote doubled.
func TestQuote(t *testing.T) {
	for name, want := range map[string]string{"user": `"user"`, `my "user"`: `"my ""user"""`, "a?b c": `"a?b c"`} {
		if got := quote(name); got != want {
			t.Errorf("quote(%q) = %s, want %s", name, got, want)
		}
	}
}

// TestKeyCodec: a UUID key is base62 on the wire and hyphenated in the
// database, either form names it, and any other key is its text.
func TestKeyCodec(t *testing.T) {
	c := newKeyCodec("Identity.UUID")
	if !c.uuid {
		t.Fatal("Identity.UUID is not read as a UUID key")
	}
	const hyphenated = "00000000-0000-4000-8000-000000000001"
	wire := c.toWire(hyphenated)
	for _, in := range []string{wire, hyphenated} {
		if got, ok := c.toDB(in); !ok || got != hyphenated {
			t.Errorf("toDB(%q) = %q, %v; want %s", in, got, ok, hyphenated)
		}
	}
	if _, ok := c.toDB("not a key"); ok {
		t.Error("toDB took a value that is no UUID")
	}
	plain := newKeyCodec("string")
	if plain.uuid || plain.toWire("abc") != "abc" {
		t.Error("a string key is not its text")
	}
	if got, ok := plain.toDB("abc"); !ok || got != "abc" {
		t.Errorf("a string key's database form = %q, %v", got, ok)
	}
	if _, ok := plain.toDB(""); ok {
		t.Error("an empty string key names a row")
	}
}

// TestDummyHashAtConfigCost: the hash an unknown login verifies against
// has the config's cost, so it takes as long as a real one.
func TestDummyHashAtConfigCost(t *testing.T) {
	low := uint32(64)
	one := uint32(1)
	cfg := Config{Password: PasswordConfig{Argon2: Argon2Config{MemoryKiB: &low, Iterations: &one}}}
	s, err := New(nopStore{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := parsePHC(s.dummy)
	if err != nil {
		t.Fatal(err)
	}
	if h.params != cfg.Argon2Params() || len(h.salt) != SaltBytes || len(h.key) != KeyBytes {
		t.Errorf("the dummy hash is %s, want the config's cost %+v", s.dummy, cfg.Argon2Params())
	}
}

// nopStore is a Store whose every method is never called.
type nopStore struct{ Store }

func (nopStore) HasRoles() bool { return false }
