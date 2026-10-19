package engine

import (
	"encoding/json"
	"strconv"
)

// Config is how an engine opens: its storage, its access policy and the
// rest of the engine's options, as the archive reads them
// (runtime/engine/rust/ffi/src/config.rs). A member left zero is the
// engine's default.
type Config struct {
	// Path opens a SQLite file, created if absent, which one process
	// writes: a test, a local run, a Topcoat app.
	Path string `json:"path,omitempty"`
	// Postgres opens a schema of a Postgres database, in place of Path,
	// which every instance of a server shares.
	Postgres *Postgres `json:"postgres,omitempty"`
	// BusyTimeoutMs is how long a write waits for another writer's lock
	// before it is refused busy (5000 unless set).
	BusyTimeoutMs *int64 `json:"busyTimeoutMs,omitempty"`
	// Policy is who may read, write, define and publish: AllowAll (tests
	// and local use), PermissionPolicy, or RulesPolicy. There is no
	// default; an engine opened without one is refused.
	Policy Policy `json:"policy,omitempty"`
	// Namespaces are the configured namespaces besides default, and the
	// shared one.
	Namespaces *Namespaces `json:"namespaces,omitempty"`
	// IDs is how a create without an id makes one: UUIDs unless set.
	IDs IDs `json:"ids,omitempty"`
	// Clock is what time it is to the engine: the system's unless set.
	Clock Clock `json:"clock,omitempty"`
	// MetaSchema is the meta-schema documents load against, the
	// `superschematic json-schema` output of the deployment's binary; the
	// core's when nil.
	MetaSchema json.RawMessage `json:"metaSchema,omitempty"`
	// Values are the value store's limits.
	Values *Values `json:"values,omitempty"`
	// Runner is the principal behaviors' reactions and schedules act as,
	// and its limits; without it the runner refuses to run.
	Runner *Runner `json:"runner,omitempty"`
	// Retention is how long the event log keeps events, pruned on the
	// runner; without it the log keeps every event.
	Retention *Retention `json:"retention,omitempty"`
	// Tools are the invocation policy and vendor keys of the describe and
	// tools documents and the MCP tools (the core's unless set).
	Tools json.RawMessage `json:"tools,omitempty"`
	// Project is the project's own config, which the archive's project
	// reads; the core's archive takes none.
	Project json.RawMessage `json:"project,omitempty"`

	// Concurrency is how many calls the binding makes at once, each on a
	// thread of its own while it runs in the archive; the rest wait for a
	// slot. Unless set, it is the Postgres pool's size (PoolMax, 4 unless
	// set), since a call past it would only wait in the archive for a
	// connection, and 8 on SQLite.
	Concurrency int `json:"-"`
}

// Postgres is a schema of a Postgres database.
type Postgres struct {
	// URL is the database's URL (postgres://...) or a key-value string.
	URL string `json:"url"`
	// Schema holds the engine's tables, created if absent: engine unless
	// set.
	Schema string `json:"schema,omitempty"`
	// PoolMax is the most connections the engine opens (4 unless set).
	PoolMax int `json:"poolMax,omitempty"`
	// Listen keeps a listener connection, so a commit in another process
	// wakes this one's streams and runner (true unless set).
	Listen *bool `json:"listen,omitempty"`
	// ConnectTimeoutMs bounds opening a connection, end to end.
	ConnectTimeoutMs int64 `json:"connectTimeoutMs,omitempty"`
	// MigrationTimeoutMs bounds a migration's wait for another process's.
	MigrationTimeoutMs int64 `json:"migrationTimeoutMs,omitempty"`
	// IdleInTransactionTimeoutMs is how long the server lets a session sit
	// idle inside a transaction before it ends it, so a process that stops
	// mid-transaction holds its locks no longer (60000 unless set; 0 leaves
	// the server's own setting).
	IdleInTransactionTimeoutMs *int64 `json:"idleInTransactionTimeoutMs,omitempty"`
}

// Namespaces are the configured namespaces besides default.
type Namespaces struct {
	Names []string `json:"names,omitempty"`
	// Shared names the namespace every other one reaches.
	Shared string `json:"shared,omitempty"`
}

// Values are the value store's limits, each the engine's default when nil.
type Values struct {
	ThresholdBytes *int64 `json:"thresholdBytes,omitempty"`
	CacheBytes     *int64 `json:"cacheBytes,omitempty"`
	MaxBytes       *int64 `json:"maxBytes,omitempty"`
}

// Runner is the runner's principal and its limits, each the engine's
// default when nil.
type Runner struct {
	Principal      Principal `json:"principal"`
	MaxDepth       *int64    `json:"maxDepth,omitempty"`
	MaxAttempts    *int64    `json:"maxAttempts,omitempty"`
	RetryInitialMs *int64    `json:"retryInitialMs,omitempty"`
	RetryMaxMs     *int64    `json:"retryMaxMs,omitempty"`
	BatchSize      *int64    `json:"batchSize,omitempty"`
	LeaseRetryMs   *int64    `json:"leaseRetryMs,omitempty"`
}

// Retention is how long the event log keeps events.
type Retention struct {
	MaxAgeMs  *int64 `json:"maxAgeMs,omitempty"`
	MaxEvents *int64 `json:"maxEvents,omitempty"`
	MaxHoldMs *int64 `json:"maxHoldMs,omitempty"`
	EveryMs   *int64 `json:"everyMs,omitempty"`
	BatchSize *int64 `json:"batchSize,omitempty"`
}

// Policy is the access policy as the engine reads it: a shorthand or rules.
type Policy json.RawMessage

// MarshalJSON writes the policy as it is.
func (p Policy) MarshalJSON() ([]byte, error) {
	if len(p) == 0 {
		return []byte("null"), nil
	}
	return p, nil
}

// AllowAll allows everything: for tests and local use.
func AllowAll() Policy { return Policy(`"allowAll"`) }

// PermissionPolicy allows an action on a schema to a caller holding
// `<schema>.<action>`, and no question that names no schema.
func PermissionPolicy() Policy { return Policy(`"permission"`) }

// RulesPolicy is ordered rules, the first that matches a question deciding
// it (`{"action": ..., "require": ...}` or `{..., "allow": false}`; see the
// engine's access module), a question no rule matches refused.
func RulesPolicy(rules ...json.RawMessage) Policy {
	document, _ := json.Marshal(map[string][]json.RawMessage{"rules": rules})
	return Policy(document)
}

// IDs is how a create without an id makes one.
type IDs json.RawMessage

// MarshalJSON writes the ids as they are.
func (i IDs) MarshalJSON() ([]byte, error) {
	if len(i) == 0 {
		return []byte("null"), nil
	}
	return i, nil
}

// UUIDs are random version 4 UUIDs, the default.
func UUIDs() IDs { return IDs(`"uuid"`) }

// SequenceIDs are <prefix>1, <prefix>2, ...: one count per engine, for
// tests.
func SequenceIDs(prefix string) IDs { return IDs(`{"sequence":` + strconv.Quote(prefix) + `}`) }

// SeededIDs are the scenario recorder's: the n-th is a version 4 UUID made
// of SHA-256(<prefix><n>).
func SeededIDs(prefix string) IDs { return IDs(`{"seeded":` + strconv.Quote(prefix) + `}`) }

// Clock is what time it is to the engine.
type Clock json.RawMessage

// MarshalJSON writes the clock as it is.
func (c Clock) MarshalJSON() ([]byte, error) {
	if len(c) == 0 {
		return []byte("null"), nil
	}
	return c, nil
}

// SystemClock is the system's clock, the default.
func SystemClock() Clock { return Clock(`"system"`) }

// StrictClock is the system's clock, never the same millisecond twice.
func StrictClock() Clock { return Clock(`"strict"`) }

// ManualClock stands at ms until a call's Now moves it.
func ManualClock(ms int64) Clock {
	return Clock(`{"manual":` + strconv.FormatInt(ms, 10) + `}`)
}

// concurrency is how many calls the binding makes at once.
func (c Config) concurrency() int {
	switch {
	case c.Concurrency > 0:
		return c.Concurrency
	case c.Postgres != nil && c.Postgres.PoolMax > 0:
		return c.Postgres.PoolMax
	case c.Postgres != nil:
		return 4
	default:
		return 8
	}
}
