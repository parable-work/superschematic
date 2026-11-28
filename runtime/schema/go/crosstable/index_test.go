package crosstable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// indexHost is a Host over a table of notes held in memory, which answers
// the statements indexRows and ReindexSearch run and records them: the
// batching itself, without a database. TestGoSearchEndToEnd runs them on
// Postgres.
type indexHost struct {
	notes      map[string]Row // by key: title, text, deleted
	comments   map[string]string
	statements []string
	upserts    [][]any
	updates    [][]any
	deletes    [][]string
}

var noteIndex = Searchable{
	Index: "search_index", Table: "note", Key: "id", KeyType: "text", Title: "title", Body: []string{"text"}, DeletedAt: "deleted_at",
	Comments: &SearchComments{Table: "note_comments", Row: "note_id", Body: "body", State: "state", Order: "message"},
}

var limitOf = regexp.MustCompile(`LIMIT (\d+)$`)

func (h *indexHost) Query(_ context.Context, sql string, args ...any) ([]Row, error) {
	h.statements = append(h.statements, sql)
	switch {
	case strings.HasPrefix(sql, "SELECT id::text AS key FROM note"):
		keys := slices.Sorted(maps.Keys(h.notes))
		if len(args) == 1 {
			keys = slices.DeleteFunc(keys, func(k string) bool { return k <= args[0].(string) })
		}
		limit, _ := strconv.Atoi(limitOf.FindStringSubmatch(sql)[1])
		var out []Row
		for _, k := range keys[:min(limit, len(keys))] {
			out = append(out, Row{"key": k})
		}
		return out, nil
	case strings.HasPrefix(sql, "SELECT given.key AS given"):
		var out []Row
		for _, k := range args[0].([]string) {
			if note, ok := h.notes[k]; ok {
				out = append(out, Row{"given": k, "key": k, "title": note["title"], "b0": note["text"], "deleted": note["deleted"] == true})
			}
		}
		return out, nil
	case strings.Contains(sql, "string_agg(body"):
		var out []Row
		for _, k := range args[0].([]string) {
			if c, ok := h.comments[k]; ok {
				out = append(out, Row{"key": k, "text": c})
			}
		}
		return out, nil
	case strings.HasPrefix(sql, "INSERT INTO search_index"):
		h.upserts = append(h.upserts, args)
		var out []Row
		for _, k := range args[1].([]string) {
			out = append(out, Row{"source_key": k, "embedding_model": nil})
		}
		return out, nil
	}
	return nil, fmt.Errorf("unexpected statement %q", sql)
}

func (h *indexHost) Exec(_ context.Context, sql string, args ...any) (int64, error) {
	h.statements = append(h.statements, sql)
	switch {
	case strings.HasPrefix(sql, "DELETE FROM search_index"):
		h.deletes = append(h.deletes, args[1].([]string))
	case strings.HasPrefix(sql, "UPDATE search_index"):
		h.updates = append(h.updates, args)
	default:
		return 0, fmt.Errorf("unexpected statement %q", sql)
	}
	return 0, nil
}

func (h *indexHost) Transaction(_ context.Context, fn func(Host) error) error { return fn(h) }
func (h *indexHost) As(ctx context.Context, _ access.Principal) context.Context {
	return ctx
}
func (h *indexHost) System(ctx context.Context) context.Context { return ctx }
func (h *indexHost) Conditions(context.Context, string, *[]any, int) []string {
	return nil
}
func (h *indexHost) Definitions() *Definitions {
	return &Definitions{Searchable: map[string]Searchable{"Note": noteIndex}}
}
func (h *indexHost) Grants() Grants { return nil }

func hashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func TestReindexSearchBatches(t *testing.T) {
	h := &indexHost{notes: map[string]Row{}, comments: map[string]string{"n1": "First comment"}}
	for i := 1; i <= 7; i++ {
		h.notes[fmt.Sprintf("n%d", i)] = Row{"title": fmt.Sprintf("Note %d", i), "text": fmt.Sprintf("Text %d", i), "deleted": i == 5}
	}
	var calls [][]string
	embeddings := &Embeddings{Model: "m", Embedder: EmbedderFunc(func(_ context.Context, texts []string) ([][]float64, error) {
		calls = append(calls, texts)
		out := make([][]float64, len(texts))
		for i := range texts {
			out[i] = []float64{3, 4}
		}
		return out, nil
	})}
	n, err := ReindexSearch(context.Background(), h, ReindexOptions{SearchOptions: SearchOptions{Embeddings: embeddings}, Batch: 3})
	if err != nil || n != 7 {
		t.Fatalf("reindexed %d, %v; want 7", n, err)
	}
	// Three pages: one model call each, with every text of the page that
	// was indexed, the soft-deleted note's index row deleted instead.
	if len(calls) != 3 || len(calls[0]) != 3 || len(calls[1]) != 2 || len(calls[2]) != 1 {
		t.Fatalf("model calls %q, want one a page", calls)
	}
	if want := "Note 1\nText 1\nFirst comment"; calls[0][0] != want {
		t.Errorf("first text %q, want %q", calls[0][0], want)
	}
	if len(h.deletes) != 1 || !slices.Equal(h.deletes[0], []string{"n5"}) {
		t.Errorf("deleted %q", h.deletes)
	}
	// Each page runs the same statements whatever its size: its keys, its
	// rows, their conversations, the delete of a page with a row gone,
	// the upsert and the embeddings' update.
	if len(h.statements) != 3*5+1 {
		t.Errorf("%d statements:\n%s", len(h.statements), strings.Join(h.statements, "\n"))
	}
	if len(h.upserts) != 3 {
		t.Fatalf("%d upserts, want one a page", len(h.upserts))
	}
	first := h.upserts[0]
	if keys := first[1].([]string); !slices.Equal(keys, []string{"n1", "n2", "n3"}) {
		t.Errorf("first page's keys %q", keys)
	}
	if hashes := first[7].([]string); hashes[0] != hashOf("Note 1\nText 1\nFirst comment") || hashes[1] != hashOf("Note 2\nText 2") {
		t.Errorf("first page's hashes %q", hashes)
	}
	if comments := first[4].([]*string); *comments[0] != "First comment" || comments[1] != nil {
		t.Errorf("first page's comments %v", comments)
	}
	if len(h.updates) != 3 {
		t.Fatalf("%d embedding updates, want one a page", len(h.updates))
	}
	if update := h.updates[1]; !slices.Equal(update[2].([]string), []string{"n4", "n6"}) || !slices.Equal(update[4].([]string), []string{"{0.6,0.8}", "{0.6,0.8}"}) {
		t.Errorf("second page's update %v", update)
	}
}

func TestIndexSearchIsOneKey(t *testing.T) {
	h := &indexHost{notes: map[string]Row{"n1": {"title": "Note 1", "text": nil}}}
	var calls int
	options := SearchOptions{Embeddings: &Embeddings{Model: "m", Embedder: EmbedderFunc(func(_ context.Context, texts []string) ([][]float64, error) {
		calls++
		return [][]float64{{}}, nil
	})}}
	ctx := context.Background()
	if got, err := IndexSearch(ctx, h, SearchIndexing{SourceTable: "Note", SourceKey: "n1"}, options); err != nil || got != IndexedRow {
		t.Errorf("a note: %q, %v", got, err)
	}
	// A model that gives no embedding leaves the row unembedded.
	if calls != 1 || len(h.updates) != 0 {
		t.Errorf("%d model calls, %d updates", calls, len(h.updates))
	}
	if got, err := IndexSearch(ctx, h, SearchIndexing{SourceTable: "Note", SourceKey: "gone"}, options); err != nil || got != IndexedRemoved {
		t.Errorf("a note that is gone: %q, %v", got, err)
	}
	if got, err := IndexSearch(ctx, h, SearchIndexing{SourceTable: "Other", SourceKey: "n1"}, options); err != nil || got != IndexedUnknown {
		t.Errorf("a table that is not @searchable: %q, %v", got, err)
	}
	if len(h.deletes) != 1 || !slices.Equal(h.deletes[0], []string{"gone"}) || calls != 1 {
		t.Errorf("deleted %q, %d model calls", h.deletes, calls)
	}
}

func TestFloatArray(t *testing.T) {
	if got := floatArray([]float64{0.1, -0, 1e-7, 1.0 / 3}); got != "{0.1,0,1e-07,0.3333333333333333}" {
		t.Errorf("floatArray: %s", got)
	}
}
