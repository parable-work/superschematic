package tsgen

import (
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// ormEnvironmentSearch is EnvironmentSearchDefinition
// (runtime/http/typescript/src/search-environment.ts, #740): the
// environment's search index a search database holds, which orm.ts
// exports as environmentSearch.
type ormEnvironmentSearch struct {
	// Index is the index table, quoted, and Document the expression of a
	// row's weighed words over it as searchIndexAlias.
	Index    string `json:"index"`
	Document string `json:"document"`
	// Config is the text search configuration its words are in, when it
	// is not simple: search_unaccent, which folds diacritics.
	Config string `json:"config,omitempty"`
}

// environmentSearchOf writes the schema's EnvironmentSearchDefinition, as
// JSON, or "" for a database that holds no environment index.
func environmentSearchOf(schema *ir.Schema, tables []sqlutil.TableModel) (string, error) {
	td := schema.EnvironmentSearchIndex()
	if td == nil {
		return "", nil
	}
	var index *sqlutil.TableModel
	for i := range tables {
		if tables[i].Type == td.Name {
			index = &tables[i]
		}
	}
	if index == nil {
		return "", fmt.Errorf("tsgen: the environment's search index %s has no table", td.Name)
	}
	config := ir.SearchTextConfig
	if td.SearchFolds {
		config = ir.SearchFoldedConfig
	}
	out := ormEnvironmentSearch{Index: index.Quoted, Document: ir.SearchDocumentIn(config, searchIndexAlias)}
	if config != ir.SearchTextConfig {
		out.Config = config
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
