package sdk

import (
	"example.com/schemas/sdk/go/fixture-pool-api/namespaces"
	"fmt"
)

// FixturePoolApiSDK is the root client for fixture-pool-api endpoints.
type FixturePoolApiSDK struct {
	client *HTTPClient
	// AccountNamespace exposes account namespace operations.
	//
	// Endpoints:
	// - Pool.Pick: Picks a member to run work on, a read of the table as a whole that writes nothing: of the members match keeps, leaving out those exclude names, the first by order of the open members prefer keeps, then of its near ones, then of the other open and near ones. A member counts as out while its table's Capacity has no room for it. With none it answers member null and until, the soonest time a member match keeps may be back, so a caller waits on a deadline of its own. It reads at most 2000 members. Requires authentication.
	// - Pool.MarkOut: Puts the member out until until, or outForMs from now when it names none, for reason: what a caller a provider refused tells the pool. An out member keeps the later of its outUntil and the new one and takes the new reason, and a call that moves neither is answered unchanged. Arms the member's recheck at its outUntil. Needs permission. Requires authentication.
	// - Pool.ReportRoom: Records a check of the member at its provider, made now. With room it opens an out member whose outUntil has come, near when near says so, and keeps an open or near one open or near as near says; a check with room before an out member's outUntil records checkedAt alone. With no room the member is out until the later of its outUntil and resetsAt, or outForMs from now when the check names no time, and its recheck is armed then, or recheckAfterMs from now when that is later. Needs permission. Requires authentication.
	// - Budget.CheckReserve: Whether reserve with the same parameters would fit now, here and in every enclosing scope, after the reservations of a lease that is no longer active are settled, as reserve settles them; it changes nothing and does not ask for a lease. Queue copies its answer so that a claim that cannot fit is not tried. Requires authentication.
	// - Budget.Observe: Records the usage of a meter a provider reports, in place of what the meter counts until resetsAt (until the meter's next reset without it, and until the next observe for a meter with no reset): the meter's used is the figure given, usage recorded after it adds to it, and limit, when given, is the meter's limit while it holds. Nothing passes up to an enclosing scope. Needs observePermission. A table's alone: an engine type refuses it. Requires authentication.
	// - Budget.RecordUsage: Records usage of a meter, which is never refused. The part of it the instance's own reservation covers is released, here and in every enclosing scope, and the usage counts in each of them. Reports every instance of the chain whose usage is over its limit after it, moves the status of each whose config gives escalate, and sends onExceeded's directive to the holder of the active lease once per meter per lease. Requires authentication.
	// - Budget.RecordUsageFor: Counts usage of a meter by an instance whose scope link points here, releases the part of what this instance holds for it that its own reservation covered, never more than that one no longer holds, and passes both on up this instance's own scope. Never refused for the amount. Requires authentication.
	// - Budget.Reserve: Reserves an amount of one meter, or of every meter the config gives a reservation, for the instance and every enclosing scope. Refused when the instance or an enclosing scope cannot fit it, and then nothing is reserved anywhere. On a type that composes Lease it needs an active lease, and the reservation is settled when that lease ends. Requires authentication.
	// - Budget.ReserveFor: Holds an amount of a meter for an instance whose scope link points here, as that instance's reserve does, and passes it on up this instance's own scope. Refused when the amount does not fit, when that instance does not draw the meter from here, and beyond what it has reserved and this instance does not hold for it yet. Requires authentication.
	// - Budget.SetLimit: Sets a meter's limit on the instance, raising or lowering it; through limitField when the meter has one. Needs limitPermission, and is refused when the config names none, and below what the instance has used and reserved. Requires authentication.
	// - Budget.Settle: Releases the instance's own reservation of one meter, or of every meter, here and in every enclosing scope. Requires authentication.
	// - Budget.SettleFor: Releases up to an amount of what this instance holds of a meter for another, never more than that one no longer holds itself, and passes the release on up this instance's own scope. Requires authentication.
	AccountNamespace *namespaces.AccountNamespace
}

// New creates a new FixturePoolApiSDK.
func New(config SDKConfig) (*FixturePoolApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixturePoolApiSDK{
		client:           client,
		AccountNamespace: namespaces.NewAccountNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixturePoolApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixturePoolApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixturePoolApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
