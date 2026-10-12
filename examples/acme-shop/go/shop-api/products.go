package shopapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	api "example.com/acme/api/shop-api"
	orm "example.com/acme/orm/shop-db"
	types "example.com/acme/types/go/shop-api"
	db "example.com/acme/types/go/shop-db"
	"github.com/parable-work/superschematic/runtime/http/go/bucket"
)

// Products implements the product namespace of shop-api, which the
// ProductQueries and ProductMutations operation sets share. Media is
// shop-media, where the products' images go.
type Products struct {
	DB    orm.DatabaseInterface
	Media bucket.Bucket
}

// imageUploadExpiry is how long a product image's upload URL lives.
const imageUploadExpiry = 15 * time.Minute

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

// CreateProductImageUpload names a new object in shop-media for the
// product's image, records the name on the product, and signs a URL that
// lets a browser PUT the image there directly, with the content type it
// asked for, for imageUploadExpiry. The image's bytes never pass through
// the server.
func (p *Products) CreateProductImageUpload(ctx context.Context, id types.IdentityUUID, input *types.ProductImageUploadInput) (*types.ProductImageUpload, error) {
	products := p.DB.GetProductRepository()
	if _, err := products.GetOne(ctx, id, nil); errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("product", err)
	} else if err != nil {
		return nil, err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return nil, err
	}
	object := fmt.Sprintf("products/%s/image-%s", id, hex.EncodeToString(suffix))
	expires := time.Now().Add(imageUploadExpiry)
	url, err := p.Media.SignedURL(ctx, object, bucket.SignedURLOptions{
		Method:      bucket.MethodPut,
		Expires:     imageUploadExpiry,
		ContentType: input.ContentType,
	})
	if err != nil {
		return nil, err
	}
	if _, err := products.UpdateOne(ctx, id, &orm.ProductUpdate{ImageObject: &object}); err != nil {
		return nil, err
	}
	return &types.ProductImageUpload{ObjectName: object, UploadUrl: url, ExpiresAt: types.TemporalDateTime(expires)}, nil
}

// productView copies the columns a caller may see from a Product row. Both
// types hold the same superscalar scalars, so no field needs converting.
func productView(row *db.Product) types.ProductView {
	view := types.ProductView{
		Sku:         row.Sku,
		Name:        row.Name,
		PriceCents:  row.PriceCents,
		InStock:     row.InStock,
		ImageObject: row.ImageObject,
	}
	if row.Id != nil {
		view.Id = *row.Id
	}
	return view
}
