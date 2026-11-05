package sdk

import (
	"example.com/schemas/sdk/go/fixture-seats-api/namespaces"
	"fmt"
)

// FixtureSeatsApiSDK is the root client for fixture-seats-api endpoints.
type FixtureSeatsApiSDK struct {
	client *HTTPClient
	// ProblemNamespace exposes problem namespace operations.
	//
	// Endpoints:
	// - Seats.LeaveSeat: Unseats a subject from a seat: the caller, which needs no permission, or another, with the seat's assign permission. The subject the seat's fromField holds cannot leave while it holds them. A subject not seated is answered unchanged. Requires authentication.
	// - Seats.ListSeats: Lists the subjects seated on the row, by seat in code point order, each seat's fromField subject first and then the others in the order they were seated; with seat, only its subjects. Requires authentication.
	// - Seats.TakeSeat: Seats a subject in a seat: the caller, with the seat's take permission when it names one, or another, with its assign permission. A full seat refuses it, or unseats the subject seated longest when its onFull is displace. A subject seated already is answered unchanged. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	ProblemNamespace *namespaces.ProblemNamespace
	// SandboxNamespace exposes sandbox namespace operations.
	//
	// Endpoints:
	// - Observed.Observe: Records the state the provider reports, when at is newer than the last observation's, and moves the Workflow there along the fewest transitions its config allows, each as the caller, so every guard and permission holds each step; a step refused stops there and records drift. An observation no newer is answered unchanged. Needs observePermission. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	SandboxNamespace *namespaces.SandboxNamespace
}

// New creates a new FixtureSeatsApiSDK.
func New(config SDKConfig) (*FixtureSeatsApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureSeatsApiSDK{
		client:           client,
		ProblemNamespace: namespaces.NewProblemNamespace(client),
		SandboxNamespace: namespaces.NewSandboxNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureSeatsApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureSeatsApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureSeatsApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
