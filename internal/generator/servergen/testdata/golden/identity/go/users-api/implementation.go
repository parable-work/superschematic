// Package usersapi implements the users-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it. Each job fails
// until you implement it.
package usersapi

import (
	"context"

	api "example.com/schemas/api/users-api"
	types "example.com/schemas/types/go/users-api"
)

// New builds the implementation of users-api from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Greeting: &Greeting{deps: deps},
	}, nil
}

var _ api.Constructor = New

// NewJobs builds the jobs of users-api from the API's dependencies. Its
// signature is the generated one, api.JobsConstructor.
func NewJobs(deps api.Deps) (api.Jobs, error) {
	return &jobs{deps: deps}, nil
}

var _ api.JobsConstructor = NewJobs

// jobs implements api.Jobs.
type jobs struct {
	deps api.Deps
}

// PurgeSessions runs the job PurgeSessions.
func (j *jobs) PurgeSessions(ctx context.Context) error {
	return api.NotImplementedError("job PurgeSessions")
}

// Greeting implements api.GreetingImplementation.
type Greeting struct {
	deps api.Deps
}

// Greet handles GET /api/greeting.
func (impl *Greeting) Greet(ctx context.Context) (*types.Greeting, error) {
	return nil, api.NotImplementedError("Greeting.Greet")
}
