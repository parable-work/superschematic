package notify

import (
	"context"

	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
)

// AgentRequest is the run request a route's { agent } recipient makes
// (D158): the agent by name, the task the route gives it, the event's
// record, the event, and the route that asked.
type AgentRequest struct {
	Agent        string
	Task         string
	SubjectTable string
	SubjectKey   string
	EventID      string
	EventKind    string
	Payload      map[string]any
	Route        string
}

// Agents is the agent dispatcher a route's { agent } recipient reaches
// (D158, D192): the HTTP runtime's agents package, whose Request puts the
// run request on its queue in h, the routing transaction, so the run is
// asked for once the routing commits or not at all. A notifier without
// one logs the route and reaches no one.
type Agents interface {
	RouteRequest(ctx context.Context, h notifications.Host, request AgentRequest) error
}

// lapsed reports whether identity, a row of the channel identities read
// through h, is a party's that lapsed with its grants (D157): a database
// with parties answers it through its orchestration host, and one without
// has no party to lapse.
func (n *Notifier) lapsed(ctx context.Context, h notifications.Host, identity ChannelIdentity) (bool, error) {
	if identity.PrincipalKind != "party" {
		return false, nil
	}
	host, err := orchestration.Of(h)
	if err != nil {
		return false, nil
	}
	return orchestration.IdentityLapsed(ctx, host, orchestration.IdentityStanding{PrincipalKind: identity.PrincipalKind, Principal: identity.Principal, VerifiedAt: identity.VerifiedAt, CreatedAt: identity.CreatedAt}, n.now())
}

// requestAgents puts the run request of each { agent } recipient of a
// route on the dispatcher's queue in h (D158).
func (n *Notifier) requestAgents(ctx context.Context, h notifications.Host, event raisedEvent, route string, specs []recipientSpec) error {
	for _, spec := range specs {
		if spec.Agent == nil {
			continue
		}
		if n.opts.Agents == nil {
			n.log.Warn("a route names an agent, and the notifier has no agent dispatcher")
			continue
		}
		if err := n.opts.Agents.RouteRequest(ctx, h, AgentRequest{
			Agent:        *spec.Agent,
			Task:         spec.Task,
			SubjectTable: event.Table,
			SubjectKey:   event.Key,
			EventID:      event.ID,
			EventKind:    event.Kind,
			Payload:      event.Payload,
			Route:        route,
		}); err != nil {
			return err
		}
	}
	return nil
}
