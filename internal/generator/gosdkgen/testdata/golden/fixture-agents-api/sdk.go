package sdk

import (
	"example.com/schemas/sdk/go/fixture-agents-api/namespaces"
	"fmt"
)

// FixtureAgentsApiSDK is the root client for fixture-agents-api endpoints.
type FixtureAgentsApiSDK struct {
	client *HTTPClient
	// AccountNamespace exposes account namespace operations.
	//
	// Endpoints:
	// - Budget.CheckReserve: Whether reserve with the same parameters would fit now, here and in every enclosing scope, after the reservations of a lease that is no longer active are settled, as reserve settles them; it changes nothing and does not ask for a lease. Queue copies its answer so that a claim that cannot fit is not tried. Requires authentication.
	// - Budget.Observe: Records the usage of a meter a provider reports, in place of what the meter counts until resetsAt (until the meter's next reset without it, and until the next observe for a meter with no reset): the meter's used is the figure given, usage recorded after it adds to it, and limit, when given, is the meter's limit while it holds. Nothing passes up to an enclosing scope. Needs observePermission. A table's alone: an engine type refuses it. Requires authentication.
	// - Budget.RecordUsage: Records usage of a meter, which is never refused. The part of it the instance's own reservation covers is released, here and in every enclosing scope, and the usage counts in each of them. Reports every instance of the chain whose usage is over its limit after it, moves the status of each whose config gives escalate, and sends onExceeded's directive to the holder of the active lease once per meter per lease. Requires authentication.
	// - Budget.RecordUsageFor: Counts usage of a meter by an instance whose scope link points here, releases the part of what this instance holds for it that its own reservation covered, never more than that one no longer holds, and passes both on up this instance's own scope. Never refused for the amount. Requires authentication.
	// - Budget.Reserve: Reserves an amount of one meter, or of every meter the config gives a reservation, for the instance and every enclosing scope. Refused when the instance or an enclosing scope cannot fit it, and then nothing is reserved anywhere. On a type that composes Lease it needs an active lease, and the reservation is settled when that lease ends. Requires authentication.
	// - Budget.ReserveFor: Holds an amount of a meter for an instance whose scope link points here, as that instance's reserve does, and passes it on up this instance's own scope. Refused when the amount does not fit, when that instance does not draw the meter from here, and beyond what it has reserved and this instance does not hold for it yet. Requires authentication.
	// - Budget.SetLimit: Sets a meter's limit on the instance, raising or lowering it; through limitField when the meter has one. Needs limitPermission, and is refused when the config names none, and below what the instance has used and reserved. Requires authentication.
	// - Budget.Settle: Releases the instance's own reservation of one meter, or of every meter, here and in every enclosing scope. Requires authentication.
	// - Budget.SettleFor: Releases up to an amount of what this instance holds of a meter for another, never more than that one no longer holds itself, and passes the release on up this instance's own scope. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	AccountNamespace *namespaces.AccountNamespace
	// JobNamespace exposes job namespace operations.
	//
	// Endpoints:
	// - Lease.Acknowledge: Marks directives of the lease as handled, so heartbeats stop delivering them. Only the holder, presenting the current token as Lease's precondition. Requires authentication.
	// - Lease.Acquire: Takes the lease for the caller and returns its fencing token. Refused while a lease is active, the caller's own included (heartbeat renews it), without acquirePermission when the config names one, and once the instance has had maxExpiries expiries. Over a lease that expired, it applies that expiry first. Requires authentication.
	// - Lease.Direct: Sends the holder of the active lease a directive, which its heartbeats deliver until it acknowledges it. With dedupeKey, a directive already sent under the lease with the key is not sent again. A principal needs directPermission, or overridePermission when that is absent, and is refused when the config names neither; another behavior of the type sends one with call(), as the principal it runs for, without either. Requires authentication.
	// - Lease.Expire: Applies the expiry of a lease past its expiry time or its longest hold: the holder is cleared, the token advances, the expiry is counted unless the instance is in a terminal state of its Workflow, and onExpiry or escalate moves the status. Any principal who may write the instance may call it; on any other instance it changes nothing. With holder, a principal with overridePermission expires that holder's lease at once, active or not, unless notRenewedAfter spares it. The engine's runner calls it on every lapsed lease, every sweepMs. Requires authentication.
	// - Lease.Heartbeat: Renews the lease, never past its longest hold, acknowledges the directives acknowledge names, and returns the directives sent under it that are not acknowledged. Only the holder, presenting the current token as Lease's precondition, while the lease is active: an expired lease cannot be renewed. Requires authentication.
	// - Lease.Release: Ends an active lease: the holder, presenting the current token as Lease's precondition, or a principal with overridePermission, who need not. The token advances, so the directives sent under the lease are not delivered again, and onExpiry moves the status when it is still one of its from states. With abandon, the holder gives the work up as failed: the release counts as an expiry, and escalate applies at the one that reaches maxExpiries. Requires authentication.
	// - Lease.ResetExpiries: Sets the instance's count of expiries back to 0, so a lease can be acquired again after maxExpiries. Needs overridePermission; refused when the config names none. Requires authentication.
	// - Retries.RecordAttempt: Records an attempt: a success without failure, or a failure of one of the config's classes, counted against its caps, and returns the class's hint. Decides whether its result is kept, and writes a kept result to resultField. Exhausts the instance at a terminal class, a failure past a cap or a stuck signature, moving its status to exhaustedState from the from states. Refused once the instance is exhausted, and without the config's permission. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	JobNamespace *namespaces.JobNamespace
	// SessionNamespace exposes session namespace operations.
	//
	// Endpoints:
	// - Presence.Beat: Moves the deadline to ttlMs from now and clears missed, and onBeat moves the status when it is one of its from states. Only the principal whose subject the instance's principalField holds. Requires authentication.
	// - Presence.Miss: Applies the miss of an instance past its deadline and not missed yet: sets missed, moves the status with onMissed when it is one of its from states, and expires the principal's leases on the releaseLeases schemas that were not renewed after its last beat, recording which in released. Any principal who may write the instance may call it; on any other instance it changes nothing. The engine's runner calls it every sweepMs. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	SessionNamespace *namespaces.SessionNamespace
}

// New creates a new FixtureAgentsApiSDK.
func New(config SDKConfig) (*FixtureAgentsApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureAgentsApiSDK{
		client:           client,
		AccountNamespace: namespaces.NewAccountNamespace(client),
		JobNamespace:     namespaces.NewJobNamespace(client),
		SessionNamespace: namespaces.NewSessionNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureAgentsApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureAgentsApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureAgentsApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
