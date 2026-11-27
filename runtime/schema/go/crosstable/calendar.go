package crosstable

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

/*
The calendar (D152): the dates of many tables' rows, merged into one read.

A DB table's @calendarSource declarations become the generated ORM's
calendar definitions: a source dates each row by a date or date-time field
(to an end field when it spans), or each occurrence of the table's
Recurrence by its due time, and titles each entry by a field of the row.
ReadCalendar reads every source in a window, each table's rows held to the
caller by the predicate the table's repository applies for it
(Host.Conditions: its audience and its grants, D156, D180), so a row's
audience and the caller's grants hold as they hold on any read; an
occurrence is shown only when its row is read. A source a caller lacks the
permission of (CalendarOptions.Permissions) is left out.
*/

// CalendarOccurrence is an occurrence's key, its wall due and its state,
// on its entry.
type CalendarOccurrence struct {
	Key   string `json:"key"`
	Due   string `json:"due"`
	State string `json:"state"`
}

// CalendarEntry is one entry of the calendar.
type CalendarEntry struct {
	// Source is the source's name, Kind its kind and Color its colour key.
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Color  string `json:"color,omitempty"`
	// Type is the row's table type and Key its key in its wire form.
	Type  string `json:"type"`
	Key   string `json:"key"`
	Title string `json:"title"`
	// Start is when it starts: an RFC 3339 instant, or a date for an
	// all-day entry; End when it ends, in the same form.
	Start  string `json:"start"`
	End    string `json:"end,omitempty"`
	AllDay bool   `json:"allDay,omitempty"`
	// Occurrence is an occurrence's key, wall due and state.
	Occurrence *CalendarOccurrence `json:"occurrence,omitempty"`
}

// CalendarRead is what ReadCalendar reads: a window of instants or dates,
// and which sources.
type CalendarRead struct {
	// From and To are the window: an RFC 3339 instant, a date (its
	// midnight in UTC) or epoch milliseconds, as a JavaScript Date reads
	// one; an entry starts at or after From and before To.
	From, To string
	// Sources are the sources it reads, by name; every one when nil.
	Sources []string
	// Mine keeps to the rows whose source's owner field holds the caller;
	// a source with no owner field is left out.
	Mine bool
	// Limit is the most entries of a source; 500 unless set, at most 5000.
	Limit int
}

// CalendarOptions is what the calendar takes beside the database.
type CalendarOptions struct {
	// Permissions is the permission each source needs, by its name: a
	// caller without it reads none of its entries. A source not named
	// needs none beyond its table's audience and grants.
	Permissions map[string]string
}

// Calendar is a read of the calendar.
type Calendar struct {
	// Entries are in start order, ties by source and key.
	Entries []CalendarEntry `json:"entries"`
	// More reports that a source held more than the limit.
	More bool `json:"more"`
}

// instant reads a window's bound: an RFC 3339 instant, a date, or epoch
// milliseconds.
func instant(value, what string) (time.Time, error) {
	v := strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(n), nil
	}
	return time.Time{}, invalid("readCalendar: %s is no instant: %s", what, value)
}

// ReadCalendar reads the calendar as caller (D152): every source's
// entries whose start lies in [From, To), each source's rows kept to what
// caller reads of its table, so a row's audience and the caller's grants
// hold; an occurrence only when its row is read. Entries come in start
// order, ties by source and key; More says a source held more than the
// limit.
func ReadCalendar(ctx context.Context, h Host, caller Caller, read CalendarRead, options CalendarOptions) (Calendar, error) {
	from, err := instant(read.From, "from")
	if err != nil {
		return Calendar{}, err
	}
	to, err := instant(read.To, "to")
	if err != nil {
		return Calendar{}, err
	}
	limit := read.Limit
	if limit == 0 {
		limit = 500
	}
	limit = min(max(limit, 1), 5000)
	as := h.As(ctx, caller.Principal)
	out := Calendar{Entries: []CalendarEntry{}}
	defs := h.Definitions().Calendar
	for _, typeName := range sortedKeys(defs) {
		table := defs[typeName]
		for _, source := range table.Sources {
			if read.Sources != nil && !slices.Contains(read.Sources, source.Name) {
				continue
			}
			if permission, ok := options.Permissions[source.Name]; ok && !caller.holds(permission) {
				continue
			}
			if read.Mine && (source.Owner == "" || caller.Principal.Anonymous()) {
				continue
			}
			var entries []CalendarEntry
			var more bool
			if source.Occurrences != nil {
				entries, more, err = occurrencesOf(as, h, typeName, table, source, from, to, limit, read.Mine, caller)
			} else {
				entries, more, err = rowsOf(as, h, typeName, table, source, from, to, limit, read.Mine, caller)
			}
			if err != nil {
				return Calendar{}, err
			}
			out.Entries = append(out.Entries, entries...)
			out.More = out.More || more
		}
	}
	slices.SortStableFunc(out.Entries, func(a, b CalendarEntry) int {
		if c := strings.Compare(a.Start, b.Start); c != 0 {
			return c
		}
		if c := strings.Compare(a.Source, b.Source); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out, nil
}

// heldConditions are the conditions a read of table keeps to: its live
// condition, the caller's own rows for a read of Mine, and what the
// table's repository holds the caller of ctx to.
func heldConditions(ctx context.Context, h Host, typeName string, table CalendarTable, source CalendarSource, mine bool, caller Caller, args *[]any) []string {
	var out []string
	if table.Live != "" {
		out = append(out, table.Live)
	}
	if mine {
		subject := caller.Principal.Subject
		if source.OwnerUUID {
			subject = rowrefs.UUIDToDB(subject)
		}
		if subject == "" {
			out = append(out, "FALSE")
		} else {
			*args = append(*args, subject)
			cast := ""
			if source.OwnerUUID {
				cast = "::uuid"
			}
			out = append(out, fmt.Sprintf("%s = $%d%s", source.Owner, len(*args), cast))
		}
	}
	return append(out, h.Conditions(ctx, typeName, args, len(*args))...)
}

func where(conditions []string) string {
	if len(conditions) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conditions, " AND ")
}

// form is a date or date-time value as an entry's start or end.
func form(value any, timestamp bool) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", false
	case time.Time:
		if timestamp {
			return iso(v), true
		}
		return v.UTC().Format("2006-01-02"), true
	case string:
		if timestamp {
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return iso(t), true
			}
			return v, true
		}
		if len(v) > 10 {
			v = v[:10]
		}
		return v, true
	}
	return fmt.Sprint(value), true
}

// rowsOf reads a field source's rows in the window.
func rowsOf(ctx context.Context, h Host, typeName string, table CalendarTable, source CalendarSource, from, to time.Time, limit int, mine bool, caller Caller) ([]CalendarEntry, bool, error) {
	if source.Field == "" {
		return nil, false, fmt.Errorf("crosstable: %s's calendar source %s dates neither a field nor occurrences", typeName, source.Name)
	}
	var args []any
	if source.Timestamp {
		args = append(args, from, to)
	} else {
		args = append(args, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	}
	cast := ""
	if !source.Timestamp {
		cast = "::date"
	}
	conditions := []string{fmt.Sprintf("%s >= $1%s", source.Field, cast), fmt.Sprintf("%s < $2%s", source.Field, cast)}
	conditions = append(conditions, heldConditions(ctx, h, typeName, table, source, mine, caller, &args)...)
	end := "NULL"
	if source.End != "" {
		end = source.End
	}
	sql := fmt.Sprintf("SELECT %s::text AS key, %s::text AS title, %s AS start, %s AS finish FROM %s%s ORDER BY %s, %s LIMIT %d",
		table.Key, source.Title, source.Field, end, table.Table, where(conditions), source.Field, table.Key, limit+1)
	rows, err := h.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	var out []CalendarEntry
	for _, row := range rows[:min(len(rows), limit)] {
		start, ok := form(row["start"], source.Timestamp)
		if !ok {
			continue
		}
		entry := CalendarEntry{Source: source.Name, Kind: source.Kind, Color: source.Color, Type: typeName, Key: wireKey(table.UUID, text(row, "key")), Title: text(row, "title"), Start: start, AllDay: !source.Timestamp}
		if finish, ok := form(row["finish"], source.Timestamp); ok {
			entry.End = finish
		}
		out = append(out, entry)
	}
	return out, more, nil
}

// occurrencesOf reads an occurrences source's occurrences in the window,
// then the rows they belong to as the caller, and keeps the occurrences
// of the rows it read.
func occurrencesOf(ctx context.Context, h Host, typeName string, table CalendarTable, source CalendarSource, from, to time.Time, limit int, mine bool, caller Caller) ([]CalendarEntry, bool, error) {
	occ := source.Occurrences
	due := "NULL::text"
	if occ.Due != "" {
		due = occ.Due + "::text"
	}
	rows, err := h.Query(h.System(ctx), fmt.Sprintf("SELECT %s::text AS row, %s::text AS key, %s AS due, %s AS due_at, %s::text AS state FROM %s WHERE %s >= $1 AND %s < $2 ORDER BY %s, %s LIMIT %d",
		occ.Row, occ.Key, due, occ.DueAt, occ.State, occ.Table, occ.DueAt, occ.DueAt, occ.DueAt, occ.Key, limit+1), from, to)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	rows = rows[:min(len(rows), limit)]
	var keys []string
	for _, row := range rows {
		if key := text(row, "row"); !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	titles := map[string]string{}
	if len(keys) > 0 {
		args := []any{keys}
		conditions := []string{fmt.Sprintf("%s = ANY($1::%s[])", table.Key, table.KeyType)}
		conditions = append(conditions, heldConditions(ctx, h, typeName, table, source, mine, caller, &args)...)
		parents, err := h.Query(ctx, fmt.Sprintf("SELECT %s::text AS key, %s::text AS title FROM %s%s", table.Key, source.Title, table.Table, where(conditions)), args...)
		if err != nil {
			return nil, false, err
		}
		for _, parent := range parents {
			titles[text(parent, "key")] = text(parent, "title")
		}
	}
	var out []CalendarEntry
	for _, row := range rows {
		key := text(row, "row")
		title, ok := titles[key]
		if !ok {
			continue
		}
		start, _ := form(row["due_at"], true)
		out = append(out, CalendarEntry{
			Source: source.Name, Kind: source.Kind, Color: source.Color, Type: typeName,
			Key: wireKey(table.UUID, key), Title: title, Start: start,
			Occurrence: &CalendarOccurrence{Key: text(row, "key"), Due: text(row, "due"), State: text(row, "state")},
		})
	}
	return out, more, nil
}
