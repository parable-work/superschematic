package crosstable

import (
	"context"
	"fmt"
	"strings"
)

// searchByVector is a search by vector alone (#740), the TypeScript
// runtime's searchByVector: every index row conditions keep whose
// embedding is of model and of vector's length, by cosine similarity,
// best first, then table and key; one page of them and how many there
// are, each matched by its title. args are the values conditions bind.
func searchByVector(ctx context.Context, h Host, all map[string]Searchable, index string, conditions []string, args []any, vector []float64, model string, limit, offset int) (SearchResult, error) {
	param := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	v := param(vector)
	conditions = append(conditions, "si.embedding IS NOT NULL", "si.embedding_model = "+param(model)+"::text", "cardinality(si.embedding) = cardinality("+v+"::float8[])")
	sql := `WITH hits AS (
      SELECT si.source_table, si.source_key, si.title,
        (SELECT sum(a * b) FROM unnest(si.embedding, ` + v + `::float8[]) AS t(a, b)) AS similarity
      FROM ` + index + ` AS si
      WHERE ` + strings.Join(conditions, " AND ") + `
    ),
    ranked AS (
      SELECT h.*, row_number() OVER (ORDER BY h.similarity DESC, h.source_table, h.source_key) AS place FROM hits AS h
    )
    SELECT c.total, p.source_table, p.source_key, p.title, p.place
    FROM (SELECT count(*) AS total FROM hits) AS c
    LEFT JOIN (SELECT * FROM ranked ORDER BY place LIMIT ` + fmt.Sprint(limit) + ` OFFSET ` + fmt.Sprint(offset) + `) AS p ON TRUE
    ORDER BY p.place`
	rows, err := h.Query(h.System(ctx), sql, args...)
	if err != nil {
		return SearchResult{Items: []SearchHit{}}, err
	}
	result := SearchResult{Items: []SearchHit{}, Reranked: true}
	if len(rows) > 0 {
		result.Total = int(integer(rows[0], "total"))
	}
	for _, row := range rows {
		if row["source_table"] == nil {
			continue
		}
		table := text(row, "source_table")
		title := text(row, "title")
		snippet := []SnippetPart{}
		if title != "" {
			snippet = append(snippet, SnippetPart{Text: title})
		}
		result.Items = append(result.Items, SearchHit{
			Table:   table,
			Key:     wireKey(all[table].UUID, text(row, "source_key")),
			Title:   title,
			Matched: "title",
			Snippet: snippet,
			Place:   offset + len(result.Items) + 1,
		})
	}
	result.More = offset+len(result.Items) < result.Total
	return result, nil
}
