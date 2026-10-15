package enginehttp_test

// The corpus's symbols (runtime/engine/testdata/scenarios/README.md,
// "Symbols"), as the Rust test kit binds them: each binds at its first
// appearance in an answer to what this engine answered there, and from
// then on is written wherever it appears and required wherever it is
// expected. On Postgres an event cursor binds too, held to the recorded
// order rather than its number.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type symbol struct {
	text   string
	kind   string
	number string
	micros bool
	base62 bool
}

func (s symbol) key() string { return s.kind + ":" + s.number }

// bound is a symbol's value: text, or an instant in epoch milliseconds.
type bound struct {
	text   string
	time   int64
	isTime bool
}

func (b bound) String() string {
	if b.isTime {
		return fmt.Sprintf("time %d", b.time)
	}
	return strconv.Quote(b.text)
}

// reads is what a run of an answer's text stands for, when it can be the
// symbol's kind.
func (s symbol) reads(candidate string) (bound, bool) {
	switch {
	case s.kind == "id" && !s.base62:
		if len(candidate) == 36 && strings.Trim(candidate, "0123456789abcdefABCDEF-") == "" {
			return bound{text: candidate}, true
		}
	case s.kind == "time" && s.micros:
		if micros, err := strconv.ParseInt(candidate, 10, 64); err == nil {
			return bound{time: micros / 1000, isTime: true}, true
		}
	case s.kind == "time" && len(candidate) == 13 && strings.Trim(candidate, "0123456789") == "":
		ms, _ := strconv.ParseInt(candidate, 10, 64)
		return bound{time: ms, isTime: true}, true
	case s.kind == "time":
		if ms, ok := msOfISO(candidate); ok {
			return bound{time: ms, isTime: true}, true
		}
	case s.kind == "opaque":
		if candidate != "" && strings.Trim(candidate, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == "" {
			return bound{text: candidate}, true
		}
	case s.kind == "dir":
		if candidate != "" {
			return bound{text: candidate}, true
		}
	}
	return bound{}, false
}

type segment struct {
	text   string
	symbol *symbol
}

// segments splits a string into its text and its symbols.
func segments(text string) []segment {
	var out []segment
	rest := text
	for {
		open := strings.Index(rest, "{{")
		if open < 0 {
			break
		}
		closeAt := strings.Index(rest[open:], "}}")
		if closeAt < 0 {
			break
		}
		inner := rest[open+2 : open+closeAt]
		parts := strings.Split(inner, ":")
		var found *symbol
		if (len(parts) == 2 || len(parts) == 3) && strings.Trim(parts[1], "0123456789") == "" && parts[1] != "" {
			switch parts[0] {
			case "id", "time", "dir", "opaque":
				form := ""
				if len(parts) == 3 {
					form = parts[2]
				}
				found = &symbol{text: inner, kind: parts[0], number: parts[1], micros: form == "us", base62: form == "62"}
			}
		}
		if found != nil {
			if open > 0 {
				out = append(out, segment{text: rest[:open]})
			}
			out = append(out, segment{symbol: found})
		} else {
			out = append(out, segment{text: rest[:open+closeAt+2]})
		}
		rest = rest[open+closeAt+2:]
	}
	if rest != "" {
		out = append(out, segment{text: rest})
	}
	return out
}

func hasSymbol(parts []segment) bool {
	for _, part := range parts {
		if part.symbol != nil {
			return true
		}
	}
	return false
}

// render is a bound value where its symbol stands: a time as a number in
// place of a whole value, as an RFC 3339 date-time inside a string.
func render(s symbol, b bound, whole bool) any {
	switch {
	case !b.isTime:
		return b.text
	case s.micros && whole:
		return number(b.time * 1000)
	case s.micros:
		return strconv.FormatInt(b.time*1000, 10)
	case whole:
		return number(b.time)
	default:
		return isoOf(b.time)
	}
}

// isoOf is an instant as JavaScript's toISOString writes it.
func isoOf(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

func msOfISO(text string) (int64, bool) {
	if len(text) != 24 {
		return 0, false
	}
	at, err := time.Parse("2006-01-02T15:04:05.000Z", text)
	if err != nil {
		return 0, false
	}
	ms := at.UnixMilli()
	return ms, isoOf(ms) == text
}

// symbols are a scenario's bindings, and on Postgres its cursors.
type symbols struct {
	bindings map[string]bound
	postgres bool
	// cursors maps each recorded event cursor to the one read in its
	// place.
	cursors map[int64]int64
	next    int
}

func newSymbols(postgres bool) *symbols {
	return &symbols{bindings: map[string]bound{}, postgres: postgres, cursors: map[int64]int64{}}
}

// substitute writes a value with its symbols' bound values; a directory
// named first is made up.
func (s *symbols) substitute(value any) (any, error) {
	switch v := value.(type) {
	case string:
		parts := segments(v)
		if len(parts) == 1 && parts[0].symbol != nil {
			return s.argument(*parts[0].symbol, true)
		}
		if !hasSymbol(parts) {
			return v, nil
		}
		var out strings.Builder
		for _, part := range parts {
			if part.symbol == nil {
				out.WriteString(part.text)
				continue
			}
			written, err := s.argument(*part.symbol, false)
			if err != nil {
				return nil, err
			}
			if str, ok := written.(string); ok {
				out.WriteString(str)
			} else {
				out.WriteString(show(written))
			}
		}
		return out.String(), nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			written, err := s.substitute(item)
			if err != nil {
				return nil, err
			}
			out[i] = written
		}
		return out, nil
	case *object:
		out := newObject()
		for _, key := range v.keys {
			written, err := s.substitute(v.members[key])
			if err != nil {
				return nil, err
			}
			out.set(key, written)
		}
		return out, nil
	default:
		return value, nil
	}
}

// latest is the latest instant a time symbol is bound to.
func (s *symbols) latest() int64 {
	var latest int64
	for _, b := range s.bindings {
		if b.isTime && b.time > latest {
			latest = b.time
		}
	}
	return latest
}

func (s *symbols) argument(sym symbol, whole bool) (any, error) {
	if _, ok := s.bindings[sym.key()]; !ok && sym.kind == "dir" {
		s.next++
		s.bindings[sym.key()] = bound{text: fmt.Sprintf("/replay/dir%d", s.next)}
	}
	b, ok := s.bindings[sym.key()]
	if !ok {
		return nil, fmt.Errorf("an argument names {{%s}} before an answer bound it", sym.text)
	}
	return render(sym, b, whole), nil
}

// isCursor is whether a member of an answer holds an event cursor.
func isCursor(o *object, key string) bool {
	has := func(name string) bool { _, ok := o.get(name); return ok }
	switch key {
	case "cursor":
		return true
	case "next":
		return has("events")
	case "head":
		return has("running")
	case "event":
		return has("depth")
	case "floor", "heldAt":
		return has("pruned")
	}
	return false
}

// matches compares an answer with the recorded one, binding each symbol
// at its first appearance.
func (s *symbols) matches(expected, actual any, at string) error {
	if str, ok := expected.(string); ok && strings.Contains(str, "{{") {
		parts := segments(str)
		if len(parts) == 1 && parts[0].symbol != nil {
			return s.matchWhole(*parts[0].symbol, actual, at)
		}
		got, ok := actual.(string)
		if !ok {
			return fmt.Errorf("%s: expected %s, got %s", at, show(expected), show(actual))
		}
		var found []foundSymbol
		if !s.matchSegments(parts, got, &found) {
			return fmt.Errorf("%s: expected %s, got %s", at, show(expected), show(actual))
		}
		for _, each := range found {
			if err := s.bind(each.symbol, each.bound, at); err != nil {
				return err
			}
		}
		return nil
	}
	switch want := expected.(type) {
	case *object:
		got, ok := actual.(*object)
		if !ok {
			return fmt.Errorf("%s: expected %s, got %s", at, show(expected), show(actual))
		}
		for _, key := range want.keys {
			if _, ok := got.get(key); !ok {
				return fmt.Errorf("%s/%s: expected %s, got nothing", at, key, show(want.members[key]))
			}
		}
		for _, key := range got.keys {
			if _, ok := want.get(key); !ok {
				return fmt.Errorf("%s/%s: expected nothing, got %s", at, key, show(got.members[key]))
			}
		}
		for _, key := range want.keys {
			path := at + "/" + key
			recorded, okRecorded := integer(want.members[key])
			read, okRead := integer(got.members[key])
			if s.postgres && okRecorded && okRead && isCursor(want, key) {
				if err := s.matchCursor(recorded, read, path); err != nil {
					return err
				}
				continue
			}
			if err := s.matches(want.members[key], got.members[key], path); err != nil {
				return err
			}
		}
		return nil
	case []any:
		got, ok := actual.([]any)
		if !ok || len(got) != len(want) {
			return fmt.Errorf("%s: expected %d items, got %s", at, len(want), show(actual))
		}
		for i := range want {
			if err := s.matches(want[i], got[i], fmt.Sprintf("%s/%d", at, i)); err != nil {
				return err
			}
		}
		return nil
	}
	if jsEqual(expected, actual) {
		return nil
	}
	return fmt.Errorf("%s: expected %s, got %s", at, show(expected), show(actual))
}

// matchCursor holds an event cursor read on Postgres to the recorded one:
// bound at its first appearance, it keeps the recorded order.
func (s *symbols) matchCursor(recorded, read int64, at string) error {
	if known, ok := s.cursors[recorded]; ok {
		if known == read {
			return nil
		}
		return fmt.Errorf("%s: expected cursor %d, bound to %d, got %d", at, recorded, known, read)
	}
	for each, boundTo := range s.cursors {
		if (each < recorded && boundTo >= read) || (each > recorded && boundTo <= read) {
			return fmt.Errorf("%s: expected cursor %d, got %d, out of the recorded order", at, recorded, read)
		}
	}
	s.cursors[recorded] = read
	return nil
}

// cursorArgument is the cursor passed on Postgres where the recording
// passed recorded: the one bound to it, else as far past the nearest one
// bound below it as the recording's.
func (s *symbols) cursorArgument(recorded int64) int64 {
	var below []int64
	for each := range s.cursors {
		if each <= recorded {
			below = append(below, each)
		}
	}
	if len(below) == 0 {
		return recorded
	}
	sort.Slice(below, func(i, j int) bool { return below[i] < below[j] })
	nearest := below[len(below)-1]
	return s.cursors[nearest] + (recorded - nearest)
}

func (s *symbols) matchWhole(sym symbol, actual any, at string) error {
	var b bound
	switch got := actual.(type) {
	case string:
		if sym.kind == "time" {
			ms, ok := msOfISO(got)
			if !ok {
				return fmt.Errorf("%s: %s is no date-time", at, got)
			}
			b = bound{time: ms, isTime: true}
		} else {
			b = bound{text: got}
		}
	default:
		read, ok := integer(actual)
		if sym.kind != "time" || !ok {
			return fmt.Errorf("%s: expected {{%s}}, got %s", at, sym.text, show(actual))
		}
		if sym.micros {
			read /= 1000
		}
		b = bound{time: read, isTime: true}
	}
	if known, ok := s.bindings[sym.key()]; ok {
		rendered := render(sym, known, true)
		if jsEqual(rendered, actual) {
			return nil
		}
		return fmt.Errorf("%s: expected {{%s}}, bound to %s, got %s", at, sym.text, show(rendered), show(actual))
	}
	return s.bind(sym, b, at)
}

func (s *symbols) bind(sym symbol, b bound, at string) error {
	if known, ok := s.bindings[sym.key()]; ok {
		if known == b {
			return nil
		}
		return fmt.Errorf("%s: {{%s}} is bound to %s, got %s", at, sym.text, known, b)
	}
	for key, other := range s.bindings {
		if strings.HasPrefix(key, sym.kind+":") && other == b {
			return fmt.Errorf("%s: {{%s}} and {{%s}} stand for one value, %s", at, sym.text, key, b)
		}
	}
	s.bindings[sym.key()] = b
	return nil
}

type foundSymbol struct {
	symbol symbol
	bound  bound
}

// matchSegments matches text and symbols against an answer's text,
// collecting what each unbound symbol stands for.
func (s *symbols) matchSegments(parts []segment, actual string, found *[]foundSymbol) bool {
	if len(parts) == 0 {
		return actual == ""
	}
	first, rest := parts[0], parts[1:]
	if first.symbol == nil {
		after, ok := strings.CutPrefix(actual, first.text)
		return ok && s.matchSegments(rest, after, found)
	}
	sym := *first.symbol
	known, ok := s.bindings[sym.key()]
	if !ok {
		for _, each := range *found {
			if each.symbol.key() == sym.key() {
				known, ok = each.bound, true
			}
		}
	}
	if ok {
		forms := []string{}
		if str, isText := render(sym, known, false).(string); isText {
			forms = append(forms, str)
		}
		// An instant in text is a date-time, or, in JSON a string carries,
		// its epoch milliseconds.
		if known.isTime && !sym.micros {
			forms = append(forms, strconv.FormatInt(known.time, 10))
		}
		for _, form := range forms {
			if after, cut := strings.CutPrefix(actual, form); cut && s.matchSegments(rest, after, found) {
				return true
			}
		}
		return false
	}
	// An unbound symbol takes the longest run its kind allows that lets
	// the rest match.
	for length := len(actual); length >= 0; length-- {
		if length < len(actual) && !utf8.RuneStart(actual[length]) {
			continue
		}
		if b, readable := sym.reads(actual[:length]); readable {
			*found = append(*found, foundSymbol{sym, b})
			if s.matchSegments(rest, actual[length:], found) {
				return true
			}
			*found = (*found)[:len(*found)-1]
		}
	}
	return false
}
