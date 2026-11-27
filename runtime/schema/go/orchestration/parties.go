package orchestration

import (
	"context"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

/*
Parties (D157): the principal a @party row is, and whether a party's
channel identity still stands. TypeScript's parties.ts.

A party is a row of a @party table, a principal of kind party (D156) that
acts only through the grants it holds: the rows of the @accessGrants table
whose holder is its key. PartyPrincipal resolves it the way a grant's token
resolves, with every active grant's reach (or one grant's), and the
permissions of those grants' roles.

An identity whose party's grants have all ended since it was verified has
lapsed: a phone number is reassigned, an address changes hands, and a
message from it is no longer the party's until it is verified again. It is
decided from the rows, with no stamp to keep: the party holds no active
grant, and some grant of it did not end, by its date or its revocation,
before the identity was verified. A grant that ended by its target's state
counts as ended after, since the row does not say when.
*/

// PartyRef is a party: its table, by its type's name, and its row's key.
type PartyRef struct {
	Table string `json:"table"`
	Key   string `json:"key"`
}

// Principal is a principal with the permissions it holds, which Go's
// access.Principal does not carry (D187's crosstable.Caller is the same).
type Principal struct {
	access.Principal
	Permissions []string
}

// PartyPrincipal is the principal party is (D157): kind party, its subject
// the row's key, the reach of each of its active grants at now (or of the
// one grant named, when it is active), and the permissions of those
// grants' roles. A party with no active grant is a principal that reaches
// nothing.
func PartyPrincipal(ctx context.Context, h Host, party PartyRef, grant string, now time.Time) (Principal, error) {
	out := Principal{Principal: access.Principal{Subject: party.Key, Kind: access.KindParty, Grants: []access.GrantReach{}}, Permissions: []string{}}
	ctx = h.System(ctx)
	grants, err := h.Grants(ctx, party.Key, true, now)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, g := range grants {
		if grant != "" && !SameUUID(g.ID, grant) {
			continue
		}
		role, _, err := h.Role(ctx, g.Role)
		if err != nil {
			return out, err
		}
		for _, permission := range role.Permissions {
			if !seen[permission] {
				seen[permission] = true
				out.Permissions = append(out.Permissions, permission)
			}
		}
		out.Grants = append(out.Grants, access.GrantReach{ID: g.ID, TargetType: g.TargetType, TargetID: g.TargetID, Reaches: access.ReachesOf(role.Reaches)})
	}
	return out, nil
}

// IdentityStanding is a channel identity as a lapse reads it.
type IdentityStanding struct {
	PrincipalKind string
	Principal     string
	VerifiedAt    *time.Time
	CreatedAt     *time.Time
}

// IdentityLapsed reports whether identity has lapsed at now (D157): it is
// a party's, the party holds no active grant, and some grant of the party
// did not end before the identity was verified. A user's identity never
// lapses, and neither does a party's that has held no grant since it was
// verified.
func IdentityLapsed(ctx context.Context, h Host, identity IdentityStanding, now time.Time) (bool, error) {
	if identity.PrincipalKind != string(access.KindParty) {
		return false, nil
	}
	ctx = h.System(ctx)
	grants, err := h.Grants(ctx, identity.Principal, false, now)
	if err != nil || len(grants) == 0 {
		return false, err
	}
	var verified time.Time
	switch {
	case identity.VerifiedAt != nil:
		verified = *identity.VerifiedAt
	case identity.CreatedAt != nil:
		verified = *identity.CreatedAt
	}
	endedSince := false
	for _, grant := range grants {
		why, err := h.GrantEnded(ctx, grant, now)
		if err != nil {
			return false, err
		}
		if why == "" {
			// An active grant: the identity stands.
			return false, nil
		}
		if grant.StartsAt.After(now) {
			continue
		}
		end := time.Time{}
		for _, at := range []*time.Time{grant.EndsAt, grant.RevokedAt} {
			if at != nil && !at.After(now) && (end.IsZero() || at.Before(end)) {
				end = *at
			}
		}
		if end.IsZero() || end.After(verified) {
			endedSince = true
		}
	}
	return endedSince, nil
}

// SameUUID reports whether a and b are the same id, whatever form each
// UUID came in; any other text is compared as it is.
func SameUUID(a, b string) bool {
	x := rowrefs.UUIDToDB(a)
	if x != "" {
		return x == rowrefs.UUIDToDB(b)
	}
	return a == b
}
