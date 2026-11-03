package sdk

import (
	"example.com/schemas/sdk/go/fixture-timers-api/namespaces"
	"fmt"
)

// FixtureTimersApiSDK is the root client for fixture-timers-api endpoints.
type FixtureTimersApiSDK struct {
	client *HTTPClient
	// TicketNamespace exposes ticket namespace operations.
	//
	// Endpoints:
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	// - Timers.FireTimer: Fires a deadline that is due and has not fired: marks it fired and moves the status by its then, recording the outcome; a veto or a missing permission the move meets leaves the status as it is and the deadline fired. A deadline whose until holds is cancelled instead. On a deadline not due, fired already or not started it changes nothing. Any principal who may write the instance may call it; the engine's runner calls it on every due deadline, every sweepMs. Requires authentication.
	// - Timers.ResetTimer: Starts a deadline again from what it starts from now, the create, the status's entry into its state or its field's value, so it is due afterMs after it and not fired: a snooze or a fire is undone. Needs the config's permission; refused for a deadline with nothing to start from now, or whose until holds. Requires authentication.
	// - Timers.SnoozeTimer: Moves a started deadline's due time to dueAt, and a fired one fires again then. A later write keeps it, until what the deadline starts from changes. Needs the config's permission; refused for a deadline not started, cancelled, or whose until holds. Requires authentication.
	TicketNamespace *namespaces.TicketNamespace
}

// New creates a new FixtureTimersApiSDK.
func New(config SDKConfig) (*FixtureTimersApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureTimersApiSDK{
		client:          client,
		TicketNamespace: namespaces.NewTicketNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureTimersApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureTimersApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureTimersApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
