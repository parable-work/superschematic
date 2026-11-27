package agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/inbound"
	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
)

// RunPrincipal is the principal a run's token resolves to (D158): kind
// agent, its subject the run, acting for whom the run acts for, its
// permissions the agent's role's narrowed to those that principal holds,
// its grants the run's, narrowed to a party's paths (D183), and the claims
// TypeScript's principal carries: the run, the agent, the definition's
// version, and the trust of an inbound message's run.
type RunPrincipal struct {
	orchestration.Principal
	Run               string
	Agent             string
	DefinitionVersion int64
	Trust             string
}

// roleReaches are the paths the role named name reaches, by type.
func (a *Agents) roleReaches(ctx context.Context, name string) (map[string][]string, error) {
	role, _, err := a.host.Role(ctx, name)
	if err != nil {
		return nil, err
	}
	return access.ReachesOf(role.Reaches), nil
}

// permissionsOf are the permissions whom a run acts for holds: a user's
// roles', a party's grants' roles' (or the one grant the run is under),
// any other holder's grants' roles'.
func (a *Agents) permissionsOf(ctx context.Context, kind, subject, table, grant string) ([]string, error) {
	switch {
	case kind == string(access.KindUser):
		return a.host.Permissions(a.host.System(ctx), subject)
	case table != "":
		// A party is what D157's PartyPrincipal says it is: its active
		// grants' roles, or the one grant the run is under.
		party, err := orchestration.PartyPrincipal(ctx, a.host, orchestration.PartyRef{Table: table, Key: subject}, grant, a.now())
		return party.Permissions, err
	}
	grants, err := a.host.Grants(ctx, subject, true, a.now())
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, g := range grants {
		role, _, err := a.host.Role(ctx, g.Role)
		if err != nil {
			return nil, err
		}
		for _, p := range role.Permissions {
			if !contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// PrincipalOf is the principal a run's token resolves to; nil for a token
// that is no running or waiting run's.
func (a *Agents) PrincipalOf(ctx context.Context, token string) (*RunPrincipal, error) {
	if !strings.HasPrefix(token, access.GrantTokenPrefix) {
		return nil, nil
	}
	base, err := a.host.AuthenticateGrant(ctx, token, a.now())
	if err != nil {
		// An unknown, revoked or ended grant: no running run holds it.
		if errors.Is(err, access.ErrGrantRefused) {
			return nil, nil
		}
		return nil, err
	}
	if base.Principal.Kind != access.KindAgent {
		return nil, nil
	}
	run, err := a.run(ctx, a.host, base.Principal.Subject)
	if err != nil || run == nil {
		return nil, err
	}
	if status := run.Str("status"); status != "running" && status != "waiting" {
		return nil, nil
	}
	input := run.Object("input")
	grantOfRun, _ := input["grant"].(string)
	var acting *access.ActingFor
	if run.Str("actingForKind") != "" && run.Str("actingFor") != "" {
		acting = &access.ActingFor{Kind: access.Kind(run.Str("actingForKind")), Subject: run.Str("actingFor")}
	}
	sys := a.host.System(ctx)
	// A run acting for a party reaches through no path the party's grant
	// does not (D183): the grant it runs under, or each active grant of the
	// party, read on every call, so a grant that ended narrows it to
	// nothing.
	type partyReach struct {
		targetType, targetID string
		reaches              map[string][]string
	}
	var party []partyReach
	narrowing := acting != nil && acting.Kind == access.KindParty
	if narrowing {
		grants, err := a.host.Grants(sys, acting.Subject, true, a.now())
		if err != nil {
			return nil, err
		}
		for _, g := range grants {
			if grantOfRun != "" && !orchestration.SameUUID(g.ID, grantOfRun) {
				continue
			}
			reaches, err := a.roleReaches(sys, g.Role)
			if err != nil {
				return nil, err
			}
			party = append(party, partyReach{targetType: g.TargetType, targetID: g.TargetID, reaches: reaches})
		}
	}
	own, err := a.host.Grants(sys, base.Principal.Subject, true, a.now())
	if err != nil {
		return nil, err
	}
	// The run's grants were issued one per target, in the targets' order.
	reaches := []access.GrantReach{}
	order := targetsOf(run)
	position := func(g access.Grant) int {
		for i, t := range order {
			if t.Table == g.TargetType && orchestration.SameUUID(t.Key, g.TargetID) {
				return i
			}
		}
		return len(order)
	}
	sortGrants(own, position)
	for _, g := range own {
		mine, err := a.roleReaches(sys, g.Role)
		if err != nil {
			return nil, err
		}
		if narrowing {
			var of []map[string][]string
			for _, p := range party {
				if p.targetType == g.TargetType && orchestration.SameUUID(p.targetID, g.TargetID) {
					of = append(of, p.reaches)
				}
			}
			mine = NarrowReaches(mine, of)
		}
		reaches = append(reaches, access.GrantReach{ID: g.ID, TargetType: g.TargetType, TargetID: g.TargetID, Reaches: mine})
	}
	permissions := append([]string{}, base.Permissions...)
	if acting != nil {
		held, err := a.permissionsOf(ctx, string(acting.Kind), acting.Subject, run.Str("actingForTable"), grantOfRun)
		if err != nil {
			return nil, err
		}
		permissions = NarrowPermissions(base.Permissions, held)
	}
	out := &RunPrincipal{
		Principal: orchestration.Principal{
			Principal:   access.Principal{Subject: base.Principal.Subject, Kind: access.KindAgent, ActingFor: acting, Grants: reaches, Credential: base.Principal.Credential},
			Permissions: permissions,
		},
		Run:               run.Str("id"),
		Agent:             run.Str("agent"),
		DefinitionVersion: run.Int("definitionVersion"),
	}
	if trust := run.Str("trust"); trust != "" {
		out.Trust = trust
	}
	return out, nil
}

func sortGrants(grants []access.Grant, position func(access.Grant) int) {
	for i := 1; i < len(grants); i++ {
		for j := i; j > 0 && position(grants[j]) < position(grants[j-1]); j-- {
			grants[j], grants[j-1] = grants[j-1], grants[j]
		}
	}
}

// PrincipalOfRequest is the principal of a request's bearer run token;
// nil for none.
func (a *Agents) PrincipalOfRequest(r *http.Request) (*RunPrincipal, error) {
	token, ok := session.BearerToken(r.Header)
	if !ok {
		return nil, nil
	}
	return a.PrincipalOf(r.Context(), token)
}

func (a *Agents) runOf(ctx context.Context, principal RunPrincipal) (orchestration.Row, error) {
	id := principal.Run
	if id == "" {
		id = principal.Subject
	}
	run, err := a.run(ctx, a.host, id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, refuse(401, "unauthorized", "no such run")
	}
	return run, nil
}

// ---- autonomy ----

func (a *Agents) policyMode(ctx context.Context, agent, operation, class string) (PolicyRow, bool, error) {
	var rows []PolicyRow
	// The rows in the order they were written: the first of equals wins.
	order := a.policies.C("id")
	if a.policies.Has("createdAt") {
		order = a.policies.C("createdAt") + ", " + order
	}
	for _, w := range []*orchestration.Where{a.policies.Where().Eq("agent", agent), a.policies.Where().Null("agent")} {
		found, err := orchestration.Find(ctx, a.host, w, order, 500)
		if err != nil {
			return PolicyRow{}, false, err
		}
		for _, r := range found {
			rows = append(rows, PolicyRow{Agent: r.Str("agent"), Operation: r.Str("operation"), OperationClass: r.Str("operationClass"), Mode: r.Str("mode"), Permission: r.Str("permission")})
		}
	}
	at := BestPolicy(rows, operation, class)
	if at < 0 {
		return PolicyRow{}, false, nil
	}
	return rows[at], true, nil
}

func (a *Agents) askApproval(ctx context.Context, run orchestration.Row, operation, class string, params any, permission, summary string) (orchestration.Row, error) {
	hash := ParamsHash(operation, params)
	var created orchestration.Row
	err := a.inTransaction(ctx, func(h orchestration.Host) error {
		var err error
		created, err = orchestration.Insert(ctx, h, a.approvals, map[string]any{
			"agent":          run.Str("agent"),
			"run":            run.Str("id"),
			"operation":      operation,
			"operationClass": class,
			"params":         a.redactJSON(params),
			"paramsHash":     hash,
			"summary":        a.redactText(summary),
			"permission":     permission,
			"state":          "pending",
			"expiresAt":      a.now().Add(time.Duration(a.opts.ApprovalTTLSeconds) * time.Second),
		})
		if err != nil {
			return err
		}
		if err := a.raise(ctx, h, a.approvals.Type, created.Str("id"), "approval.requested", map[string]any{"operation": operation, "summary": summary}, notify.Urgent); err != nil {
			return err
		}
		current, err := a.run(ctx, h, run.Str("id"))
		if err != nil || current == nil {
			return err
		}
		return a.write(a.self(ctx), h, a.runs, run.Str("id"), map[string]any{"approvals": current.Int("approvals") + 1})
	})
	return created, err
}

// GateOutcome is what Gate answers: run the call, in its mode, or the
// refusal the call answers instead; the approval it spent or waits on.
type GateOutcome struct {
	Run      bool
	Mode     string
	Status   int
	Code     string
	Detail   string
	Approval string
}

// GateOptions are the class of the operation Gate holds (internalWrite
// unless set) and its mode when no policy row names it.
type GateOptions struct {
	Class string
	Mode  string
}

// Gate holds a call of operation with args to the run's autonomy policy.
func (a *Agents) Gate(ctx context.Context, principal RunPrincipal, operation string, args map[string]any, opts GateOptions) (GateOutcome, error) {
	run, err := a.runOf(ctx, principal)
	if err != nil {
		return GateOutcome{}, err
	}
	class := opts.Class
	if class == "" {
		class = InternalWrite
	}
	policy, found, err := a.policyMode(ctx, run.Str("agent"), operation, class)
	if err != nil {
		return GateOutcome{}, err
	}
	trust := ""
	if run.Str("trust") != "" {
		trust = TrustOf(run.Str("trust"))
	}
	mode := DefaultAutonomy(class)
	switch {
	case found && policy.Mode != "":
		mode = policy.Mode
	case opts.Mode != "":
		mode = opts.Mode
	}
	mode = EffectiveAutonomy(mode, class, trust)
	if mode != Ask {
		return GateOutcome{Run: true, Mode: mode}, nil
	}
	hash := ParamsHash(operation, args)
	open, err := orchestration.Find(ctx, a.host, a.approvals.Where().Eq("run", run.Str("id")).Eq("operation", operation).In("state", "pending", "approved"), a.approvals.C("requestedAt"), 100)
	if err != nil {
		return GateOutcome{}, err
	}
	var same orchestration.Row
	for _, approval := range open {
		if at := approval.Time("expiresAt"); at != nil && !at.After(a.now()) {
			continue
		}
		// A changed parameter: every open approval of another hash no
		// longer holds.
		if approval.Str("paramsHash") != hash {
			if _, err := orchestration.UpdateID(ctx, a.host, a.approvals, approval.Str("id"), map[string]any{"state": "superseded"}); err != nil {
				return GateOutcome{}, err
			}
			continue
		}
		if same == nil {
			same = approval
		}
	}
	if same != nil && same.Str("state") == "approved" {
		if _, err := orchestration.UpdateID(ctx, a.host, a.approvals, same.Str("id"), map[string]any{"state": "used", "usedAt": a.now()}); err != nil {
			return GateOutcome{}, err
		}
		if run.Str("status") == "waiting" {
			if err := a.moveRun(ctx, run.Str("id"), "running", nil); err != nil {
				return GateOutcome{}, err
			}
		}
		return GateOutcome{Run: true, Mode: mode, Approval: same.Str("id")}, nil
	}
	denied, err := orchestration.FindOne(ctx, a.host, a.approvals.Where().Eq("run", run.Str("id")).Eq("operation", operation).Eq("paramsHash", hash).Eq("state", "denied"), "")
	if err != nil {
		return GateOutcome{}, err
	}
	if denied != nil {
		return GateOutcome{Status: 403, Code: "approval_denied", Detail: operation + " was denied", Approval: denied.Str("id")}, nil
	}
	approval := same
	if approval == nil {
		sender := ""
		if trust == Entered {
			sender = ", for a sender entered by hand and not verified"
		}
		permission := PermissionApprove
		if found && policy.Permission != "" {
			permission = policy.Permission
		}
		if approval, err = a.askApproval(ctx, run, operation, class, args, permission, fmt.Sprintf("%s asks to run %s with %s%s", run.Str("agentName"), operation, CanonicalJSON(args), sender)); err != nil {
			return GateOutcome{}, err
		}
	}
	if run.Str("status") == "running" {
		if err := a.moveRun(ctx, run.Str("id"), "waiting", nil); err != nil {
			return GateOutcome{}, err
		}
	}
	return GateOutcome{Status: 428, Code: "approval_required", Detail: fmt.Sprintf("%s runs once someone holding %s approves it: approval %s", operation, approval.Str("permission"), approval.Str("id")), Approval: approval.Str("id")}, nil
}

// decide decides the approval id as principal, in h (the host unless a
// reply's transaction), via the channel it came on.
func (a *Agents) decide(ctx context.Context, h orchestration.Host, principal orchestration.Principal, id, state, via string) error {
	approval, err := orchestration.ByID(ctx, h, a.approvals, id)
	if err != nil {
		return err
	}
	if approval == nil {
		return refuse(404, "not_found", "no such approval")
	}
	held := false
	for _, p := range principal.Permissions {
		if session.Covers(p, approval.Str("permission")) {
			held = true
		}
	}
	if !held {
		return refuse(403, "forbidden", "approving needs %s", approval.Str("permission"))
	}
	if principal.Kind == access.KindAgent {
		return refuse(403, "forbidden", "an agent run may not decide an approval")
	}
	if approval.Str("state") != "pending" {
		return refuse(409, "not_pending", "the approval is %s", approval.Str("state"))
	}
	if via == "" {
		via = "api"
	}
	if _, err := orchestration.UpdateID(ctx, h, a.approvals, id, map[string]any{"state": state, "decidedBy": principal.Subject, "decidedVia": via, "decidedAt": a.now()}); err != nil {
		return err
	}
	// A message to a party held for this approval goes, or is refused,
	// with it (D157).
	if holds := approval.Str("holds"); holds != "" {
		if in := a.inboundOf(); in != nil {
			if state == "approved" {
				if _, err := orchestration.UpdateID(ctx, h, a.approvals, id, map[string]any{"state": "used", "usedAt": a.now()}); err != nil {
					return err
				}
				if _, err := in.Release(ctx, holds); err != nil {
					return err
				}
			} else if err := in.Reject(ctx, holds, "its approval was denied"); err != nil {
				return err
			}
			if runID := approval.Str("run"); runID != "" {
				held, err := a.run(ctx, h, runID)
				if err != nil {
					return err
				}
				if held != nil && held.Str("status") == "waiting" {
					if err := a.moveRun(ctx, runID, "running", nil); err != nil {
						return err
					}
				}
			}
		}
	}
	if approval.Str("operation") == overBudget && approval.Str("run") != "" {
		runID := approval.Str("run")
		step := "watch"
		if state == "approved" {
			step = "start"
		}
		if err := a.enqueue(ctx, h, Request{Step: step, Run: runID}, 0); err != nil {
			return err
		}
		if state == "denied" {
			return a.finish(ctx, runID, "refused", "its approval to run past the budget was denied")
		}
	}
	return nil
}

// Approve and Deny decide an approval as principal, via the channel it
// came on (api unless set).
func (a *Agents) Approve(ctx context.Context, principal orchestration.Principal, approval, via string) error {
	return a.decide(ctx, a.host, principal, approval, "approved", via)
}

func (a *Agents) Deny(ctx context.Context, principal orchestration.Principal, approval, via string) error {
	return a.decide(ctx, a.host, principal, approval, "denied", via)
}

// ReplyActions are the reply actions approve and deny of
// approval.requested (D153), for notify's Replies: each decides the
// approval as the replier, within the channel's trust, in the reply's
// transaction.
func (a *Agents) ReplyActions() map[string]notify.ReplyAction {
	action := func(state, describe string) notify.ReplyAction {
		return notify.ReplyAction{
			Permission: PermissionApprove,
			Describe:   describe,
			Run: func(rc notify.ReplyContext) (any, error) {
				h, err := orchestration.Of(rc.Host)
				if err != nil {
					return nil, err
				}
				principal := orchestration.Principal{Principal: rc.Principal, Permissions: rc.Permissions}
				if err := a.decide(rc.Ctx, h, principal, rc.Notice.SubjectKey, state, rc.Channel); err != nil {
					return nil, err
				}
				return map[string]any{"approval": rc.Notice.SubjectKey}, nil
			},
		}
	}
	return map[string]notify.ReplyAction{"approve": action("approved", "Approved"), "deny": action("denied", "Denied")}
}

// SendPolicy is section 7's send approval for any send: a message to a
// party an agent run sends is held to its autonomy policy as class
// messageParty, allow when it runs, ask while its approval waits, deny
// when it was denied; a principal of any other kind is allow.
func (a *Agents) SendPolicy(ctx context.Context, principal orchestration.Principal, operation string, params map[string]any) (inbound.Decision, error) {
	if principal.Kind != access.KindAgent {
		return inbound.Allow, nil
	}
	run, err := a.run(ctx, a.host, principal.Subject)
	if err != nil {
		return "", err
	}
	if run == nil {
		return inbound.Allow, nil
	}
	gated, err := a.Gate(ctx, RunPrincipal{Principal: principal, Run: run.Str("id")}, operation, params, GateOptions{Class: MessageParty})
	if err != nil {
		return "", err
	}
	switch {
	case gated.Run:
		return inbound.Allow, nil
	case gated.Status == 428:
		return inbound.Ask, nil
	}
	return inbound.Deny, nil
}

func sendParams(input inbound.SendInput) map[string]any {
	params := map[string]any{"party": map[string]any{"table": input.Party.Table, "key": input.Party.Key}, "channel": input.Channel, "body": input.Body}
	if input.Subject != "" {
		params["subject"] = input.Subject
	}
	return params
}

// ApproveSend is the inbound path's approval policy (D157's approve): a
// send as an agent run is send_message, class messageParty, of its party,
// channel, subject and body.
func (a *Agents) ApproveSend(ctx context.Context, input inbound.SendInput) (inbound.Decision, error) {
	return a.SendPolicy(ctx, input.As, "send_message", sendParams(input))
}

var _ inbound.SendApproval = (*Agents)(nil)

// ToolAutonomy is a generated MCP tool's @autonomy (D183): its class and
// its mode when no policy row names it.
type ToolAutonomy struct {
	Class string `json:"class"`
	Mode  string `json:"mode,omitempty"`
}

// MCPGate holds a call of a generated API's MCP tool to the run's
// autonomy policy by its @autonomy (internalWrite without one), for a
// caller an agent run's token resolved to; any other caller's runs (nil).
// A Go server serves no MCP endpoint yet (D81, D116), so nothing calls it
// there: it is the gate for the day one does, as Rust's mcp_gate is.
func (a *Agents) MCPGate(ctx context.Context, principal *RunPrincipal, tool string, autonomy *ToolAutonomy, args map[string]any) (*GateOutcome, error) {
	if principal == nil || principal.Run == "" {
		return nil, nil
	}
	opts := GateOptions{Class: InternalWrite}
	if autonomy != nil {
		if autonomy.Class != "" {
			opts.Class = autonomy.Class
		}
		opts.Mode = autonomy.Mode
	}
	gated, err := a.Gate(ctx, *principal, tool, args, opts)
	if err != nil || gated.Run {
		return nil, err
	}
	return &gated, nil
}
