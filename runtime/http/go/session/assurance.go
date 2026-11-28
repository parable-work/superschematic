package session

import (
	"context"
	"time"
)

// Assurance is how the auth step verified the caller for one call (D200):
// when its session signed in, and whether the call carried a passkey
// assertion with user verification over a challenge bound to it. An
// Approvals requirement's proof reads it, recent-sign-in the one and
// user-verification the other; a caller with none proves nothing.
type Assurance struct {
	// SignedInAt is when the session signed in; nil for a caller that
	// signed in through no session, an API key's or a grant's.
	SignedInAt *time.Time
	// UserVerified is true for a call a passkey assertion with the UV flag
	// verified.
	UserVerified bool
}

type assuranceKey struct{}

type agentKey struct{}

// ContextWithAssurance returns ctx carrying the assurance the auth step
// established for the call (D200).
func ContextWithAssurance(ctx context.Context, assurance Assurance) context.Context {
	return context.WithValue(ctx, assuranceKey{}, assurance)
}

// AssuranceOf is the assurance the auth step put on ctx, and false when it
// put none.
func AssuranceOf(ctx context.Context) (Assurance, bool) {
	assurance, ok := ctx.Value(assuranceKey{}).(Assurance)
	return assurance, ok
}

// ContextWithAgent returns ctx naming the Agent whose run the caller is
// (D158, D200): the run's agent claim, which the agent dispatcher's
// authenticator establishes.
func ContextWithAgent(ctx context.Context, agent string) context.Context {
	return context.WithValue(ctx, agentKey{}, agent)
}

// AgentOf is the Agent whose run the caller of ctx is; "" for any other
// caller, and for a caller ContextWithCaller put after the Agent's run,
// whose subject is another.
func AgentOf(ctx context.Context) string {
	agent, _ := ctx.Value(agentKey{}).(string)
	if agent == "" || GetCaller(ctx).Kind != "agent" {
		return ""
	}
	return agent
}
