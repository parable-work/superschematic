// Package notify is the notifier of D153 in Go (D190): events raised on a
// table's rows, routed by rows, held to each recipient's audience when
// sent, and delivered per recipient through the channel adapters an
// environment configures (D159), with replies that run as their sender.
// It is the TypeScript runtime's notify.ts, with its tables, statements,
// states and refusals, and the Rust runtime's orm::notify (D191), whose
// settled points it keeps.
//
// Events. A table declares the events its rows raise with @event, which
// the generated ORM's definitions carry. Raise writes an event as a
// message of the notifier's dispatch queue in the caller's transaction:
// it commits with the change that caused it or not at all. A behavior's
// event (D104), which the Go behaviors adapter hands a project's Workers
// method with the transaction it runs in, reaches the notifier through
// FromBehaviorEvent, and a cleared reference (D154) through Cleared.
//
// Routing. The worker routes an event in one transaction: each enabled
// route of its kind (or *) and table whose filter, in the repositories'
// filter syntax, the record matches, names recipients by relation paths
// of the record, principals, roles or parties. A recipient who cannot
// read the record, through its table's repository as the recipient (D156:
// audience and grants), is not notified, and nothing records that it was
// skipped. A recipient who can gets one notice in the inbox, numbered
// among its own, and one delivery on the first channel its preferences
// name, sent at once, after its quiet hours, or in its next digest, by
// the urgency: urgent ignores quiet hours and escalates through its
// route's steps unless acknowledged in time, each step a delayed message
// of the same queue; normal waits out quiet hours; digest joins the next
// digest; record sends nothing.
//
// Delivery. A delivery is queued, then sent, delivered and read, or ends
// failed or acknowledged. An adapter whose health check fails is skipped,
// and the notifier raises channel.down, routed as any event. A failure, or
// no delivery report within the timeout from an adapter that reports
// them, falls through to the recipient's next channel.
//
// Replies. Reply is the entry point an inbound adapter's route calls with
// a sender and the text, and Inbound reads an adapter's @webhook request
// and calls it per message. A sender whose channel identity is verified
// (or entered by hand) runs as its principal; an unknown one reaches
// nothing and answers unknown. The channel kind's trust row caps the
// permissions a reply may use, and an identity entered by hand reaches no
// operation past an acknowledgement. ack acknowledges; any other verb is a
// ReplyAction the app gives for the notice's event kind, which runs in a
// transaction as the principal through the same repository write as its
// route. An approval's approve and deny (ApprovalReplies) are two such
// actions. Every reply is a row of the replies table, and every action is
// echoed back with its undo.
//
// The pure parts here are shared with the TypeScript and Rust runtimes
// through runtime/http/testdata/notify_parity.json. The notifier is
// Postgres's alone, as the loader holds it to.
package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// DefaultDeliveryRetentionDays is how long a delivery that ended is kept
// before Prune deletes it.
const DefaultDeliveryRetentionDays = 90

// DefaultDeliveryTimeoutSeconds is how long a delivery an adapter reports
// on may go unreported before it falls through to the next channel.
const DefaultDeliveryTimeoutSeconds = 300

// HealthTTLMillis is how long an adapter's health check holds before it is
// asked again.
const HealthTTLMillis = 60_000

// CodeTTLSeconds is how long a code sent to an address answers, and
// CodeAttempts how many wrong answers spend it.
const (
	CodeTTLSeconds = 1800
	CodeAttempts   = 5
)

// InApp is the channel kind of the in-app inbox, which every notice
// reaches.
const InApp = "inapp"

// The urgencies of an event (D153).
const (
	Urgent = "urgent"
	Normal = "normal"
	Digest = "digest"
	Record = "record"
)

// AckVerbs are the verbs that acknowledge a notice, and UndoVerb the one
// that undoes the latest action on it.
var AckVerbs = []string{"ack", "ok", "acknowledge", "acknowledged"}

// UndoVerb undoes the latest action on a notice.
const UndoVerb = "undo"

// SameID is an id in one form, whatever form a UUID came in: a UUID's
// base62, and any other text as it is.
func SameID(id string) string {
	return rowrefs.UUIDFromDB(id)
}

// NormaliseAddress is an address normalised as the kind of address it is:
// an email address in lower case, a phone number as + and its digits, a
// handle trimmed.
func NormaliseAddress(kind, address string) string {
	text := strings.TrimSpace(address)
	switch kind {
	case "email":
		return strings.ToLower(text)
	case "phone":
		var digits strings.Builder
		for _, r := range text {
			if r >= '0' && r <= '9' {
				digits.WriteRune(r)
			}
		}
		d := digits.String()
		if strings.HasPrefix(text, "+") || strings.HasPrefix(text, "00") {
			return "+" + strings.TrimPrefix(d, "00")
		}
		return "+" + d
	}
	return text
}

// DefaultAddressKind is the kind of address a channel kind sends to unless
// the notifier is told otherwise: email to email, sms and voice to phone,
// any other to handle.
func DefaultAddressKind(channel string) string {
	switch channel {
	case "email":
		return "email"
	case "sms", "voice":
		return "phone"
	}
	return "handle"
}

// CodeHash is the SHA-256 of a code, in hex, as the channel identities
// hold it: the code trimmed and in upper case.
func CodeHash(code string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hex.EncodeToString(sum[:])
}

// ReadReply is a reply's words as the keyword interpreter reads them: its
// verb in lower case, the reference a second word of digits names (done
// 142, ack #7), and the words after.
type ReadReply struct {
	Verb      string
	Reference int64
	// HasReference is whether the reply named one.
	HasReference bool
	Args         []string
}

// Acknowledges reports whether the verb acknowledges.
func (r ReadReply) Acknowledges() bool {
	for _, verb := range AckVerbs {
		if r.Verb == verb {
			return true
		}
	}
	return false
}

// Undoes reports whether the verb undoes.
func (r ReadReply) Undoes() bool { return r.Verb == UndoVerb }

// ReadReplyText reads a reply's text: its first word is the verb, a
// second of digits (with or without #) the reference, and the rest its
// words.
func ReadReplyText(text string) ReadReply {
	words := strings.FieldsFunc(text, unicode.IsSpace)
	out := ReadReply{Args: []string{}}
	if len(words) == 0 {
		return out
	}
	out.Verb = strings.ToLower(words[0])
	rest := words[1:]
	if len(rest) > 0 {
		digits := strings.TrimPrefix(rest[0], "#")
		if digits != "" && strings.Trim(digits, "0123456789") == "" {
			if n, err := strconv.ParseInt(digits, 10, 64); err == nil {
				out.Reference, out.HasReference = n, true
				rest = rest[1:]
			}
		}
	}
	out.Args = append(out.Args, rest...)
	return out
}

// LooksLikeCode reports whether a reply's text is shaped as a code an
// address answers its verification with: four to twelve letters and
// digits.
func LooksLikeCode(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) < 4 || len(text) > 12 {
		return false
	}
	for _, r := range text {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// minutesOf is HH:MM as minutes of the day, false for any other text.
func minutesOf(text string) (int, bool) {
	hour, minute, ok := strings.Cut(text, ":")
	if !ok || hour == "" || len(hour) > 2 || len(minute) != 2 || strings.Trim(hour+minute, "0123456789") != "" {
		return 0, false
	}
	h, _ := strconv.Atoi(hour)
	m, _ := strconv.Atoi(minute)
	return h*60 + m, true
}

// QuietDelaySeconds is how many seconds from a wall time (its minutes of
// the day and its second) until the quiet hours [start, end) end, or 0
// when it is outside them. A window that crosses midnight (22:00 to
// 07:00) is read as one. The notifier reads the wall time of an instant in
// a zone from Postgres (AT TIME ZONE), which holds the zone database.
func QuietDelaySeconds(wallMinutes, second int, start, end string) int {
	from, okFrom := minutesOf(start)
	to, okTo := minutesOf(end)
	if !okFrom || !okTo || from == to {
		return 0
	}
	now := wallMinutes
	inside := now >= from && now < to
	if from > to {
		inside = now >= from || now < to
	}
	if !inside {
		return 0
	}
	minutes := (to - now + 1440) % 1440
	return max(minutes*60-second, 0)
}

// ReplyAllows reports whether held, a principal's permissions, may use
// need within limit, the permissions its channel's trust leaves a reply:
// nil limits nothing, and an empty limit allows nothing.
func ReplyAllows(held, limit []string, need string) bool {
	if !coversAny(held, need) {
		return false
	}
	if limit == nil {
		return true
	}
	return coversAny(limit, need)
}

// NarrowedPermissions is held narrowed to limit: each held permission
// limit covers, and each of limit's that a held one covers, each once in
// that order; nil limits nothing.
func NarrowedPermissions(held, limit []string) []string {
	if limit == nil {
		return append([]string{}, held...)
	}
	out := []string{}
	add := func(p string) {
		for _, have := range out {
			if have == p {
				return
			}
		}
		out = append(out, p)
	}
	for _, h := range held {
		for _, c := range limit {
			switch {
			case session.Covers(c, h):
				add(h)
			case session.Covers(h, c):
				add(c)
			}
		}
	}
	return out
}

func coversAny(held []string, need string) bool {
	for _, h := range held {
		if session.Covers(h, need) {
			return true
		}
	}
	return false
}
