package shoporders

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	orm "example.com/acme/orm/shop-db"
	types "example.com/acme/types/go/shop-orders"
	"github.com/jackc/pgx/v5"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
)

// The paged lists of shop-orders (D60) read a page in two steps, in one
// transaction: the keys of up to page.Limit+1 rows after the cursor's, in
// the list's order, a comparison of two columns that the ORM's filters
// cannot say, then, through the ORM, the rows of the keys paging.FromRows
// keeps, with their columns and an order's lines as GetOrder reads them,
// put in the keys' order. A page's cursor is the key of its last row.

// rowKey is a row's place in a list's order: the columns the list orders
// by, the row's id last, which makes the order total.
type rowKey interface {
	rowID() types.IdentityUUID
}

// keyQuery is the query of a page's keys: its conditions and the values
// they take, each value's placeholder numbered as it is added.
type keyQuery struct {
	conditions []string
	args       []any
}

// where adds a condition, with a placeholder in place of each %s for each
// value in turn.
func (q *keyQuery) where(condition string, values ...any) {
	placeholders := make([]any, len(values))
	for i, value := range values {
		q.args = append(q.args, value)
		placeholders[i] = "$" + strconv.Itoa(len(q.args))
	}
	q.conditions = append(q.conditions, fmt.Sprintf(condition, placeholders...))
}

// decodeKey reads a cursor back into the key of a row. DecodeCursor
// refuses null and an empty object, but an object without an id decodes,
// its id zero, so a key without one, which names no row, is refused here
// as a cursor that does not decode is: 400 at cursor.
func decodeKey[K rowKey](cursor string) (K, error) {
	var key K
	if err := paging.DecodeCursor(cursor, &key); err != nil {
		return key, err
	}
	if key.rowID().IsZero() {
		return key, paging.RefusedCursor()
	}
	return key, nil
}

// readPage reads a page of table's rows that meet the query's conditions,
// in the order of the key columns, which K's fields hold in turn: the keys
// of up to page.Limit+1 rows, then, with read, the rows of the ids the
// page keeps, which inKeyOrder puts in the keys' order.
func readPage[K rowKey, T any](
	ctx context.Context,
	db orm.DatabaseInterface,
	query *keyQuery,
	table, key string,
	page paging.Request,
	read func(tx orm.TxInterface, ids []types.IdentityUUID) ([]T, error),
) (paging.Page[T], error) {
	sql := "SELECT " + key + " FROM " + table
	if len(query.conditions) > 0 {
		sql += " WHERE " + strings.Join(query.conditions, " AND ")
	}
	sql += " ORDER BY " + key + " LIMIT " + strconv.Itoa(max(page.Limit, 1)+1)
	var listed paging.Page[T]
	err := db.Transaction(ctx, func(tx orm.TxInterface) error {
		rows, err := tx.Query(ctx, sql, query.args...)
		if err != nil {
			return err
		}
		found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[K])
		if err != nil {
			return err
		}
		keys, err := paging.FromRows(found, page.Limit, func(key K) any { return key })
		if err != nil || len(keys.Items) == 0 {
			return err
		}
		ids := make([]types.IdentityUUID, 0, len(keys.Items))
		for _, key := range keys.Items {
			ids = append(ids, key.rowID())
		}
		listed.Items, err = read(tx, ids)
		listed.Next = keys.Next
		return err
	})
	return listed, err
}

// inKeyOrder lists the rows found by their ids in the order of ids, a
// page's, each as view makes it, leaving out an id no row has.
func inKeyOrder[R, V any](ids []types.IdentityUUID, found map[types.IdentityUUID]R, view func(R) V) []V {
	views := make([]V, 0, len(ids))
	for _, id := range ids {
		if row, ok := found[id]; ok {
			views = append(views, view(row))
		}
	}
	return views
}
