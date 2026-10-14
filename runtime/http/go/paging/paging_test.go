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
