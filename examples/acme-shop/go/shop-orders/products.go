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

// productKey is a product's place in ListProducts' order: its name, then
// its id.
type productKey struct {
	Name string             `json:"name"`
	ID   types.IdentityUUID `json:"id"`
}

func (key productKey) rowID() types.IdentityUUID { return key.ID }

// ListProducts answers a page of the products by name, those with the
// given ids when there are any: the lookup a page that holds products' ids
// names them through (D61).
func (p *Products) ListProducts(ctx context.Context, ids []types.IdentityUUID, page paging.Request) (paging.Page[types.ProductView], error) {
	query := &keyQuery{}
	if len(ids) > 0 {
		in := make([]string, 0, len(ids))
		for _, id := range ids {
			in = append(in, id.ToUUID().String())
		}
		query.where("id = ANY(%s::uuid[])", in)
	}
	if page.Cursor != "" {
		after, err := decodeKey[productKey](page.Cursor)
		if err != nil {
			return paging.Page[types.ProductView]{}, err
		}
		query.where(`("name", id) > (%s, %s::uuid)`, after.Name, after.ID.ToUUID())
	}
	return readPage[productKey](ctx, p.DB, query, "product", `"name", id`, page,
		func(tx orm.TxInterface, ids []types.IdentityUUID) ([]types.ProductView, error) {
			products, err := tx.GetProductRepository().GetManyByIDs(ctx, ids)
			if err != nil {
				return nil, err
			}
			return inKeyOrder(ids, products, productView), nil
		})
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
