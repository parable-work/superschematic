package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// A describe document asks a Postgres what it makes of a DB service's
// declared queries (`superschematic-migrate describe`, D86):
// `superschematic queries verify` writes the service's create.sql and each
// statement its queries run, the runner applies the DDL to a database of
// its own, prepares each statement there and prints what Postgres
// described, and the compiler compares that with the types the schema
// declares. The runner holds the Postgres driver, which stays out of the
// compiler's module (D27, Apply), and judges nothing itself.

// DescribeVersion is the version of the describe document and of the
// result the describe command prints.
const DescribeVersion = 1

// DescribeDocument is a describe document.
type DescribeDocument struct {
	Version int `json:"version"`
	// DDL is the service's create.sql, applied to an empty database.
	DDL string `json:"ddl"`
	// Statements are the statements to prepare, each with an id the
	// result gives back.
	Statements []DescribeStatement `json:"statements"`
}

// DescribeStatement is one statement of a describe document.
type DescribeStatement struct {
	ID  string `json:"id"`
	SQL string `json:"sql"`
}

// DescribeResult is what the describe command prints: each statement's
// description, in the document's order.
type DescribeResult struct {
	Version    int                    `json:"version"`
	Statements []StatementDescription `json:"statements"`
}

// StatementDescription is what Postgres described of a statement it
// prepared, or the error it refused it with.
type StatementDescription struct {
	ID string `json:"id"`
	// Error is Postgres's message, with its position, when it refused
	// the statement; nothing else is set then.
	Error   string            `json:"error,omitempty"`
	Params  []DescribedType   `json:"params,omitempty"`
	Columns []DescribedColumn `json:"columns,omitempty"`
}

// DescribedType is a type as Postgres names it: Type its format_type,
// "timestamp with time zone", Name its pg_type name, timestamptz, and
// Element the name of each element's type of an array, "" otherwise.
type DescribedType struct {
	Type    string `json:"type"`
	Name    string `json:"typeName"`
	Element string `json:"element,omitempty"`
}

// DescribedColumn is a result column: its name and type, and, when it
// comes straight from a table's column, that table and column and whether
// the column is NOT NULL.
type DescribedColumn struct {
	Name string `json:"name"`
	DescribedType
	Table   string `json:"table,omitempty"`
	Column  string `json:"column,omitempty"`
	NotNull bool   `json:"notNull,omitempty"`
}

// ReadDescribeDocument decodes a describe document, refusing an unknown
// key, another version, and a statement without an id or SQL.
func ReadDescribeDocument(data []byte) (*DescribeDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var doc DescribeDocument
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("not a describe document: %w", err)
	}
	if doc.Version != DescribeVersion {
		return nil, fmt.Errorf("describe document version %d; this runner reads version %d", doc.Version, DescribeVersion)
	}
	for i, st := range doc.Statements {
		if st.ID == "" || st.SQL == "" {
			return nil, fmt.Errorf("describe document: statement %d needs an id and its sql", i)
		}
	}
	return &doc, nil
}
