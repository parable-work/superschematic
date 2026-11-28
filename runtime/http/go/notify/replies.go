package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
)

// ReplyContext is what a reply action is given.
type ReplyContext struct {
	// Ctx acts as the reply's principal: the repositories' reads and
	// writes of it keep to what the principal sees (D156), and its
	// behaviors' commands run with the reply's permissions (D103).
	Ctx context.Context
	// Tx is the ORM's transaction it runs in (its TxInterface), and Host
	// the notifier's host over it.
	Tx   any
	Host notifications.Host
	// Notice is the notice it answers.
	Notice Notice
	// Principal is the reply's principal, and Permissions what the
	// channel's trust leaves it of its own.
	Principal   access.Principal
	Permissions []string
	// Args are the words after the verb and the reference.
	Args []string
	// Channel is the channel kind it came on.
	Channel string
	// Now is the time the notifier reads.
	Now time.Time
}

// ReplyAction is what a reply's verb runs (D153): the permission it
// needs, what it does, and how it is undone. The TypeScript runtime's
// ReplyAction.
type ReplyAction struct {
	// Permission is the permission the operation's route requires, which
	// the reply's principal must hold within its channel's trust; none for
	// an action any principal of the notice may run.
	Permission string
	// Describe is what the echo says was done: "Marked done"; "Done:
	// <verb>" unless set.
	Describe string
	// Run runs the action; what it returns is kept for its undo.
	Run func(rc ReplyContext) (any, error)
	// Undo undoes it with what Run returned, as JSON; an action without
	// one cannot be undone, and its echo says so.
	Undo func(rc ReplyContext, data json.RawMessage) error
}

// ReplyInput is a message an inbound adapter received.
type ReplyInput struct {
	// Channel is the channel kind it came on: sms, email, chat.
	Channel string
	// Slot is the slot whose adapter received it, which the echo goes back
	// through; the first of its kind unless set.
	Slot              string
	From              string
	Body              string
	ReceivedAt        time.Time
	ProviderMessageID string
}

// ReplyOutcome is what a reply came to.
type ReplyOutcome struct {
	// Outcome is done, refused, unknown, ambiguous, failed or verified.
	Outcome string
	// Reply is its row in the replies table.
	Reply  string
	Notice string
	Verb   string
	// Echo is what was echoed back to the sender.
	Echo   string
	Detail string
	// Principal is whom it ran as; nil for an unknown sender, which D157's
	// triage takes.
	Principal *RecipientRef
}

// trustOf is a channel kind's trust: its level and the permissions a
// reply on it may use (nil: all its principal holds). A kind with no row
// is low, and runs nothing past an acknowledgement.
func (n *Notifier) trustOf(ctx context.Context, channelKind string) (string, []string, error) {
	if n.trust == nil {
		return "low", []string{}, nil
	}
	found, err := n.findOne(ctx, n.host, n.trust.where().eq("channelKind", channelKind), "")
	if err != nil {
		return "", nil, err
	}
	if found == nil {
		return "low", []string{}, nil
	}
	if found.isNull("permissions") {
		return found.str("level"), nil, nil
	}
	permissions := found.strings("permissions")
	if permissions == nil {
		permissions = []string{}
	}
	return found.str("level"), permissions, nil
}

// record writes a reply's row of values, "" for a database with no
// replies table.
func (n *Notifier) record(ctx context.Context, h notifications.Host, values map[string]any) (string, error) {
	if n.reply == nil {
		return "", nil
	}
	created, err := n.insert(ctx, h, *n.reply, values)
	if err != nil {
		return "", err
	}
	return created.str("id"), nil
}

// echo sends text back to address on the channel input came on; a
// failure is logged.
func (n *Notifier) echo(ctx context.Context, input ReplyInput, address, text, recipient string) {
	if err := n.echoed(ctx, input, address, text, recipient); err != nil {
		n.log.Warn("echo failed", zap.String("detail", err.Error()))
	}
}

func (n *Notifier) echoed(ctx context.Context, input ReplyInput, address, text, recipient string) error {
	c, ok, err := n.channelOfKind(ctx, input.Channel, input.Slot)
	if err != nil || !ok {
		return err
	}
	created, err := n.insert(ctx, n.host, n.deliveries, map[string]any{"purpose": "echo", "recipient": nullable(recipient), "channelKind": input.Channel, "slot": c.slot, "address": address, "attempt": 1, "state": "queued"})
	if err != nil {
		return err
	}
	id := created.str("id")
	sent, err := c.adapter.Send(ctx, providers.OutboundMessage{To: address, Body: text, IdempotencyKey: id})
	if err != nil {
		return err
	}
	at := n.now()
	return n.update(ctx, n.host, n.deliveries, id, map[string]any{"state": sentState(sent.State), "providerMessageId": nullable(sent.ProviderMessageID), "sentAt": at, "endedAt": at})
}

func (n *Notifier) actionOf(kind, verb string) (ReplyAction, bool) {
	if action, ok := n.opts.Replies[kind][verb]; ok {
		return action, true
	}
	action, ok := n.opts.Replies["*"][verb]
	return action, ok
}

// replying is one reply as it is read: its input, its sender, and the
// values every row of it records.
type replying struct {
	input     ReplyInput
	address   string
	identity  ChannelIdentity
	recipient RecipientRef
	base      map[string]any
	read      ReadReply
}

func (r *replying) values(extra map[string]any) map[string]any {
	out := make(map[string]any, len(r.base)+len(extra))
	for k, v := range r.base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// refuse records the reply's outcome, echoes detail, and answers it.
func (n *Notifier) refuse(ctx context.Context, r *replying, outcome, detail string, notice string) (ReplyOutcome, error) {
	extra := map[string]any{"verb": r.read.Verb, "outcome": outcome, "detail": detail}
	if r.read.HasReference {
		extra["reference"] = r.read.Reference
	}
	if notice != "" {
		extra["notice"] = notice
	}
	id, err := n.record(ctx, n.host, r.values(extra))
	if err != nil {
		return ReplyOutcome{}, err
	}
	n.echo(ctx, r.input, r.address, detail, r.recipient.ID)
	recipient := r.recipient
	return ReplyOutcome{Outcome: outcome, Reply: id, Verb: r.read.Verb, Detail: detail, Echo: detail, Principal: &recipient, Notice: notice}, nil
}

// Reply is the entry point of an inbound message (D153): a reply, a
// verification code, or a sender the notifier does not know.
func (n *Notifier) Reply(ctx context.Context, input ReplyInput) (ReplyOutcome, error) {
	addressKind := n.addressKindOf(input.Channel)
	address := NormaliseAddress(addressKind, input.From)
	text := strings.TrimSpace(input.Body)
	received := input.ReceivedAt
	if received.IsZero() {
		received = n.now()
	}
	base := map[string]any{"receivedAt": received, "channelKind": input.Channel, "slot": nullable(input.Slot), "address": address, "providerMessageId": nullable(input.ProviderMessageID), "body": input.Body, "interpreter": "keyword"}
	with := func(extra map[string]any) map[string]any {
		out := make(map[string]any, len(base)+len(extra))
		for k, v := range base {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	identities, err := n.ResolveSender(ctx, addressKind, address)
	if err != nil {
		return ReplyOutcome{}, err
	}
	if len(identities) == 0 {
		// An address with a code pending verifies by replying with it.
		if n.identities != nil && LooksLikeCode(text) {
			pending, err := n.find(ctx, n.host, n.identities.where().eq("kind", addressKind).eq("address", address).eq("codePurpose", "verify").null("revokedAt"), "", 10)
			if err != nil {
				return ReplyOutcome{}, err
			}
			for _, identity := range pending {
				ok, err := n.checkCode(ctx, identity, text, "verify")
				if err != nil {
					return ReplyOutcome{}, err
				}
				if ok {
					id, err := n.record(ctx, n.host, with(map[string]any{"identity": identity.str("id"), "principal": identity.str("principal"), "verb": "verify", "outcome": "done"}))
					if err != nil {
						return ReplyOutcome{}, err
					}
					n.echo(ctx, input, address, "This address is confirmed.", identity.str("principal"))
					return ReplyOutcome{Outcome: "verified", Reply: id, Verb: "verify", Principal: &RecipientRef{Kind: identity.str("principalKind"), ID: identity.str("principal")}}, nil
				}
			}
		}
		id, err := n.record(ctx, n.host, with(map[string]any{"outcome": "unknown", "detail": "no verified identity holds the address"}))
		return ReplyOutcome{Outcome: "unknown", Reply: id}, err
	}
	principals := map[string]bool{}
	for _, identity := range identities {
		principals[identity.PrincipalKind+":"+SameID(identity.Principal)] = true
	}
	if len(principals) != 1 {
		detail := "the address resolves to more than one principal"
		id, err := n.record(ctx, n.host, with(map[string]any{"outcome": "ambiguous", "detail": detail}))
		return ReplyOutcome{Outcome: "ambiguous", Reply: id, Detail: detail}, err
	}
	identity := identities[0]
	r := &replying{
		input: input, address: address, identity: identity, read: ReadReplyText(text),
		recipient: RecipientRef{Kind: identity.PrincipalKind, ID: identity.Principal, Table: identity.PrincipalTable},
		base:      with(map[string]any{"identity": identity.ID, "principal": identity.Principal}),
	}
	if r.recipient.Kind == "" {
		r.recipient.Kind = "user"
	}

	if r.read.Undoes() {
		if !r.read.HasReference || n.reply == nil {
			return n.refuse(ctx, r, "ambiguous", "Reply UNDO and the reference number.", "")
		}
		notice, err := n.noticeOf(ctx, r.recipient, r.read.Reference)
		if err != nil {
			return ReplyOutcome{}, err
		}
		if notice == nil {
			return n.refuse(ctx, r, "refused", fmt.Sprintf("No notice %d is yours.", r.read.Reference), "")
		}
		done, err := n.findOne(ctx, n.host, n.reply.where().eq("notice", notice.ID).eq("principal", r.recipient.ID).eq("outcome", "done").null("undoneAt"), n.reply.c("receivedAt")+" DESC")
		if err != nil {
			return ReplyOutcome{}, err
		}
		if done == nil {
			return n.refuse(ctx, r, "refused", fmt.Sprintf("Nothing to undo on %d.", r.read.Reference), notice.ID)
		}
		return n.runUndo(ctx, r, *notice, done)
	}

	// Which notice it answers: the one it names, or the one waiting.
	var notice *Notice
	if r.read.HasReference {
		if notice, err = n.noticeOf(ctx, r.recipient, r.read.Reference); err != nil {
			return ReplyOutcome{}, err
		}
		if notice == nil {
			return n.refuse(ctx, r, "refused", fmt.Sprintf("No notice %d is yours.", r.read.Reference), "")
		}
	} else {
		open, err := n.Inbox(ctx, r.recipient, []string{"unread", "read"}, 50)
		if err != nil {
			return ReplyOutcome{}, err
		}
		var waiting []Notice
		for _, candidate := range open {
			sent, err := n.findOne(ctx, n.host, n.deliveries.where().eq("notice", candidate.ID).eq("channelKind", input.Channel).eq("address", address), "")
			if err != nil {
				return ReplyOutcome{}, err
			}
			if sent != nil {
				waiting = append(waiting, candidate)
			}
		}
		if len(waiting) != 1 {
			detail := "Nothing is waiting on you here."
			if len(waiting) > 1 {
				refs := make([]string, len(waiting))
				for i, w := range waiting {
					refs[i] = fmt.Sprintf("#%d", w.Reference)
				}
				detail = "Name the reference: " + strings.Join(refs, ", ") + "."
			}
			return n.refuse(ctx, r, "ambiguous", detail, "")
		}
		notice = &waiting[0]
	}

	level, limit, err := n.trustOf(ctx, input.Channel)
	if err != nil {
		return ReplyOutcome{}, err
	}
	// An identity entered by hand runs at the lowest trust: it
	// acknowledges and runs nothing else.
	if identity.Trust == "entered" {
		limit = []string{}
	}
	held, err := n.permissionsOf(ctx, r.recipient)
	if err != nil {
		return ReplyOutcome{}, err
	}
	permissions := NarrowedPermissions(held, limit)
	principal := access.Principal{Subject: r.recipient.ID, Kind: r.recipient.principalKind()}

	if r.read.Acknowledges() {
		if notice.closed() {
			return n.refuse(ctx, r, "refused", fmt.Sprintf("%d is acknowledged already.", notice.Reference), notice.ID)
		}
		var id string
		err := n.host.Transaction(ctx, func(h notifications.Host) error {
			if err := n.acknowledgeIn(ctx, h, *notice, input.Channel); err != nil {
				return err
			}
			var err error
			id, err = n.record(ctx, h, r.values(map[string]any{"verb": "ack", "notice": notice.ID, "reference": notice.Reference, "outcome": "done", "undo": map[string]any{"previous": notice.State}}))
			return err
		})
		if err != nil {
			return ReplyOutcome{}, err
		}
		said := fmt.Sprintf("Acknowledged %d: %s. Reply UNDO %d to undo.", notice.Reference, notice.Title, notice.Reference)
		n.echo(ctx, input, address, said, r.recipient.ID)
		recipient := r.recipient
		return ReplyOutcome{Outcome: "done", Reply: id, Notice: notice.ID, Verb: "ack", Echo: said, Principal: &recipient}, nil
	}

	verb := r.read.Verb
	action, ok := n.actionOf(notice.EventKind, verb)
	if !ok || action.Run == nil {
		return n.refuse(ctx, r, "refused", fmt.Sprintf("%q is no reply %d takes.", verb, notice.Reference), notice.ID)
	}
	if action.Permission != "" && !ReplyAllows(held, limit, action.Permission) {
		detail := fmt.Sprintf("You may not %s %d.", verb, notice.Reference)
		if coversAny(held, action.Permission) {
			trust := level
			if identity.Trust == "entered" {
				trust = "entered by hand"
			}
			detail = fmt.Sprintf("A reply by %s may not %s: its trust is %s. Use the app.", input.Channel, verb, trust)
		}
		return n.refuse(ctx, r, "refused", detail, notice.ID)
	}
	var id string
	err = n.host.Transaction(ctx, func(h notifications.Host) error {
		rc := ReplyContext{Ctx: h.Acting(notifications.WithChannel(ctx, input.Channel), principal, permissions), Tx: h.Tx(), Host: h, Notice: *notice, Principal: principal, Permissions: permissions, Args: r.read.Args, Channel: input.Channel, Now: n.now()}
		data, err := action.Run(rc)
		if err != nil {
			return err
		}
		values := map[string]any{"state": "done", "acknowledgedVia": input.Channel}
		if notice.AcknowledgedAt == nil {
			values["acknowledgedAt"] = n.now()
		}
		if err := n.update(ctx, h, n.inbox, notice.ID, values); err != nil {
			return err
		}
		if err := n.endDeliveries(ctx, h, notice.ID, func(state string) bool { return !ended(state) }); err != nil {
			return err
		}
		var undo any
		if action.Undo != nil {
			undo = map[string]any{"data": data, "previous": notice.State}
		}
		id, err = n.record(ctx, h, r.values(map[string]any{"verb": verb, "notice": notice.ID, "reference": notice.Reference, "outcome": "done", "undo": undo}))
		return err
	})
	if err != nil {
		return n.refuse(ctx, r, "failed", fmt.Sprintf("%s %d failed: %s", verb, notice.Reference, err.Error()), notice.ID)
	}
	describe := action.Describe
	if describe == "" {
		describe = "Done: " + verb
	}
	said := fmt.Sprintf("%s %d: %s.", describe, notice.Reference, notice.Title)
	if action.Undo != nil {
		said += fmt.Sprintf(" Reply UNDO %d to undo.", notice.Reference)
	} else {
		said += " This cannot be undone here."
	}
	n.echo(ctx, input, address, said, r.recipient.ID)
	recipient := r.recipient
	return ReplyOutcome{Outcome: "done", Reply: id, Notice: notice.ID, Verb: verb, Echo: said, Principal: &recipient}, nil
}

// noticeOf is recipient's notice of reference, nil for none.
func (n *Notifier) noticeOf(ctx context.Context, recipient RecipientRef, reference int64) (*Notice, error) {
	if !n.inbox.holds("recipient", recipient.ID) {
		return nil, nil
	}
	found, err := n.findOne(ctx, n.host, n.inbox.where().eq("recipient", recipient.ID).eq("reference", reference), "")
	if err != nil || found == nil {
		return nil, err
	}
	notice := noticeOf(found)
	return &notice, nil
}

func (n *Notifier) runUndo(ctx context.Context, r *replying, notice Notice, done row) (ReplyOutcome, error) {
	undo, _ := done["undo"].(map[string]any)
	held, err := n.permissionsOf(ctx, r.recipient)
	if err != nil {
		return ReplyOutcome{}, err
	}
	_, limit, err := n.trustOf(ctx, r.input.Channel)
	if err != nil {
		return ReplyOutcome{}, err
	}
	permissions := NarrowedPermissions(held, limit)
	principal := access.Principal{Subject: r.recipient.ID, Kind: r.recipient.principalKind()}
	verb := done.str("verb")
	var action ReplyAction
	hasAction := false
	if verb != "ack" {
		action, hasAction = n.actionOf(notice.EventKind, verb)
	}
	recipient := r.recipient
	if undo == nil || (verb != "ack" && (!hasAction || action.Undo == nil)) {
		id, err := n.record(ctx, n.host, r.values(map[string]any{"verb": UndoVerb, "notice": notice.ID, "reference": notice.Reference, "outcome": "refused", "detail": verb + " cannot be undone here"}))
		return ReplyOutcome{Outcome: "refused", Reply: id, Notice: notice.ID, Verb: UndoVerb, Principal: &recipient}, err
	}
	var id string
	err = n.host.Transaction(ctx, func(h notifications.Host) error {
		if hasAction && action.Undo != nil {
			data, err := json.Marshal(undo["data"])
			if err != nil {
				return err
			}
			rc := ReplyContext{Ctx: h.Acting(notifications.WithChannel(ctx, r.input.Channel), principal, permissions), Tx: h.Tx(), Host: h, Notice: notice, Principal: principal, Permissions: permissions, Args: []string{}, Channel: r.input.Channel, Now: n.now()}
			if err := action.Undo(rc, data); err != nil {
				return err
			}
		}
		previous := textOf(undo["previous"])
		if previous == "" {
			previous = "read"
		}
		if err := n.update(ctx, h, n.inbox, notice.ID, map[string]any{"state": previous, "acknowledgedAt": nil, "acknowledgedVia": nil}); err != nil {
			return err
		}
		if err := n.update(ctx, h, *n.reply, done.str("id"), map[string]any{"undoneAt": n.now()}); err != nil {
			return err
		}
		var err error
		id, err = n.record(ctx, h, r.values(map[string]any{"verb": UndoVerb, "notice": notice.ID, "reference": notice.Reference, "outcome": "done"}))
		return err
	})
	if err != nil {
		return ReplyOutcome{}, err
	}
	said := fmt.Sprintf("Undone: %s %d.", verb, notice.Reference)
	n.echo(ctx, r.input, r.address, said, r.recipient.ID)
	return ReplyOutcome{Outcome: "done", Reply: id, Notice: notice.ID, Verb: UndoVerb, Echo: said, Principal: &recipient}, nil
}

func (n *Notifier) slotChannel(ctx context.Context, slot string) (channel, error) {
	all, err := n.channels(ctx)
	if err != nil {
		return channel{}, err
	}
	for _, c := range all {
		if c.slot == slot {
			return c, nil
		}
	}
	return channel{}, refused("slot %s is no channel of this environment", slot)
}

// Inbound is the replies an adapter's @webhook request carries, each
// through Reply. An adapter whose definition has no inbound operation
// carries none: in Go, one that is no providers.ChannelInboundReader, as
// Providers binds it.
func (n *Notifier) Inbound(ctx context.Context, slot string, request providers.WebhookRequest) ([]ReplyOutcome, error) {
	c, err := n.slotChannel(ctx, slot)
	if err != nil {
		return nil, err
	}
	reader, ok := c.adapter.(providers.ChannelInboundReader)
	if !ok {
		return []ReplyOutcome{}, nil
	}
	messages, err := reader.Inbound(ctx, request)
	if err != nil {
		return nil, err
	}
	out := make([]ReplyOutcome, 0, len(messages))
	for _, message := range messages {
		outcome, err := n.Reply(ctx, ReplyInput{Channel: c.kind, Slot: slot, From: message.From, Body: message.Body, ReceivedAt: message.ReceivedAt, ProviderMessageID: message.ProviderMessageID})
		if err != nil {
			return out, err
		}
		out = append(out, outcome)
	}
	return out, nil
}

var stateOrder = map[string]int{"queued": 0, "sent": 1, "delivered": 2, "read": 3, "failed": 4, "acknowledged": 5}

// Statuses applies the delivery statuses an adapter's @webhook request
// carries to the deliveries they name, and returns how many moved. An
// adapter whose definition has no status operation reports none.
func (n *Notifier) Statuses(ctx context.Context, slot string, request providers.WebhookRequest) (int, error) {
	c, err := n.slotChannel(ctx, slot)
	if err != nil {
		return 0, err
	}
	reader, ok := c.adapter.(providers.ChannelStatusReader)
	if !ok {
		return 0, nil
	}
	statuses, err := reader.Status(ctx, request)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, status := range statuses {
		if status.ProviderMessageID == "" {
			continue
		}
		delivery, err := n.findOne(ctx, n.host, n.deliveries.where().eq("providerMessageId", status.ProviderMessageID), "")
		if err != nil {
			return moved, err
		}
		state := string(status.State)
		if delivery == nil || ended(delivery.str("state")) || stateOrder[state] <= stateOrder[delivery.str("state")] {
			continue
		}
		if state == "failed" {
			detail := status.Detail
			if detail == "" {
				detail = "the service reported a failure"
			}
			if err := n.fallThroughAlone(ctx, delivery, detail); err != nil {
				return moved, err
			}
		} else {
			at := status.At
			if at.IsZero() {
				at = n.now()
			}
			values := map[string]any{"state": state}
			switch state {
			case "delivered":
				values["deliveredAt"] = at
			case "read":
				values["readAt"], values["endedAt"] = at, at
			}
			if err := n.update(ctx, n.host, n.deliveries, delivery.str("id"), values); err != nil {
				return moved, err
			}
		}
		moved++
	}
	return moved, nil
}

// ChannelHealthOf is one channel adapter's health.
type ChannelHealthOf struct {
	Slot   string
	Kind   string
	OK     bool
	Detail string
}

// CheckHealth asks every channel adapter its health; one that fails
// raises channel.down once.
func (n *Notifier) CheckHealth(ctx context.Context) ([]ChannelHealthOf, error) {
	all, err := n.channels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelHealthOf, 0, len(all))
	for _, c := range all {
		n.healthMu.Lock()
		delete(n.health, c.slot)
		n.healthMu.Unlock()
		ok, detail, err := n.healthOf(ctx, c)
		if err != nil {
			return out, err
		}
		out = append(out, ChannelHealthOf{Slot: c.slot, Kind: c.kind, OK: ok, Detail: detail})
	}
	return out, nil
}

// ApprovalReplies are the verbs approve and deny of an approval's notice
// (D153, section 8): each decides the approval the notice is about, a row
// of table, as the replier, who must hold the approval's own permission
// (its permission member) within the channel's trust, and may not be an
// agent run; a decided approval is refused. It writes the row's state
// (approved or denied), decidedBy, decidedVia (the channel) and
// decidedAt, the members of D158's approvals. permission is the one a
// reply needs to run either verb at all (agents.approve). Give them for
// the event kind approval.requested, as the Rust runtime's
// approval_replies (D191).
func ApprovalReplies(table, permission string) map[string]ReplyAction {
	decide := func(state, describe string) ReplyAction {
		return ReplyAction{
			Permission: permission,
			Describe:   describe,
			Run: func(rc ReplyContext) (any, error) {
				key := rc.Notice.SubjectKey
				if key == "" {
					return nil, errors.New("the notice is about no approval")
				}
				// The approval is read and written as the system: the
				// replier's right to decide it is its permission, not its
				// audience.
				sys := rc.Host.System(rc.Ctx)
				approval, found, err := rc.Host.Record(sys, table, key)
				if err != nil {
					return nil, err
				}
				if !found {
					return nil, errors.New("no such approval")
				}
				needs := textOf(approval["permission"])
				if needs == "" {
					needs = permission
				}
				if !coversAny(rc.Permissions, needs) {
					return nil, fmt.Errorf("approving needs %s", needs)
				}
				if rc.Principal.Kind == access.KindAgent {
					return nil, errors.New("an agent run may not decide an approval")
				}
				if current := textOf(approval["state"]); current != "pending" {
					return nil, fmt.Errorf("the approval is %s", current)
				}
				update, err := json.Marshal(map[string]any{"state": state, "decidedBy": rc.Principal.Subject, "decidedVia": rc.Channel, "decidedAt": rc.Now.UTC().Format("2006-01-02T15:04:05.000Z")})
				if err != nil {
					return nil, err
				}
				if err := rc.Host.Update(sys, table, key, update); err != nil {
					return nil, err
				}
				return map[string]any{"approval": key}, nil
			},
		}
	}
	return map[string]ReplyAction{"approve": decide("approved", "Approved"), "deny": decide("denied", "Denied")}
}
