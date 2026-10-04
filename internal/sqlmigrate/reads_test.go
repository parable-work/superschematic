package sqlmigrate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ir "github.com/parable-work/superschematic/ir"
)

// readsModel is shop-db with an order table, its history table and a
// customer table. A relation field's foreign-key column carries the
// relation field's origin; a column the generator adds carries none.
func readsModel() *Model {
	return &Model{
		Version: ModelVersion,
		Dialect: Postgres,
		Service: "shop-db",
		Tables: []*Table{
			{Name: "customer", Kind: TableEntity, Origin: "Customer", Columns: []*Column{
				{Name: "id", Origin: "Customer.id", Type: "UUID"},
				{Name: "name", Origin: "Customer.name", Type: "TEXT"},
			}},
			{Name: "order", Kind: TableEntity, Origin: "Order", History: "order_history", Columns: []*Column{
				{Name: "id", Origin: "Order.id", Type: "UUID"},
				{Name: "total", Origin: "Order.total", Type: "BIGINT"},
				{Name: "customer_id", Origin: "Order.customer", Type: "UUID"},
				{Name: "_version", Type: "BIGINT"},
			}},
			{Name: "order_history", Kind: TableHistory, Origin: "Order", Columns: []*Column{
				{Name: "id", Type: "UUID"},
				{Name: "total", Type: "BIGINT"},
			}},
		},
	}
}

func readsConsumer() *ir.Schema {
	schema := ir.NewSchema("shop-api", ir.SchemaKindAPI)
	schema.Types["OrderView"] = &ir.TypeDef{
		Name:   "OrderView",
		Source: &ir.SourceRef{Target: "shop-db.Order", Virtual: []string{"label"}},
		Fields: []*ir.FieldDef{
			{Name: "id"},
			{Name: "total"},
			// A relation field reads its foreign-key column.
			{Name: "customer"},
			// A list relation has no column.
			{Name: "lines"},
			{Name: "lineCount", Virtual: true},
			// Listed as virtual on the SourceRef, as a data-form schema
			// whose target is only declared in its imports records it.
			{Name: "label"},
		},
	}
	schema.Types["OrderTotal"] = &ir.TypeDef{
		Name:   "OrderTotal",
		Source: &ir.SourceRef{Target: "shop-db.Order"},
		Fields: []*ir.FieldDef{{Name: "total"}},
	}
	// Another database's Order, a type of shop-db that is not a table, and
	// a local projection read nothing from shop-db.
	schema.Types["LegacyOrder"] = &ir.TypeDef{
		Name:   "LegacyOrder",
		Source: &ir.SourceRef{Target: "legacy-db.Order"},
		Fields: []*ir.FieldDef{{Name: "total"}},
	}
	schema.Types["AuditView"] = &ir.TypeDef{
		Name:   "AuditView",
		Source: &ir.SourceRef{Target: "shop-db.Auditable"},
		Fields: []*ir.FieldDef{{Name: "createdAt"}},
	}
	schema.Types["OrderSummary"] = &ir.TypeDef{
		Name:   "OrderSummary",
		Source: &ir.SourceRef{Target: "OrderView"},
		Fields: []*ir.FieldDef{{Name: "total"}},
	}
	schema.Types["Plain"] = &ir.TypeDef{
		Name:   "Plain",
		Fields: []*ir.FieldDef{{Name: "total"}},
	}
	return schema
}

func TestSourceReads(t *testing.T) {
	reads, err := SourceReads(readsConsumer(), "shop-db", readsModel())
	require.NoError(t, err)
	assert.Equal(t, []Read{
		{Reader: "shop-api", Via: "OrderView.customer", Table: "order", Column: "customer_id"},
		{Reader: "shop-api", Via: "OrderView.id", Table: "order", Column: "id"},
		{Reader: "shop-api", Via: "OrderTotal.total", Table: "order", Column: "total"},
		{Reader: "shop-api", Via: "OrderView.total", Table: "order", Column: "total"},
	}, reads)
}

func TestSourceReadsRefusesAnotherServicesModel(t *testing.T) {
	_, err := SourceReads(readsConsumer(), "legacy-db", readsModel())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reads of legacy-db: the model is shop-db's")
}

func TestSourceReadsEmptyDatabase(t *testing.T) {
	reads, err := SourceReads(readsConsumer(), "shop-db", nil)
	require.NoError(t, err)
	assert.Empty(t, reads)
}

func TestMergeReads(t *testing.T) {
	api := []Read{
		{Reader: "shop-api", Via: "OrderView.total", Table: "order", Column: "total"},
		{Reader: "shop-api", Via: "OrderView.id", Table: "order", Column: "id"},
	}
	admin := []Read{
		{Reader: "admin-api", Via: "OrderRow.total", Table: "order", Column: "total"},
		{Reader: "shop-api", Via: "OrderView.total", Table: "order", Column: "total"},
	}
	assert.Equal(t, []Read{
		{Reader: "shop-api", Via: "OrderView.id", Table: "order", Column: "id"},
		{Reader: "admin-api", Via: "OrderRow.total", Table: "order", Column: "total"},
		{Reader: "shop-api", Via: "OrderView.total", Table: "order", Column: "total"},
	}, MergeReads(api, admin))
	assert.Empty(t, MergeReads())
}
