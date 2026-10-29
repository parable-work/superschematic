package sqlutil

import "fmt"

// A query that returns one row, or one or none (D95), is refused when its
// text shows it may return more: its top-level statement groups its rows,
// is a UNION or a VALUES list of several rows, or reads a set-returning
// function, in its select list or in its FROM, where an aggregate over the
// function's rows makes one row again. A top-level LIMIT 1 holds it to one
// row whatever else it writes. Nothing else is read: a table's rows, a
// subquery's and a join's may be many or one, which only the data tells.

// querySetReturning are Postgres's set-returning functions a query reads
// rows from, by name.
var querySetReturning = map[string]bool{
	"generate_series": true, "generate_subscripts": true, "unnest": true,
	"regexp_matches": true, "regexp_split_to_table": true, "string_to_table": true,
	"json_array_elements": true, "json_array_elements_text": true, "jsonb_array_elements": true, "jsonb_array_elements_text": true,
	"json_each": true, "json_each_text": true, "jsonb_each": true, "jsonb_each_text": true,
	"json_object_keys": true, "jsonb_object_keys": true,
	"json_populate_recordset": true, "jsonb_populate_recordset": true, "json_to_recordset": true, "jsonb_to_recordset": true,
	"jsonb_path_query": true,
}

// queryAggregates are Postgres's aggregate functions, by name: a select
// list that calls one, not as a window function, with no GROUP BY, makes
// one row.
var queryAggregates = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	"bool_and": true, "bool_or": true, "every": true, "any_value": true,
	"array_agg": true, "string_agg": true, "json_agg": true, "jsonb_agg": true, "json_object_agg": true, "jsonb_object_agg": true,
	"bit_and": true, "bit_or": true, "bit_xor": true, "range_agg": true, "xmlagg": true,
	"stddev": true, "stddev_pop": true, "stddev_samp": true, "variance": true, "var_pop": true, "var_samp": true,
	"corr": true, "covar_pop": true, "covar_samp": true,
	"percentile_cont": true, "percentile_disc": true, "mode": true,
}

// queryClauseEnds are the words that end a top-level FROM clause.
var queryClauseEnds = map[string]bool{
	"WHERE": true, "GROUP": true, "HAVING": true, "WINDOW": true, "ORDER": true, "LIMIT": true, "OFFSET": true,
	"FETCH": true, "FOR": true, "UNION": true, "INTERSECT": true, "EXCEPT": true, "RETURNING": true,
}

// queryManyRows says why the statement of words may return more than one
// row, as far as its text tells, or "".
func queryManyRows(words []queryWord) string {
	top := func(i int, upper string) bool {
		return i >= 0 && i < len(words) && words[i].depth == 0 && words[i].upper == upper
	}
	main := -1
	for i, w := range words {
		if w.depth != 0 {
			continue
		}
		switch {
		case w.upper == "LIMIT" && top(i+1, "1") && (i+2 == len(words) || top(i+2, "OFFSET") || top(i+2, "FOR")):
			return ""
		case w.upper == "FETCH" && (top(i+1, "FIRST") || top(i+1, "NEXT")):
			j := i + 2
			if top(j, "1") {
				j++
			}
			if top(j, "ROW") || top(j, "ROWS") {
				if top(j+1, "ONLY") {
					return ""
				}
			}
		case (w.upper == "SELECT" || w.upper == "VALUES") && main < 0:
			main = i
		}
	}
	for i, w := range words {
		if w.depth != 0 {
			continue
		}
		switch {
		case w.upper == "UNION":
			return "is a UNION, which returns the rows of each of its branches"
		case w.upper == "GROUP" && top(i+1, "BY"):
			return "groups its rows with GROUP BY, which returns a row per group"
		}
	}
	if main < 0 {
		return ""
	}
	if words[main].upper == "VALUES" {
		rows := 1
		for i := main + 1; i < len(words) && !queryClauseEnds[words[i].upper]; i++ {
			if words[i].depth == 0 && words[i].upper == "," {
				rows++
			}
		}
		if rows > 1 {
			return fmt.Sprintf("is a VALUES list of %d rows", rows)
		}
		return ""
	}
	from := len(words)
	for i := main + 1; i < len(words); i++ {
		if words[i].depth == 0 && words[i].upper == "FROM" {
			from = i
			break
		}
	}
	aggregates := false
	for i := main + 1; i < from; i++ {
		switch {
		case isSubquery(words, i):
			i = closing(words, i)
		case calls(words, i) && querySetReturning[words[i].raw]:
			return fmt.Sprintf("calls the set-returning function %s in its select list, which returns a row per value", words[i].raw)
		case calls(words, i) && queryAggregates[words[i].raw] && !windowed(words, closing(words, i+1)):
			aggregates = true
		}
	}
	for i := from + 1; i < len(words); i++ {
		w := words[i]
		if w.depth != 0 {
			continue
		}
		if queryClauseEnds[w.upper] {
			break
		}
		if w.upper == "ROWS" && top(i+1, "FROM") || calls(words, i) && querySetReturning[w.raw] {
			if aggregates {
				return ""
			}
			name := w.raw
			if w.upper == "ROWS" {
				name = "ROWS FROM"
			}
			return fmt.Sprintf("reads the set-returning function %s in its FROM, which returns a row per value", name)
		}
	}
	return ""
}

// calls reports whether words[i] names a function its next word calls.
func calls(words []queryWord, i int) bool {
	return words[i].raw != "" && i+1 < len(words) && words[i+1].upper == "("
}

// isSubquery reports whether words[i] opens a parenthesized SELECT, VALUES
// or WITH.
func isSubquery(words []queryWord, i int) bool {
	if words[i].upper != "(" || i+1 >= len(words) {
		return false
	}
	switch words[i+1].upper {
	case "SELECT", "VALUES", "WITH":
		return true
	}
	return false
}

// closing is the index of the ")" that closes the "(" at words[open], or
// the last word when none does.
func closing(words []queryWord, open int) int {
	if open >= len(words) {
		return len(words) - 1
	}
	depth := words[open].depth
	for i := open + 1; i < len(words); i++ {
		if words[i].upper == ")" && words[i].depth == depth {
			return i
		}
	}
	return len(words) - 1
}

// windowed reports whether the call that ends at words[end] is a window
// function: OVER follows it, past a WITHIN GROUP (...) and a FILTER (...).
func windowed(words []queryWord, end int) bool {
	i := end + 1
	if i+2 < len(words) && words[i].upper == "WITHIN" && words[i+1].upper == "GROUP" && words[i+2].upper == "(" {
		i = closing(words, i+2) + 1
	}
	if i+1 < len(words) && words[i].upper == "FILTER" && words[i+1].upper == "(" {
		i = closing(words, i+1) + 1
	}
	return i < len(words) && words[i].upper == "OVER"
}
