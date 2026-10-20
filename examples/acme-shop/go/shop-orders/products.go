package shoporders

import (
	"context"
	"errors"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
)

// Products implements the product-catalog namespace of shop-orders: the
// catalog a shopper orders from, read from shop-db's product table, which
// shop-api's staff write.
type Products struct {
	DB orm.DatabaseInterface
}

var _ api.ProductCatalogImplementation = (*Products)(nil)

// ListProducts answers a page of the products in the page's sort, by name
// or by price: those with the given ids when there are any, the lookup a
// page that holds products' ids names them through (D61), and those whose
// name holds each word of q, the search a product picker finds them by
// (D61, amended).
func (p *Products) ListProducts(ctx context.Context, ids []types.IdentityUUID, q *string, page paging.Request) (paging.Page[types.ProductView], error) {
	filter := &orm.ProductFilter{SearchText: q}
	if len(ids) > 0 {
		filter.Id = &orm.UUIDFilter{In: ids}
	}
	sort, err := orm.ParseProductSort(page.Sort.String())
	if err != nil {
		return paging.Page[types.ProductView]{}, err
	}
	after, err := pageKey[orm.ProductPageKey](page)
	if err != nil {
		return paging.Page[types.ProductView]{}, err
	}
	found, err := p.DB.GetProductRepository().FindPage(ctx, filter, &orm.ProductPageOptions{
		Fields: orm.ProductFields{}.All(),
		Sort:   sort,
		After:  after,
		Limit:  page.Limit,
	})
	if err != nil {
		return paging.Page[types.ProductView]{}, pageError(err)
	}
	views := make([]types.ProductView, 0, len(found.Rows))
	for _, product := range found.Rows {
		views = append(views, productView(product))
	}
	return pageOf(views, found.Next)
}

// GetProduct answers the product with the id, or 404.
func (p *Products) GetProduct(ctx context.Context, productID types.IdentityUUID) (*types.ProductView, error) {
	product, err := p.DB.GetProductRepository().GetOne(ctx, productID, nil)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("product", err)
	}
	if err != nil {
		return nil, err
	}
	view := productView(product)
	return &view, nil
}

func productView(product *db.Product) types.ProductView {
	view := types.ProductView{
		Sku:        product.Sku,
		Name:       product.Name,
		PriceCents: product.PriceCents,
		InStock:    product.InStock,
	}
	if product.Id != nil {
		view.Id = *product.Id
	}
	return view
}
