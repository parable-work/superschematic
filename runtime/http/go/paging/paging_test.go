package paging

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
)

// orderKey is an implementation's key: an order's position, by when it was
// placed and its id.
type orderKey struct {
	PlacedAt time.Time `json:"placedAt"`
	ID       string    `json:"id"`
}

type order struct {
	ID       string
	PlacedAt time.Time
}

// A page an implementation builds from the rows it read: limit+1 rows give
// a next page whose cursor reads back as the last item's key, and the
// next request starts after it.
func TestFromRowsAndBack(t *testing.T) {
	at := time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)
	rows := []order{{"a", at}, {"b", at.Add(time.Minute)}, {"c", at.Add(2 * time.Minute)}}
	key := func(o order) any { return orderKey{PlacedAt: o.PlacedAt, ID: o.ID} }

	page, err := FromRows(rows, 2, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Next == nil {
		t.Fatalf("page = %+v", page)
	}
	var back orderKey
	if err := DecodeCursor(*page.Next, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != "b" || !back.PlacedAt.Equal(rows[1].PlacedAt) {
		t.Fatalf("cursor reads back as %+v", back)
	}
	// The kept items do not share capacity with the row past them.
	page.Items = append(page.Items, order{ID: "x"})
	if rows[2].ID != "c" {
		t.Fatal("appending to the page's items wrote over the rows")
	}

	last, err := FromRows(rows, 3, key)
	if err != nil || len(last.Items) != 3 || last.Next != nil {
		t.Fatalf("last page = %+v, %v", last, err)
	}
	one, err := FromRows(rows, 0, key)
	if err != nil || len(one.Items) != 1 || one.Next == nil {
		t.Fatalf("a limit of 0 = %+v, %v", one, err)
	}
}

// A page writes as {items, next}: nil items as [], no next as null.
func TestPageJSON(t *testing.T) {
	for _, tc := range []struct {
		page any
		want string
	}{
		{Last[string](nil), `{"items":[],"next":null}`},
		{Last([]string{"a"}), `{"items":["a"],"next":null}`},
		{More([]int{1, 2}, "Mg"), `{"items":[1,2],"next":"Mg"}`},
		{Page[int]{}, `{"items":[],"next":null}`},
	} {
		data, err := json.Marshal(tc.page)
		if err != nil || string(data) != tc.want {
			t.Errorf("json = %s, %v, want %s", data, err, tc.want)
		}
	}
	var page Page[int]
	if err := json.Unmarshal([]byte(`{"items":[1],"next":"MQ"}`), &page); err != nil || len(page.Items) != 1 || page.Next == nil || *page.Next != "MQ" {
		t.Fatalf("decoded page = %+v, %v", page, err)
	}
}

// Read writes a refusal as the route's 400 problem, with the request's id,
// and DecodeCursor's refusal is an app error the router writes the same
// way; a key that is not a pointer is the implementation's own error.
func TestReadWritesTheProblemAndDecodeRefuses(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/items?limit=0", nil)
	request = request.WithContext(context.WithValue(request.Context(), chimiddleware.RequestIDKey, "req-1"))
	if _, ok := Read(recorder, request, Spec{DefaultLimit: 20, MaxLimit: 100}); ok {
		t.Fatal("Read accepted limit=0")
	}
	if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d, content type %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	var problem map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem["requestId"] != "req-1" || problem["code"] != "bad_request" || problem["title"] != "Bad Request" || problem["type"] != "about:blank" {
		t.Fatalf("problem = %v", problem)
	}

	err := DecodeCursor("e30=", &orderKey{})
	var appErr *apperror.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus() != http.StatusBadRequest || appErr.Message != RefusedCursorDetail {
		t.Fatalf("DecodeCursor = %v", err)
	}
	// A cursor that decodes into the key's JSON but not its type is refused
	// too: the time is not one.
	forged, err := EncodeCursor(map[string]any{"placedAt": "yesterday"})
	if err != nil {
		t.Fatal(err)
	}
	if err := DecodeCursor(forged, &orderKey{}); !errors.As(err, &appErr) {
		t.Fatalf("a key of the wrong type = %v", err)
	}
	if err := DecodeCursor("e30", orderKey{}); err == nil || errors.As(err, &appErr) || !strings.Contains(err.Error(), "paging: decode a cursor") {
		t.Fatalf("a key that is not a pointer = %v", err)
	}
}

// A key whose cursor no router reads back fails at encode time, the
// implementation's error, not a next page every reader refuses: past
// CursorMaxLength, null, an empty object. FromRows returns the error.
func TestEncodeRefusesAKeyNoRouterReads(t *testing.T) {
	long := strings.Repeat("x", 767)
	for _, key := range []any{long, nil, map[string]any{}, struct{}{}, json.RawMessage(`"\ud800"`), json.RawMessage(`1e400`)} {
		if cursor, err := EncodeCursor(key); err == nil || errors.As(err, new(*apperror.AppError)) {
			t.Errorf("EncodeCursor(%v) = %q, %v", key, cursor, err)
		}
	}
	if _, err := EncodeCursor(long[1:]); err != nil {
		t.Fatalf("a key of 768 bytes: %v", err)
	}
	page, err := FromRows([]string{"a", long, "c"}, 2, func(row string) any { return row })
	if err == nil || !strings.Contains(err.Error(), "its cursor is 1026 characters, past the 1024 a router reads") {
		t.Fatalf("FromRows = %+v, %v", page, err)
	}
}

// DecodeCursor refuses JSON null and an empty object, which would leave a
// struct key zero, and reads an escaped surrogate pair as its character.
func TestDecodeRefusesAKeyThatNamesNoPosition(t *testing.T) {
	var appErr *apperror.AppError
	for _, text := range []string{`null`, `{}`, "\t{ \n}"} {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(text))
		if err := DecodeCursor(cursor, &orderKey{}); !errors.As(err, &appErr) || appErr.Message != RefusedCursorDetail {
			t.Errorf("%q: DecodeCursor = %v", text, err)
		}
	}
	var key map[string]string
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"a😀":"😀b"}`))
	if err := DecodeCursor(cursor, &key); err != nil || key["a😀"] != "😀b" {
		t.Fatalf("an escaped surrogate pair = %v, %v", key, err)
	}
}

// A sorted operation's page (D60, amended): the router reads the sort with
// the page, binds the next cursor the implementation returns to it, and
// unbinds the cursor the caller sends back, so the implementation reads
// its own key. A cursor of another sort is the 400 at cursor.
func TestBindAndUnbind(t *testing.T) {
	spec := Spec{DefaultLimit: 2, MaxLimit: 5, Sort: &SortSpec{Param: "sort", Fields: []string{"placedAt", "totalCents"}, Default: Sort{Field: "placedAt"}}}
	if got := spec.Sort.Values(); strings.Join(got, " ") != "placedAt -placedAt totalCents -totalCents" {
		t.Fatalf("Values = %v", got)
	}
	for text, want := range map[string]Sort{"placedAt": {Field: "placedAt"}, "-totalCents": {Field: "totalCents", Desc: true}} {
		if got, ok := ParseSort(text); !ok || got != want || got.String() != text {
			t.Errorf("ParseSort(%q) = %+v, %v", text, got, ok)
		}
	}
	for _, text := range []string{"", "-", "--placedAt", "+placedAt", "placedAt,id", "2nd", "placed At"} {
		if got, ok := ParseSort(text); ok {
			t.Errorf("ParseSort(%q) = %+v", text, got)
		}
	}

	first, refusal := Parse(QueryValues("sort=-totalCents"), spec)
	if refusal != nil || first != (Request{Limit: 2, Sort: Sort{Field: "totalCents", Desc: true}}) {
		t.Fatalf("first request = %+v, %v", first, refusal)
	}
	at := time.Date(2026, 10, 9, 14, 30, 0, 0, time.UTC)
	rows := []order{{"a", at}, {"b", at.Add(time.Minute)}, {"c", at.Add(2 * time.Minute)}}
	page, err := FromRows(rows, first.Limit, func(o order) any { return orderKey{PlacedAt: o.PlacedAt, ID: o.ID} })
	if err != nil {
		t.Fatal(err)
	}
	keyCursor := *page.Next
	bound, err := Bind(page, first)
	if err != nil || bound.Next == nil || len(bound.Items) != 2 {
		t.Fatalf("Bind = %+v, %v", bound, err)
	}
	data, err := base64.RawURLEncoding.DecodeString(*bound.Next)
	if err != nil || !strings.HasPrefix(string(data), `{"sort":"-totalCents","key":{"placedAt":"2026-10-09T14:31:00Z","id":"b"}}`) {
		t.Fatalf("bound next = %s, %v", data, err)
	}
	if *page.Next != keyCursor {
		t.Fatal("Bind wrote over the implementation's page")
	}

	// The next request reads the implementation's own cursor back.
	second, refusal := Parse(QueryValues("sort=-totalCents&cursor="+*bound.Next), spec)
	if refusal != nil || second.Cursor != keyCursor || second.Sort != first.Sort {
		t.Fatalf("second request = %+v, %v", second, refusal)
	}
	var key orderKey
	if err := DecodeCursor(second.Cursor, &key); err != nil || key.ID != "b" {
		t.Fatalf("the unbound cursor reads %+v, %v", key, err)
	}

	// Under another sort, the default among them, it is refused.
	var appErr *apperror.AppError
	for _, query := range []string{"sort=totalCents&cursor=" + *bound.Next, "cursor=" + *bound.Next} {
		_, refusal := Parse(QueryValues(query), spec)
		if !errors.As(error(refusal), &appErr) || appErr.Message != SortedCursorDetail || appErr.Details.(Details).Reason != SortedCursorReason {
			t.Errorf("%s: refusal = %v", query, refusal)
		}
	}
	// The implementation's own cursor names no sort.
	if _, refusal := Parse(QueryValues("cursor="+keyCursor), spec); refusal == nil || refusal.Message != RefusedCursorDetail {
		t.Errorf("an unbound cursor: refusal = %v", refusal)
	}

	// A page with no next, and a request with no sort, pass as they are.
	last := Last([]int{1})
	if got, err := Bind(last, first); err != nil || got.Next != nil {
		t.Errorf("Bind of the last page = %+v, %v", got, err)
	}
	unsorted := More([]int{1}, "MQ")
	if got, err := Bind(unsorted, Request{Limit: 1}); err != nil || *got.Next != "MQ" {
		t.Errorf("Bind without a sort = %+v, %v", got, err)
	}
	// A next no router would read back under its sort is the
	// implementation's error.
	long, err := EncodeCursor(strings.Repeat("x", 740))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Bind(More([]int{1}, long), first); err == nil || errors.As(err, &appErr) || !strings.Contains(err.Error(), "past the 1024 a router reads") {
		t.Errorf("Bind of a long key = %+v, %v", got, err)
	}
	if got, err := Bind(More([]int{1}, "e30="), first); err == nil || errors.As(err, &appErr) {
		t.Errorf("Bind of a cursor EncodeCursor does not write = %+v, %v", got, err)
	}
}
