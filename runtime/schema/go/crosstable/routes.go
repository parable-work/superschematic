package crosstable

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Authenticate establishes the caller of a request: nil for none, which a
// route answers 401. A Go app's is its auth step's: session.GetCaller of
// the request's context and the permissions of its roles, or a grant's
// holder through the ORM's AuthenticateGrant.
type Authenticate func(r *http.Request) (*Caller, error)

// CalendarRoutesOptions is what CalendarRoutes takes.
type CalendarRoutesOptions struct {
	CalendarOptions
	Host Host
	// Authenticate establishes the caller of a read and of a feed's
	// issue, rotation and revocation; a feed's fetch reads as its token's
	// holder.
	Authenticate Authenticate
	// Base is the path the routes are under; /api/calendar unless set.
	Base string
	// FeedDaysBefore and FeedDaysAfter are the window a feed serves, in
	// days before and after now; 31 and 400 unless set.
	FeedDaysBefore, FeedDaysAfter int
	// Now is the clock; time.Now unless set.
	Now func() time.Time
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}

func serverError(w http.ResponseWriter, err error) {
	var bad Invalid
	if errors.As(err, &bad) {
		problem(w, http.StatusBadRequest, "Bad Request", bad.Error())
		return
	}
	problem(w, http.StatusInternalServerError, "Internal Server Error", "the read failed")
}

// list is the values of a query parameter, each split at its commas.
func list(values []string) []string {
	var out []string
	for _, v := range values {
		for _, s := range strings.Split(v, ",") {
			if s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// CalendarRoutes is the calendar's routes (D152), a handler the app
// mounts under Base:
//
//   - GET {base}?from&to&sources&scope: the caller's calendar, as JSON
//     {data: Calendar}.
//   - GET {base}/feed/{token}.ics: the feed a token reads, as
//     text/calendar; 401 for a revoked, expired or unknown token.
//   - POST {base}/feeds {role, detail, sources?, days?}: issues the caller
//     a feed; the answer holds its URL's token once.
//   - POST {base}/feeds/{id}/rotate, DELETE {base}/feeds/{id}: the
//     caller's own feed.
func CalendarRoutes(options CalendarRoutesOptions) http.Handler {
	base := strings.TrimSuffix(options.Base, "/")
	if base == "" {
		base = "/api/calendar"
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	before, after := options.FeedDaysBefore, options.FeedDaysAfter
	if before == 0 {
		before = 31
	}
	if after == 0 {
		after = 400
	}
	h := options.Host
	issued := func(w http.ResponseWriter, status int, feed IssuedCalendarFeed) {
		writeJSON(w, status, map[string]any{"data": map[string]any{
			"id": feed.ID, "token": feed.Token, "path": base + "/feed/" + feed.Token + ".ics", "endsAt": iso(feed.EndsAt),
		}})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path != base && !strings.HasPrefix(path, base+"/") {
			problem(w, http.StatusNotFound, "Not Found", "no such calendar route")
			return
		}
		rest := strings.TrimPrefix(path, base)
		ctx := r.Context()
		if token, ok := strings.CutPrefix(rest, "/feed/"); ok && r.Method == http.MethodGet && !strings.Contains(token, "/") {
			token, _ = url.PathUnescape(strings.TrimSuffix(token, ".ics"))
			at := now()
			caller, scope, refused, err := FeedOf(ctx, h, token, at)
			if err != nil {
				serverError(w, err)
				return
			}
			if refused != "" {
				problem(w, http.StatusUnauthorized, "Unauthorized", refused)
				return
			}
			read, err := ReadCalendar(ctx, h, caller, CalendarRead{
				From:    iso(at.Add(-time.Duration(before) * 24 * time.Hour)),
				To:      iso(at.Add(time.Duration(after) * 24 * time.Hour)),
				Sources: scope.Sources,
			}, options.CalendarOptions)
			if err != nil {
				serverError(w, err)
				return
			}
			w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
			w.Header().Set("Cache-Control", "private, max-age=300")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(CalendarICS(read.Entries, scope.Detail, at, "")))
			return
		}
		caller, err := options.Authenticate(r)
		if err != nil || caller == nil {
			problem(w, http.StatusUnauthorized, "Unauthorized", "Authentication required")
			return
		}
		switch {
		case r.Method == http.MethodGet && rest == "":
			q := r.URL.Query()
			from, to := q.Get("from"), q.Get("to")
			if from == "" || to == "" {
				problem(w, http.StatusBadRequest, "Bad Request", "from and to are required")
				return
			}
			read, err := ReadCalendar(ctx, h, *caller, CalendarRead{From: from, To: to, Sources: list(q["sources"]), Mine: q.Get("scope") == "mine"}, options.CalendarOptions)
			if err != nil {
				serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": read})
		case r.Method == http.MethodPost && rest == "/feeds":
			var body struct {
				Role    *string  `json:"role"`
				Detail  string   `json:"detail"`
				Sources []string `json:"sources"`
				Days    *float64 `json:"days"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Role == nil || (body.Detail != string(DetailFull) && body.Detail != string(DetailBusy)) {
				problem(w, http.StatusBadRequest, "Bad Request", "a feed takes role, a UserRole, and detail, full or busy")
				return
			}
			days := float64(CalendarFeedDays)
			if body.Days != nil && *body.Days > 0 && *body.Days <= 3660 {
				days = *body.Days
			}
			at := now()
			feed, err := IssueFeed(h.As(ctx, caller.Principal), h, IssueCalendarFeed{
				Holder:   caller.Principal.Subject,
				Role:     *body.Role,
				Scope:    CalendarFeedScope{Detail: CalendarDetail(body.Detail), Sources: body.Sources},
				StartsAt: at,
				EndsAt:   at.Add(time.Duration(days * float64(24*time.Hour))),
			})
			if err != nil {
				serverError(w, err)
				return
			}
			issued(w, http.StatusCreated, feed)
		case strings.HasPrefix(rest, "/feeds/"):
			id, rotate := strings.CutSuffix(strings.TrimPrefix(rest, "/feeds/"), "/rotate")
			id, _ = url.PathUnescape(id)
			if id == "" || strings.Contains(id, "/") {
				problem(w, http.StatusNotFound, "Not Found", "no such calendar route")
				return
			}
			switch {
			case rotate && r.Method == http.MethodPost:
				feed, ok, err := RotateFeed(h.As(ctx, caller.Principal), h, caller.Principal.Subject, id, now())
				switch {
				case err != nil:
					serverError(w, err)
				case !ok:
					problem(w, http.StatusNotFound, "Not Found", "no such feed of yours")
				default:
					issued(w, http.StatusOK, feed)
				}
			case !rotate && r.Method == http.MethodDelete:
				ok, err := RevokeFeed(ctx, h, caller.Principal.Subject, id, now())
				switch {
				case err != nil:
					serverError(w, err)
				case !ok:
					problem(w, http.StatusNotFound, "Not Found", "no such feed of yours")
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			default:
				problem(w, http.StatusNotFound, "Not Found", "no such calendar route")
			}
		default:
			problem(w, http.StatusNotFound, "Not Found", "no such calendar route")
		}
	})
}

// CalendarMCPTool is the calendar as an MCP tool (D152): what tools/list
// answers of it.
func CalendarMCPTool() map[string]any {
	return map[string]any{
		"name":        "calendar",
		"title":       "Calendar",
		"description": "Reads the dates of every calendar source, rows and recurrence occurrences, in a window, as the caller reads them.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"from", "to"},
			"properties": map[string]any{
				"from":    map[string]any{"type": "string", "description": "The earliest start, an ISO 8601 date or instant."},
				"to":      map[string]any{"type": "string", "description": "The start every entry falls before."},
				"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only these sources, by name."},
				"scope":   map[string]any{"enum": []string{"all", "mine"}, "description": "mine: only rows the caller owns."},
			},
		},
		"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true},
	}
}

// Problem is a tool call's refusal.
type Problem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func badRequest(detail string) *Problem {
	return &Problem{Status: http.StatusBadRequest, Title: "Bad Request", Detail: detail}
}

func stringsOf(value any) ([]string, bool) {
	switch v := value.(type) {
	case string:
		return []string{v}, true
	case []string:
		return v, true
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		if out == nil {
			out = []string{}
		}
		return out, true
	}
	return nil, false
}

// CallCalendarTool runs a call of CalendarMCPTool as caller: its result,
// or its refusal.
func CallCalendarTool(ctx context.Context, h Host, caller Caller, args map[string]any, options CalendarOptions) (*Calendar, *Problem, error) {
	from, okFrom := args["from"].(string)
	to, okTo := args["to"].(string)
	if !okFrom || !okTo {
		return nil, badRequest("from and to are required"), nil
	}
	read := CalendarRead{From: from, To: to, Mine: args["scope"] == "mine"}
	if sources, ok := args["sources"].([]any); ok {
		read.Sources, _ = stringsOf(sources)
	}
	got, err := ReadCalendar(ctx, h, caller, read, options)
	var bad Invalid
	if errors.As(err, &bad) {
		return nil, badRequest(bad.Error()), nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &got, nil, nil
}

// SearchRoutesOptions is what SearchRoutes takes.
type SearchRoutesOptions struct {
	SearchOptions
	Host Host
	// Authenticate establishes the caller of a search: a session's user,
	// a key's, a grant's party or agent run.
	Authenticate Authenticate
	// Base is the route's path; /api/search unless set.
	Base string
}

// SearchRoutes is the search route (D155), a handler the app mounts at
// Base: GET {base}?q&tables&scope&limit&offset answers the caller's
// search as JSON {data: SearchResult}; 401 with no caller, 400 for a
// request it cannot read.
func SearchRoutes(options SearchRoutesOptions) http.Handler {
	base := strings.TrimSuffix(options.Base, "/")
	if base == "" {
		base = "/api/search"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != base {
			problem(w, http.StatusNotFound, "Not Found", "no such search route")
			return
		}
		if r.Method != http.MethodGet {
			problem(w, http.StatusMethodNotAllowed, "Method Not Allowed", "search is a GET")
			return
		}
		caller, err := options.Authenticate(r)
		if err != nil || caller == nil {
			problem(w, http.StatusUnauthorized, "Unauthorized", "Authentication required")
			return
		}
		q := r.URL.Query()
		request := SearchRequest{Q: q.Get("q"), Tables: list(q["tables"]), Scope: list(q["scope"])}
		for name, into := range map[string]*int{"limit": &request.Limit, "offset": &request.Offset} {
			if !q.Has(name) {
				continue
			}
			n, err := strconv.Atoi(q.Get(name))
			if err != nil || (name == "limit" && n == 0) {
				problem(w, http.StatusBadRequest, "Bad Request", "searchTables: "+name+" is a whole number, not "+q.Get(name))
				return
			}
			*into = n
		}
		result, err := SearchTables(r.Context(), options.Host, *caller, request, options.SearchOptions)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": result})
	})
}

// SearchMCPTool is search as an MCP tool (D155): what tools/list answers
// of it.
func SearchMCPTool() map[string]any {
	return map[string]any{
		"name":        "search",
		"title":       "Search",
		"description": "Finds the rows of every searchable table that hold all the given words, best first, of those the caller may read: each hit names its table and key, with its title and a snippet.",
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"q"},
			"properties": map[string]any{
				"q":      map[string]any{"type": "string", "minLength": 1, "description": "The words to find; a row matches when it holds every one."},
				"tables": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only these tables, by name."},
				"scope":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only rows in this scope, the app's filter: a workspace or a project."},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": maxLimit, "description": "At most this many hits; 20 unless set."},
				"offset": map[string]any{"type": "integer", "minimum": 0, "maximum": maxOffset, "description": "Hits to skip, for the next page."},
			},
		},
		"annotations": map[string]any{"readOnlyHint": true, "idempotentHint": true},
	}
}

// CallSearchTool runs a call of SearchMCPTool as caller: its result, or
// its refusal. An agent run calls it as the principal its run token's
// grant resolves to, so its hits keep to the rows its grants reach and its
// audience.
func CallSearchTool(ctx context.Context, h Host, caller Caller, args map[string]any, options SearchOptions) (*SearchResult, *Problem, error) {
	q, ok := args["q"].(string)
	if !ok {
		return nil, badRequest("q is required"), nil
	}
	request := SearchRequest{Q: q}
	if tables, ok := stringsOf(args["tables"]); ok {
		request.Tables = tables
	}
	if scope, ok := stringsOf(args["scope"]); ok {
		request.Scope = scope
	}
	for name, into := range map[string]*int{"limit": &request.Limit, "offset": &request.Offset} {
		switch n := args[name].(type) {
		case float64:
			if n != float64(int(n)) {
				return nil, badRequest("searchTables: " + name + " is a whole number"), nil
			}
			*into = int(n)
		case int:
			*into = n
		}
	}
	got, err := SearchTables(ctx, h, caller, request, options)
	var bad Invalid
	if errors.As(err, &bad) {
		return nil, badRequest(bad.Error()), nil
	}
	if err != nil {
		return nil, nil, err
	}
	return &got, nil, nil
}
