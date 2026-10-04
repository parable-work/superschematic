package sqlmigrate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRename(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  Rename
	}{
		{"purchase=order", Rename{From: "purchase", To: "order"}},
		{"order.total=order.amount", Rename{From: "order.total", To: "order.amount"}},
		// A column of a renamed table names the table in each version.
		{"purchase.total=order.amount", Rename{From: "purchase.total", To: "order.amount"}},
	} {
		got, err := ParseRename(tc.value)
		require.NoError(t, err, tc.value)
		assert.Equal(t, tc.want, got, tc.value)
	}
}

func TestParseRenameRefuses(t *testing.T) {
	for _, value := range []string{
		"",
		"order",
		"order=",
		"=order",
		"a=b=c",
		"order.total=amount",
		"order=order.amount",
		"a.b.c=a.b.d",
		".total=order.amount",
		"order.=order.amount",
		"order .total=order.amount",
	} {
		_, err := ParseRename(value)
		require.Error(t, err, "%q", value)
		assert.Contains(t, err.Error(), "old=new for a table (purchase=order)", value)
		assert.Contains(t, err.Error(), "oldTable.oldColumn=newTable.newColumn for a column (order.total=order.amount)", value)
	}
}

func TestParseRenameRefusesItself(t *testing.T) {
	_, err := ParseRename("order.total=order.total")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renames order.total to itself")
}
