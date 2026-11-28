package crosstable

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

/*
The calendar feed (D152), for a calendar client, which sends no
Authorization header: its URL holds a token. The token is a grant (D156) of
the database's @accessGrants table: its holder the user it reads as
(holderKind user), its role a row of the UserRole table whose permissions
bound what the feed may read, its target the feed itself (targetType
`CalendarFeed;detail=...;sources=...`, targetId the holder), its end a year
on unless set. The generated ORM's AuthenticateGrant refuses such a grant
on every other route, so a leaked feed URL reads the feed and nothing else;
a revoked or expired one is refused at the next fetch, and rotating one
revokes it and issues another with the same scope. Its detail is full
(each entry's title) or busy ("Busy" and a reference number, a short hash
of the entry's source and key, which its owner looks up).
*/

// CalendarDetail is what a feed shows of each entry: its title (full), or
// "Busy" and a reference number (busy).
type CalendarDetail string

// The details of a feed.
const (
	DetailFull CalendarDetail = "full"
	DetailBusy CalendarDetail = "busy"
)

// CalendarFeed is what a calendar feed's grant's targetType begins with;
// its scope follows, `;detail=busy;sources=a,b`.
const CalendarFeed = "CalendarFeed"

// CalendarFeedDays is how long a feed's token lasts unless its issue
// says: a year.
const CalendarFeedDays = 365

// IsCalendarFeed reports whether a grant's target type is a calendar
// feed's.
func IsCalendarFeed(targetType string) bool {
	return targetType == CalendarFeed || strings.HasPrefix(targetType, CalendarFeed+";")
}

// CalendarFeedScope is what a feed reads, as its grant's targetType holds
// it: its detail, and its sources, every one when nil.
type CalendarFeedScope struct {
	Detail  CalendarDetail
	Sources []string
}

// TargetType is the scope as a feed's grant's targetType.
func (s CalendarFeedScope) TargetType() string {
	detail := s.Detail
	if detail != DetailFull {
		detail = DetailBusy
	}
	out := CalendarFeed + ";detail=" + string(detail)
	if len(s.Sources) > 0 {
		out += ";sources=" + strings.Join(s.Sources, ",")
	}
	return out
}

// FeedScopeOf is the scope a feed's grant's targetType holds.
func FeedScopeOf(targetType string) CalendarFeedScope {
	scope := CalendarFeedScope{Detail: DetailBusy}
	parts := strings.Split(targetType, ";")
	for _, part := range parts[1:] {
		name, value, _ := strings.Cut(part, "=")
		switch name {
		case "detail":
			if value == string(DetailFull) {
				scope.Detail = DetailFull
			} else {
				scope.Detail = DetailBusy
			}
		case "sources":
			var sources []string
			for _, s := range strings.Split(value, ",") {
				if s != "" {
					sources = append(sources, s)
				}
			}
			scope.Sources = sources
		}
	}
	return scope
}

// IssueCalendarFeed is what IssueCalendarFeedOf takes: the user the feed
// reads as, its principal's subject; the UserRole row whose permissions
// bound what it reads; its scope; when it starts, now when zero; and when
// it expires, CalendarFeedDays on when zero. A route's feed starts at the
// route's clock, which reads it by that clock too.
type IssueCalendarFeed struct {
	Holder   string
	Role     string
	Scope    CalendarFeedScope
	StartsAt time.Time
	EndsAt   time.Time
}

// IssuedCalendarFeed is a feed issued: its grant's id, the token only its
// holder is given, and when it expires.
type IssuedCalendarFeed struct {
	ID     string    `json:"id"`
	Token  string    `json:"token"`
	EndsAt time.Time `json:"endsAt"`
}

// ErrNoGrants is the error of a feed of a database with no @accessGrants
// table.
var ErrNoGrants = errors.New("calendar: the ORM has no @accessGrants table")

func grantsOf(h Host) (Grants, error) {
	g := h.Grants()
	if g == nil {
		return nil, ErrNoGrants
	}
	return g, nil
}

// IssueFeed issues a calendar feed (D152): a grant of the database's
// @accessGrants table that reads the feed alone, as its holder, and the
// token its URL holds, which only the holder is given. It writes as ctx
// is, so the grant records who made it.
func IssueFeed(ctx context.Context, h Host, input IssueCalendarFeed) (IssuedCalendarFeed, error) {
	grants, err := grantsOf(h)
	if err != nil {
		return IssuedCalendarFeed{}, err
	}
	ends := input.EndsAt
	if ends.IsZero() {
		ends = time.Now().Add(CalendarFeedDays * 24 * time.Hour)
	}
	id, token, err := grants.Issue(ctx, access.IssueGrantInput{
		Holder:     input.Holder,
		HolderKind: string(access.KindUser),
		Role:       input.Role,
		TargetType: input.Scope.TargetType(),
		TargetID:   input.Holder,
		StartsAt:   input.StartsAt,
		EndsAt:     &ends,
	})
	if err != nil {
		return IssuedCalendarFeed{}, err
	}
	return IssuedCalendarFeed{ID: id, Token: token, EndsAt: ends}, nil
}

// feedGrant is a feed's grant by its id, as the system; false when it is
// none or no feed's.
func feedGrant(ctx context.Context, grants Grants, id string) (access.Grant, bool, error) {
	grant, ok, err := grants.ByID(ctx, id)
	if err != nil || !ok || !IsCalendarFeed(grant.TargetType) {
		return access.Grant{}, false, err
	}
	return grant, true, nil
}

// RevokeFeed revokes a feed at at: its URL is refused at the next fetch.
// Only its holder's own; false for none.
func RevokeFeed(ctx context.Context, h Host, holder, id string, at time.Time) (bool, error) {
	grants, err := grantsOf(h)
	if err != nil {
		return false, err
	}
	grant, ok, err := feedGrant(ctx, grants, id)
	if err != nil || !ok || !sameSubject(grant.Holder, holder) || grant.RevokedAt != nil {
		return false, err
	}
	if err := grants.Revoke(ctx, id, at); err != nil {
		return false, err
	}
	return true, nil
}

// RotateFeed rotates a feed: revokes it and issues another of the same
// scope, role and expiry, writing as ctx is. Only its holder's own; false
// for none.
func RotateFeed(ctx context.Context, h Host, holder, id string, now time.Time) (IssuedCalendarFeed, bool, error) {
	grants, err := grantsOf(h)
	if err != nil {
		return IssuedCalendarFeed{}, false, err
	}
	grant, ok, err := feedGrant(ctx, grants, id)
	if err != nil || !ok || !sameSubject(grant.Holder, holder) || grant.RevokedAt != nil {
		return IssuedCalendarFeed{}, false, err
	}
	if err := grants.Revoke(ctx, id, now); err != nil {
		return IssuedCalendarFeed{}, false, err
	}
	input := IssueCalendarFeed{Holder: holder, Role: grant.Role, Scope: FeedScopeOf(grant.TargetType), StartsAt: now}
	if grant.EndsAt != nil {
		input.EndsAt = *grant.EndsAt
	}
	issued, err := IssueFeed(ctx, h, input)
	return issued, err == nil, err
}

// FeedOf is the caller and scope a feed's token reads as (D152), or the
// reason it is refused: no such feed, or one that was revoked or has
// expired. The caller is the holder, a user, with the permissions of the
// grant's role, and no grants of its own, so the holder's audiences hold.
func FeedOf(ctx context.Context, h Host, token string, now time.Time) (Caller, CalendarFeedScope, string, error) {
	if !strings.HasPrefix(token, access.GrantTokenPrefix) {
		return Caller{}, CalendarFeedScope{}, "no such feed", nil
	}
	grants, err := grantsOf(h)
	if err != nil {
		return Caller{}, CalendarFeedScope{}, "", err
	}
	grant, ok, err := grants.ByToken(ctx, token)
	if err != nil {
		return Caller{}, CalendarFeedScope{}, "", err
	}
	if !ok || !IsCalendarFeed(grant.TargetType) {
		return Caller{}, CalendarFeedScope{}, "no such feed", nil
	}
	ended, err := grants.Ended(ctx, grant, now)
	if err != nil {
		return Caller{}, CalendarFeedScope{}, "", err
	}
	if ended != "" {
		if ended == access.GrantNotStarted {
			return Caller{}, CalendarFeedScope{}, "the feed has not started", nil
		}
		return Caller{}, CalendarFeedScope{}, "the feed has ended", nil
	}
	permissions, err := grants.RolePermissions(ctx, grant.Role)
	if err != nil {
		return Caller{}, CalendarFeedScope{}, "", err
	}
	caller := Caller{
		Principal:   access.Principal{Subject: grant.Holder, Kind: access.KindUser, Credential: &access.Credential{Kind: access.CredentialGrant, ID: grant.ID}},
		Permissions: append([]string{}, permissions...),
	}
	return caller, FeedScopeOf(grant.TargetType), "", nil
}

// CalendarReference is a short reference to an entry, for a busy feed:
// eight characters of base32 of the SHA-256 of its source, key and
// occurrence.
func CalendarReference(entry CalendarEntry) string {
	occurrence := ""
	if entry.Occurrence != nil {
		occurrence = entry.Occurrence.Key
	}
	digest := sha256.Sum256([]byte(entry.Source + "\x00" + entry.Key + "\x00" + occurrence))
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 8)
	for i := range out {
		out[i] = alphabet[digest[i]%32]
	}
	return string(out)
}

func icsText(value string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`).Replace(value)
}

// fold folds a content line at 75 octets, as RFC 5545 (3.1) has it.
func fold(line string) string {
	if len(line) <= 75 {
		return line
	}
	var out []string
	var current strings.Builder
	for _, r := range line {
		limit := 74
		if len(out) == 0 {
			limit = 75
		}
		if current.Len()+utf8.RuneLen(r) > limit {
			out = append(out, current.String())
			current.Reset()
		}
		current.WriteRune(r)
	}
	out = append(out, current.String())
	return strings.Join(out, "\r\n ")
}

func icsInstant(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		if t, err = time.Parse("2006-01-02", value); err != nil {
			return value
		}
	}
	return t.UTC().Format("20060102T150405Z")
}

// CalendarICS is the entries as an iCalendar document (RFC 5545): one
// VEVENT each, an all-day entry a date, its summary its title, or "Busy"
// and its reference for a busy feed, which carries nothing else of the
// row. now stamps each event; name is the calendar's, "Calendar" when
// empty.
func CalendarICS(entries []CalendarEntry, detail CalendarDetail, now time.Time, name string) string {
	if name == "" {
		name = "Calendar"
	}
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//superschematic//calendar//EN", "CALSCALE:GREGORIAN", "X-WR-CALNAME:" + icsText(name)}
	stamp := now.UTC().Format("20060102T150405Z")
	for _, entry := range entries {
		reference := CalendarReference(entry)
		uid := entry.Start
		if entry.Occurrence != nil {
			uid = entry.Occurrence.Key
		}
		lines = append(lines, "BEGIN:VEVENT", "UID:"+reference+"-"+icsText(uid)+"@superschematic", "DTSTAMP:"+stamp)
		if entry.AllDay {
			lines = append(lines, "DTSTART;VALUE=DATE:"+strings.ReplaceAll(entry.Start, "-", ""))
			if entry.End != "" {
				lines = append(lines, "DTEND;VALUE=DATE:"+strings.ReplaceAll(entry.End, "-", ""))
			}
		} else {
			lines = append(lines, "DTSTART:"+icsInstant(entry.Start))
			if entry.End != "" {
				lines = append(lines, "DTEND:"+icsInstant(entry.End))
			}
		}
		if detail == DetailFull {
			lines = append(lines, "SUMMARY:"+icsText(entry.Title), "CATEGORIES:"+icsText(entry.Kind))
			if entry.Occurrence != nil {
				status := "CONFIRMED"
				if entry.Occurrence.State == "skipped" {
					status = "CANCELLED"
				}
				lines = append(lines, "STATUS:"+status)
			}
		} else {
			lines = append(lines, "SUMMARY:Busy ("+reference+")", "TRANSP:OPAQUE")
		}
		lines = append(lines, "END:VEVENT")
	}
	lines = append(lines, "END:VCALENDAR")
	for i, line := range lines {
		lines[i] = fold(line)
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}
