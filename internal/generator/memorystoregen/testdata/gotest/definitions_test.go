package shopcache_test

// This test is copied into shop-cache's generated Go definitions module by
// TestGoDefinitionsRunOnTheInProcessStore, which builds the fixture with
// the checkout's runtime modules, and runs there against the runtime's
// in-process store (D75).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	shopcache "example.com/schemas/types/go/shop-cache"
	common "example.com/schemas/types/go/shop-cache-common"
	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/memorystore"
)

const productID = "3f2a9c1e-7b4d-4e8a-9c2f-1d5e6a7b8c9d"

func uuid(t *testing.T) scalars.IdentityUUID {
	t.Helper()
	id, err := scalars.ParseUUID(productID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestDefinitions: each definition builds its declaration's name from its
// typed parts, and its channel or key works on the in-process store.
func TestDefinitions(t *testing.T) {
	ctx := context.Background()
	store := memorystore.NewInProcess(shopcache.Store)
	defer store.Close()
	id := uuid(t)

	changed, err := shopcache.StockChanged.For(id)
	if err != nil {
		t.Fatal(err)
	}
	// A UUID part is its wire form, the compact base62 string its JSON
	// holds.
	wire := id.String()
	if len(wire) == 36 {
		t.Fatalf("the UUID's wire form is %q, not its compact one", wire)
	}
	if changed.Name() != "stock-changed:"+wire || changed.Declaration() != shopcache.StockChanged.Name() {
		t.Errorf("StockChanged = %q of %q", changed.Name(), changed.Declaration())
	}
	woke := make(chan struct{}, 1)
	sub, err := changed.Subscribe(ctx, store, func(context.Context, memorystore.Signal) { woke <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := changed.Publish(ctx, store, memorystore.Signal{}); err != nil {
		t.Fatal(err)
	}
	wait(t, woke)

	samples, err := shopcache.StockSamples.For(id)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan common.StockLevel, 1)
	sampleSub, err := samples.Subscribe(ctx, store, func(_ context.Context, m common.StockLevel) { got <- m })
	if err != nil {
		t.Fatal(err)
	}
	defer sampleSub.Close()
	if err := samples.Publish(ctx, store, common.StockLevel{ProductId: id, OnHand: 7, Bins: []common.StockBin{{Bin: "a", Count: 7}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		if m.OnHand != 7 || len(m.Bins) != 1 || m.ProductId != id {
			t.Errorf("sample = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no sample")
	}

	receipt, err := shopcache.ReceiptSent.For(id)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TTL() != shopcache.ReceiptSent.TTL() || receipt.TTL() != 10*time.Minute {
		t.Errorf("TTL = %s", receipt.TTL())
	}
	first, _ := receipt.Claim(ctx, store)
	second, _ := receipt.Claim(ctx, store)
	if !first || second {
		t.Errorf("claims = %v, %v", first, second)
	}

	views, err := shopcache.ProductViews.ForTTL(time.Hour, id, 20261025, common.Region_North, true, "a label")
	if err != nil {
		t.Fatal(err)
	}
	if want := "views:" + wire + ":20261025:north:true:a label"; views.Name() != want {
		t.Errorf("ProductViews = %q, want %q", views.Name(), want)
	}
	if n, err := views.Increment(ctx, store); err != nil || n != 1 {
		t.Errorf("Increment = %d, %v", n, err)
	}
	if _, err := shopcache.ProductViews.ForTTL(25*time.Hour, id, 1, common.Region_South, false, "x"); err == nil {
		t.Error("a TTL above the maximum was taken")
	}
	if _, err := shopcache.ProductViews.ForTTL(time.Hour, id, 1, common.Region_South, false, "a:b"); err == nil || !strings.Contains(err.Error(), "':'") {
		t.Errorf("a part with a colon: %v", err)
	}
	if shopcache.ProductViews.MaxTTL() != 24*time.Hour {
		t.Errorf("MaxTTL = %s", shopcache.ProductViews.MaxTTL())
	}

	disabled := memorystore.Disabled(shopcache.Store)
	if _, err := views.Increment(ctx, disabled); !errors.Is(err, memorystore.ErrUnavailable) {
		t.Errorf("Increment on a disabled store = %v", err)
	}
}

func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
}
