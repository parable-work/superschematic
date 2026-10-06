package shopapi

import (
	"context"
	"errors"

	api "example.com/acme/api/shop-api"
	orm "example.com/acme/orm/shop-db"
	types "example.com/acme/types/go/shop-api"
	db "example.com/acme/types/go/shop-db"
)

// Products implements the product namespace of shop-api, which the
// ProductQueries and ProductMutations operation sets share.
type Products struct {
	DB orm.DatabaseInterface
}

var _ api.ProductImplementation = (*Products)(nil)

func (p *Products) GetProduct(ctx context.Context, id types.IdentityUUID) (*types.ProductView, error) {
	row, err := p.DB.GetProductRepository().GetOne(ctx, id, nil)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("product", err)
	}
	if err != nil {
		return nil, err
	}
	view := productView(row)
	return &view, nil
}

func (p *Products) ListProducts(ctx context.Context, inStock bool) ([]types.ProductView, error) {
	var filter *orm.ProductFilter
	if inStock {
		filter = &orm.ProductFilter{InStock: &orm.BoolFilter{Eq: &inStock}}
	}
	rows, _, err := p.DB.GetProductRepository().FindMany(ctx, filter, &orm.ProductFindOptions{
		Fields: orm.ProductFields{}.All(),
	})
	if err != nil {
		return nil, err
	}
	views := make([]types.ProductView, 0, len(rows))
	for _, row := range rows {
		views = append(views, productView(row))
	}
	return views, nil
}

func (p *Products) CreateProduct(ctx context.Context, input *types.CreateProductInput) (*types.ProductView, error) {
	row, err := p.DB.GetProductRepository().CreateOne(ctx, &db.Product{
		Sku:        input.Sku,
		Name:       input.Name,
		PriceCents: input.PriceCents,
		InStock:    true,
	})
	if err != nil {
		return nil, err
	}
	view := productView(row)
	return &view, nil
}

// productView copies the columns a caller may see from a Product row. Both
// types hold the same superscalar scalars, so no field needs converting.
func productView(row *db.Product) types.ProductView {
	view := types.ProductView{
		Sku:        row.Sku,
		Name:       row.Name,
		PriceCents: row.PriceCents,
		InStock:    row.InStock,
	}
	if row.Id != nil {
		view.Id = *row.Id
	}
	return view
}
