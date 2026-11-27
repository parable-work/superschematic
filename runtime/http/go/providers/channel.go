package providers

import (
	"context"
	"time"
)

// The channel adapter (D159): what notifications (D153) send through. An
// adapter sends a message and reports its health. One whose service posts
// delivery statuses and inbound messages back reads them from the request
// a @webhook route received (ChannelStatusReader, ChannelInboundReader):
// the route is the app's, so the service's callback runs through the same
// operation, permissions and audit as any other caller. Core keeps only an
// in-app inbox; email, SMS and messaging bridges are adapters.

// DeliveryState is where a delivery stands, as the adapter's service
// reports it: queued, sent, delivered, read or failed.
type DeliveryState string

// The delivery states.
const (
	DeliveryQueued    DeliveryState = "queued"
	DeliverySent      DeliveryState = "sent"
	DeliveryDelivered DeliveryState = "delivered"
	DeliveryRead      DeliveryState = "read"
	DeliveryFailed    DeliveryState = "failed"
)

// OutboundMessage is a message to send.
type OutboundMessage struct {
	// To is the recipient's address on the channel: a phone number, an
	// email address, a chat handle.
	To   string `json:"to"`
	Body string `json:"body"`
	// Subject is a subject, for a channel that shows one.
	Subject string `json:"subject,omitempty"`
	// Reference is the reference number a reply names to say what it
	// answers (D153).
	Reference string `json:"reference,omitempty"`
	// IdempotencyKey is a key the adapter passes to its service so a
	// retried send sends once.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// MessageID is the message's id in the channel's threads, for a
	// channel that threads (D157): an email's Message-ID header.
	MessageID string `json:"messageId,omitempty"`
	// InReplyTo is the id of the message it answers: an email's
	// In-Reply-To header.
	InReplyTo string `json:"inReplyTo,omitempty"`
	// References are the ids of the messages of its thread before it: an
	// email's References header.
	References []string `json:"references,omitempty"`
	// ReplyTo is where a reply goes: the deployment's inbound address, for
	// email (D157).
	ReplyTo string `json:"replyTo,omitempty"`
}

// SendResult is what Send answers.
type SendResult struct {
	// ProviderMessageID is the service's id of the message, which its
	// statuses and replies name.
	ProviderMessageID string        `json:"providerMessageId"`
	State             DeliveryState `json:"state"`
}

// DeliveryStatus is one delivery status a service reported.
type DeliveryStatus struct {
	ProviderMessageID string        `json:"providerMessageId"`
	State             DeliveryState `json:"state"`
	At                time.Time     `json:"at"`
	// Detail is why a delivery failed, in the service's words.
	Detail string `json:"detail,omitempty"`
}

// InboundAuthentication is an email's authentication results, as an
// inbound adapter's service reports them (D157): pass, fail, softfail,
// neutral, none, temperror or permerror.
type InboundAuthentication struct {
	SPF   string `json:"spf,omitempty"`
	DKIM  string `json:"dkim,omitempty"`
	DMARC string `json:"dmarc,omitempty"`
}

// InboundAttachment is a file an inbound message came with, its bytes
// fetched by the adapter.
type InboundAttachment struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Bytes     []byte `json:"bytes"`
}

// The opt-out keywords an adapter reads (D157).
const (
	OptOut = "out"
	OptIn  = "in"
)

// InboundMessage is one message a service received for the app.
type InboundMessage struct {
	// From is the sender's address on the channel.
	From              string    `json:"from"`
	Body              string    `json:"body"`
	ReceivedAt        time.Time `json:"receivedAt"`
	ProviderMessageID string    `json:"providerMessageId"`
	// InReplyTo is the id of the message it replies to, when the service
	// threads replies.
	InReplyTo string `json:"inReplyTo,omitempty"`
	// MessageID is the message's own id in the channel's threads (D157).
	MessageID string `json:"messageId,omitempty"`
	// References are the ids of the messages of its thread before it.
	References []string `json:"references,omitempty"`
	// To are the addresses it was sent to.
	To []string `json:"to,omitempty"`
	// Subject is its subject, for a channel that has one.
	Subject string `json:"subject,omitempty"`
	// Authentication is what the service found of an email's
	// authentication (D157). The adapter reports; the core decides.
	Authentication *InboundAuthentication `json:"authentication,omitempty"`
	// Attachments are the files it came with.
	Attachments []InboundAttachment `json:"attachments,omitempty"`
	// OptOut is an opt-out or opt-in keyword of the channel the adapter
	// read (D157): OptOut for STOP and its kin, OptIn for START.
	OptOut string `json:"optOut,omitempty"`
}

// WebhookRequest is a request a @webhook route received from the
// adapter's service, as the route read it.
type WebhookRequest struct {
	// Headers are the request's headers, names in lower case.
	Headers map[string]string `json:"headers"`
	// Body is the raw body, which a signature covers.
	Body string `json:"body"`
}

// ChannelHealth is what Health answers. An adapter that is not OK is
// skipped, and the others carry the alert that it is down.
type ChannelHealth struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ChannelAdapter is a channel adapter.
type ChannelAdapter interface {
	// Kind is the kind of channel it sends on: "sms", "email", "chat". A
	// channel kind's trust level is data (D153).
	Kind() string
	Send(ctx context.Context, message OutboundMessage) (SendResult, error)
	Health(ctx context.Context) (ChannelHealth, error)
}

// ChannelStatusReader is the optional status operation of a channel
// adapter: the delivery statuses a callback request carries. A service
// that reports none leaves it out.
type ChannelStatusReader interface {
	Status(ctx context.Context, request WebhookRequest) ([]DeliveryStatus, error)
}

// ChannelInboundReader is the optional inbound operation of a channel
// adapter: the messages an inbound request carries. A send-only service
// leaves it out.
type ChannelInboundReader interface {
	Inbound(ctx context.Context, request WebhookRequest) ([]InboundMessage, error)
}
