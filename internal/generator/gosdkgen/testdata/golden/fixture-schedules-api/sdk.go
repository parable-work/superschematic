package sdk

import (
	"example.com/schemas/sdk/go/fixture-schedules-api/namespaces"
	"fmt"
)

// FixtureSchedulesApiSDK is the root client for fixture-schedules-api endpoints.
type FixtureSchedulesApiSDK struct {
	client *HTTPClient
	// ProblemNamespace exposes problem namespace operations.
	//
	// Endpoints:
	// - Schedules.CancelSchedule: Finishes a live schedule, clearing its deadline, so it no longer counts toward max; it stays, finished, for its latest run's report and a read. A finished one is answered unchanged. Requires authentication.
	// - Schedules.ListSchedules: Lists the row's schedules by name in code point order, live and finished; with name, only that one. Requires authentication.
	// - Schedules.PauseSchedule: Pauses an active schedule: it keeps its next run and the retry of its latest one, and its deadline is cleared until it resumes. A paused one is answered unchanged. Requires authentication.
	// - Schedules.ReportRun: Records the outcome of a schedule's latest run, one of the config's outcomes. The config's retry outcome tries the run again afterMs on, as its next attempt, unless the next regular run comes first; another outcome cancels a retry armed for it. A report of an older run, of a replaced schedule's latest run, or of an attempt other than the latest, or a repeat of the outcome recorded, is answered unchanged. Requires authentication.
	// - Schedules.ResumeSchedule: Resumes a paused schedule, arming its next run, or the retry of its latest, again: one past its time falls due at once and fires once. An active one is answered unchanged; a row in a state endOn names resumes none. Requires authentication.
	// - Schedules.SetSchedule: Sets a schedule of the row by its name: exactly one of after, every and cron, of a kind the config's kinds take, within minMs and maxMs, a cron line read in timeZone. A live schedule of the name is replaced and its runs start again; one with the same kind, spec and time zone is answered unchanged. A new name needs room under max, and a row in a state endOn names takes none. Requires authentication.
	// - Status.Transition: Moves the instance to another state along a transition the config lists. A transition that names a permission needs a caller who holds it. Requires authentication.
	ProblemNamespace *namespaces.ProblemNamespace
}

// New creates a new FixtureSchedulesApiSDK.
func New(config SDKConfig) (*FixtureSchedulesApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureSchedulesApiSDK{
		client:           client,
		ProblemNamespace: namespaces.NewProblemNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureSchedulesApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureSchedulesApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureSchedulesApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
