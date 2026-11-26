// Package access is per-record access (D156) as the Go runtimes share it
// (D180): the principal a request runs as, whole, which the generated ORM
// holds every read and write of an operation to and the Go server's auth
// step names; the grants a party or an agent run reaches rows through,
// and their tokens; and the access log's entries.
//
// It is the Go side of the TypeScript runtime's Principal and AccessCaller
// (runtime/http/typescript/src/auth.ts, caller.ts) and of access.ts. The
// generated ORM of a database with an @audience table or an @accessGrants
// table imports it, and so does the HTTP runtime's session package, whose
// Caller is this package's Principal, so a generated API passes the
// principal its auth step established to the ORM with no conversion.
//
// Nothing here reads a database: the generated ORM reads and writes the
// @accessGrants and @accessLog tables, and implements GrantAuthority.
package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Kind is what a principal is (D156): a user, signed in by a session or
// an API key; a party, a row of an app table that holds a grant; an agent
// run; or a runtime provider calling as itself (D159). The empty kind
// reads as a user, every auth step's before D156.
type Kind string

// The kinds of principal.
const (
	KindUser      Kind = "user"
	KindParty     Kind = "party"
	KindAgent     Kind = "agent"
	KindExtension Kind = "extension"
)

// ActingFor is whom a principal acts for (D156): the user whose reply an
// agent run interprets, the party it answers. The empty kind reads as a
// user, as the principal's own does.
type ActingFor struct {
	Kind    Kind
	Subject string
}

// GrantReach is what one grant reaches (D156): its target row, by its
// table's type and its key, and the relation paths its role reaches other
// rows by, by table: {"Request": ["unit.contract"]} reaches a request whose
// unit's contract is the target, and "*" every row of a table. The target's
// own table reaches the target.
type GrantReach struct {
	ID         string
	TargetType string
	TargetID   string
	Reaches    map[string][]string
}

// The kinds of credential a call comes with, for the access log (D114,
// D156).
const (
	CredentialAPIKey = "apiKey"
	CredentialGrant  = "grant"
)

// Credential is the credential a call came with: an API key's or a
// grant's id. A session's call has none.
type Credential struct {
	Kind string
	ID   string
}

// Principal is the caller of a request, whole (D156, D180): its subject,
// a user's, a party's or an agent run's id; what it is; whom it acts for;
// the grants it calls through; and the credential it came with. The zero
// Principal is the anonymous caller, an operation with no principal, who
// sees only the rows everyone may.
type Principal struct {
	// Subject is the principal's stable id; empty for the anonymous caller.
	Subject string
	// Kind is what the principal is; empty reads as KindUser.
	Kind Kind
	// ActingFor is whom the principal acts for; nil for one that acts for
	// itself.
	ActingFor *ActingFor
	// Grants are the grants a party or an agent run calls through. A
	// principal whose Grants is not nil reaches, of a table, only the rows
	// one of them reaches; nil for a user, who reaches rows through roles
	// and row rules.
	Grants []GrantReach
	// Credential is the credential the call came with; nil for a session.
	Credential *Credential
}

// KindOrUser is the principal's kind, KindUser when it names none.
func (p Principal) KindOrUser() Kind {
	if p.Kind == "" {
		return KindUser
	}
	return p.Kind
}

// Anonymous reports whether p is the anonymous caller.
func (p Principal) Anonymous() bool { return p.Subject == "" }

// SecretMask is what a view shows of an Encrypted<T> field the caller may
// not reveal (D156), as the TypeScript runtime's SECRET_MASK.
const SecretMask = "[secret]"

// GrantTokenPrefix is the prefix of a grant's token, which tells a person
// and a secret scanner what it is.
const GrantTokenPrefix = "grant_"

// NewGrantToken is a grant's token (D156): GrantTokenPrefix and 43
// characters of base64url, 32 random bytes.
func NewGrantToken() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return GrantTokenPrefix + base64.RawURLEncoding.EncodeToString(random[:]), nil
}

// GrantTokenHash is the SHA-256 of a token's text, as a grant's tokenHash
// holds it: lowercase hex.
func GrantTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ReachesOf reads a role's reaches, "Type:path" each, into the paths a
// grant of the role reaches rows of each type by. An entry with no type or
// no path is skipped.
func ReachesOf(entries []string) map[string][]string {
	out := map[string][]string{}
	for _, entry := range entries {
		at := strings.IndexByte(entry, ':')
		if at <= 0 || at == len(entry)-1 {
			continue
		}
		out[entry[:at]] = append(out[entry[:at]], entry[at+1:])
	}
	return out
}

// Grant is a row of the @accessGrants table, as the reads of access read
// it (D156).
type Grant struct {
	ID            string
	Holder        string
	HolderKind    string
	Role          string
	TargetType    string
	TargetID      string
	StartsAt      time.Time
	EndsAt        *time.Time
	EndsWhenField string
	EndsWhenState string
	RevokedAt     *time.Time
}

// The reasons a grant is not active.
const (
	GrantRevoked    = "revoked"
	GrantNotStarted = "not started"
	GrantExpired    = "expired"
	GrantTargetGone = "target gone"
	GrantEndedState = "target ended"
)

// EndedByDate is why g is not active at now by its own stamps, or "" when
// they leave it active: revoked, not started or expired. Whether its
// target's state ended it is the database's to read.
func (g Grant) EndedByDate(now time.Time) string {
	switch {
	case g.RevokedAt != nil:
		return GrantRevoked
	case g.StartsAt.After(now):
		return GrantNotStarted
	case g.EndsAt != nil && !g.EndsAt.After(now):
		return GrantExpired
	}
	return ""
}

// HolderPrincipalKind is the kind of principal g's holder calls as: an
// agent run for an agent's grant, a party for every other.
func (g Grant) HolderPrincipalKind() Kind {
	if g.HolderKind == string(KindAgent) {
		return KindAgent
	}
	return KindParty
}

// IssueGrantInput is what a grant is made of (D156): its row, but its
// key, its token's hash and its stamps. StartsAt zero starts it now.
type IssueGrantInput struct {
	Holder string
	// HolderKind is party or agent; user for a calendar feed (D152), whose
	// token reads its feed alone.
	HolderKind    string
	Role          string
	TargetType    string
	TargetID      string
	StartsAt      time.Time
	EndsAt        *time.Time
	EndsWhenField string
	EndsWhenState string
}

// ActiveGrantsFilter is what a read of the active grants keeps: a
// holder's grants, a target's, or every one, at Now, the clock's when
// zero.
type ActiveGrantsFilter struct {
	Holder     string
	TargetType string
	TargetID   string
	Now        time.Time
}

// ErrGrantRefused is the error of a grant's token that is not a credential
// now: unknown, a calendar feed's, ended or revoked. The HTTP runtime
// answers it 401, with the message of the error that wraps it.
var ErrGrantRefused = errors.New("grant refused")

// GrantAuthentication is what a grant's token establishes: its holder as
// the principal of its kind, with the grant's reach, and the role's
// permissions, which the route's permission check matches.
type GrantAuthentication struct {
	Principal   Principal
	Role        string
	Permissions []string
}

// GrantAuthority authenticates a grant's token (D156): the generated ORM
// of a database with an @accessGrants table implements it, reading the
// grant, its target's state and its role on every call, so a grant that
// ended or was revoked is refused at the very next one. An error that
// wraps ErrGrantRefused is a refusal of the token.
type GrantAuthority interface {
	AuthenticateGrant(ctx context.Context, token string, now time.Time) (GrantAuthentication, error)
}

// The kinds of access-log entry (D114, D156).
const (
	LogReveal    = "reveal"
	LogAgentRead = "agentRead"
	LogKeyCall   = "keyCall"
	LogGrantCall = "grantCall"
)

// LogEntry is one entry of the access log: a reveal of an Encrypted<T>
// field, a read an agent run made, or a call made with an API key or a
// grant's token. An empty member is a null column.
type LogEntry struct {
	Kind          string
	Principal     string
	PrincipalKind string
	ActingFor     string
	Credential    string
	Operation     string
	TargetType    string
	TargetID      string
	Field         string
}

// Log is where the access log is written: the generated ORM's @accessLog
// table (AccessLogOf), or the project's own.
type Log interface {
	Record(ctx context.Context, entry LogEntry) error
}

// CallEntry is the access-log entry of a call (D156): an agent run's GET
// or HEAD, and any call with an API key or a grant's token; false for any
// other, and for the anonymous caller.
func CallEntry(p Principal, operation, method string) (LogEntry, bool) {
	if p.Anonymous() {
		return LogEntry{}, false
	}
	entry := LogEntry{Principal: p.Subject, PrincipalKind: string(p.KindOrUser()), Operation: operation}
	if p.ActingFor != nil {
		entry.ActingFor = p.ActingFor.Subject
	}
	if p.Credential != nil {
		entry.Credential = p.Credential.ID
	}
	switch {
	case p.Kind == KindAgent && (method == "GET" || method == "HEAD"):
		entry.Kind = LogAgentRead
	case p.Credential != nil && p.Credential.Kind == CredentialAPIKey:
		entry.Kind = LogKeyCall
	case p.Credential != nil && p.Credential.Kind == CredentialGrant:
		entry.Kind = LogGrantCall
	default:
		return LogEntry{}, false
	}
	return entry, true
}

// RevealEntry is the access-log entry of one value an operation opened
// under a reveal permission (D156): the caller, the operation, the row by
// its table's type and key, and the field.
func RevealEntry(p Principal, operation, targetType, targetID, field string) LogEntry {
	entry := LogEntry{Kind: LogReveal, Operation: operation, TargetType: targetType, TargetID: targetID, Field: field}
	if !p.Anonymous() {
		entry.Principal = p.Subject
		entry.PrincipalKind = string(p.KindOrUser())
	}
	if p.ActingFor != nil {
		entry.ActingFor = p.ActingFor.Subject
	}
	if p.Credential != nil {
		entry.Credential = p.Credential.ID
	}
	return entry
}
