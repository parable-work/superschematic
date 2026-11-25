package ir

import "sort"

// Idempotency keys (D140). An operation declared @idempotent answers a
// repeated call what the first one answered: a caller sends
// IdempotencyKeyHeader, and the server keeps a row per operation,
// principal and key in the table the API's database declares with one
// reserved class, `export abstract class IdempotencyKey implements
// IdempotencyKeys {}`, whose columns the loader adds.

// IdempotentConfig is an operation's @idempotent({ ttl?, required? }).
type IdempotentConfig struct {
	// TTLSeconds is how long a key's answer is kept, in seconds; 0 is
	// DefaultIdempotencyTTLSeconds.
	TTLSeconds int `json:"ttlSeconds,omitempty" yaml:"ttlSeconds,omitempty"`

	// Required refuses a call without the header with 400. Without it such
	// a call runs as an operation that is not @idempotent runs.
	Required bool `json:"required,omitempty" yaml:"required,omitempty"`
}

// TTL is how long a key's answer is kept, in seconds.
func (c *IdempotentConfig) TTL() int {
	if c == nil || c.TTLSeconds <= 0 {
		return DefaultIdempotencyTTLSeconds
	}
	return c.TTLSeconds
}

const (
	// DefaultIdempotencyTTLSeconds is an @idempotent's ttl when it names
	// none: 24 hours.
	DefaultIdempotencyTTLSeconds = 24 * 60 * 60

	// MaxIdempotencyTTLSeconds is the longest ttl an @idempotent names:
	// 90 days.
	MaxIdempotencyTTLSeconds = 90 * 24 * 60 * 60

	// IdempotencyKeyHeader is the request header that carries the key.
	IdempotencyKeyHeader = "Idempotency-Key"

	// IdempotencyKeyArgument is the tool argument that carries the key on
	// the MCP endpoint, where a call has no headers, and the name of the
	// option every SDK's method takes.
	IdempotencyKeyArgument = "idempotencyKey"

	// IdempotencyKeysTraitName is the reserved trait of the class that
	// declares the table, as @superschematic/db exports it.
	IdempotencyKeysTraitName = "IdempotencyKeys"

	// OriginIdempotency marks the fields the loader adds to the reserved
	// class.
	OriginIdempotency = "idempotency"
)

// The fields the loader adds to the reserved class. A row is one key of
// one principal on one operation.
const (
	// IdempotencyLookupField names the row: the SHA-256 of the operation,
	// the principal and the key. It is unique.
	IdempotencyLookupField = "lookup"
	// IdempotencyOperationField, IdempotencyPrincipalField and
	// IdempotencyKeyField are what the lookup hashes, kept to read.
	IdempotencyOperationField = "operation"
	IdempotencyPrincipalField = "principal"
	IdempotencyKeyField       = "key"
	// IdempotencyRequestHashField is the SHA-256 of the request's method,
	// path and body.
	IdempotencyRequestHashField = "requestHash"
	// IdempotencyStateField is IdempotencyStateRunning or
	// IdempotencyStateDone.
	IdempotencyStateField = "state"
	// IdempotencyStatusField and IdempotencyBodyField are the stored
	// answer's status and body, null while the first request runs.
	IdempotencyStatusField = "status"
	IdempotencyBodyField   = "body"
	// IdempotencyStartedAtField and IdempotencyExpiresAtField are when the
	// attempt that holds the row started and when the row expires, in
	// milliseconds since the epoch.
	IdempotencyStartedAtField = "startedAt"
	IdempotencyExpiresAtField = "expiresAt"

	IdempotencyStateRunning = "running"
	IdempotencyStateDone    = "done"
)

// IdempotencyKeysTrait marks the DB table that keeps idempotency keys:
// `implements IdempotencyKeys`. It takes no config.
type IdempotencyKeysTrait struct{}

// IdempotencyKeysTable returns the schema's table that keeps idempotency
// keys, the one whose class implements IdempotencyKeys, or nil when it
// declares none. A schema has at most one; of several, which verification
// refuses, it is the first by name.
func (s *Schema) IdempotencyKeysTable() *TypeDef {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.Types))
	for name := range s.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if td := s.Types[name]; td != nil && td.IdempotencyKeys != nil {
			return td
		}
	}
	return nil
}

// IdempotentOperations returns the schema's @idempotent operations, each
// as `<Set>.<operation>`, in the sets' order.
func (s *Schema) IdempotentOperations() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, set := range s.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op != nil && op.Idempotent != nil {
				out = append(out, set.Name+"."+op.Name)
			}
		}
	}
	return out
}

// IdempotencyTable names the reserved table and its columns as the SQL
// generator writes them, which the build reads from the DB schema and
// hands each server's store (D140).
type IdempotencyTable struct {
	Table       string `json:"table"`
	Lookup      string `json:"lookup"`
	Operation   string `json:"operation"`
	Principal   string `json:"principal"`
	Key         string `json:"key"`
	RequestHash string `json:"requestHash"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Body        string `json:"body"`
	StartedAt   string `json:"startedAt"`
	ExpiresAt   string `json:"expiresAt"`
}
