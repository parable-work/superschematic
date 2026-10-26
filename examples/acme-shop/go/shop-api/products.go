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
	shopcache "example.com/acme/types/go/shop-cache"
	db "example.com/acme/types/go/shop-db"
	"github.com/parable-work/superschematic/runtime/http/go/bucket"
	"github.com/parable-work/superschematic/runtime/http/go/memorystore"
	"go.uber.org/zap"
)

// Products implements the product namespace of shop-api, which the
// ProductQueries and ProductMutations operation sets share. Media is
// shop-media, where the products' images go, and Cache shop-cache, the
// memory store every replica shares (D75): it holds no fact, so each use
// of it has a path for when it is unavailable.
type Products struct {
	DB     orm.DatabaseInterface
	Media  bucket.Bucket
	Cache  memorystore.Store
	Logger *zap.Logger
}

// The waits of WaitForProductImage: the longest a caller may ask for, how
// often a waiter rereads the product while it hears the product's
// channel, since a wake-up can be lost, and how often while it cannot.
const (
	maxImageWait     = 30 * time.Second
	slowImagePoll    = 10 * time.Second
	unheardImagePoll = time.Second
)

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

// WaitForProductImage returns the product once it has an image, or as it
// is after waitSeconds, at most 30. It subscribes to the product's
// channel before it reads the product, so an image recorded between the
// two still wakes it, and reads the product again on each wake-up, which
// the replica that records the image publishes after its commit (D75). It
// rereads it every 10 seconds too, since a wake-up can be lost, and every
// second while the store is unavailable, as it would without one: a store
// that is down holds the subscription until it reconnects, so the waiter
// asks the store each time it waits.
func (p *Products) WaitForProductImage(ctx context.Context, id types.IdentityUUID, waitSeconds int64) (*types.ProductView, error) {
	wait := min(max(time.Duration(waitSeconds)*time.Second, 0), maxImageWait)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	subscribed := false
	woke := make(chan struct{}, 1)
	if changed, err := shopcache.ProductChanged.For(id); err == nil {
		sub, err := changed.Subscribe(ctx, p.Cache, func(context.Context, memorystore.Signal) {
			select {
			case woke <- struct{}{}:
			default:
			}
		})
		if err == nil {
			defer sub.Close()
			subscribed = true
		}
	}
	for {
		view, err := p.GetProduct(ctx, id)
		if err != nil || view.ImageObject != "" {
			return view, err
		}
		poll := unheardImagePoll
		if subscribed && p.Cache.Available() {
			poll = slowImagePoll
		}
		select {
		case <-woke:
		case <-time.After(poll):
		case <-deadline.C:
			return p.GetProduct(ctx, id)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
// the server. Once the name is recorded, it wakes every request waiting
// for the product's image, on any replica (D75).
//
// One upload of a product is signed at a time, on every replica: a
// request that finds another's claim on the product in shop-cache is
// refused 409, and the claim is given back once the URL is signed or the
// signing fails. While the store is unavailable no request is refused.
func (p *Products) CreateProductImageUpload(ctx context.Context, id types.IdentityUUID, input *types.ProductImageUploadInput) (*types.ProductImageUpload, error) {
	if signing, err := shopcache.ImageUploadSigning.For(id); err == nil {
		claimed, err := signing.Claim(ctx, p.Cache)
		switch {
		case err == nil && !claimed:
			return nil, api.ConflictError("an upload of the product's image is being signed", nil)
		case err == nil:
			defer func() {
				if err := signing.Delete(context.WithoutCancel(ctx), p.Cache); err != nil {
					p.logger().Debug("the image upload's claim was not given back; it expires", zap.Error(err))
				}
			}()
		}
	}
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
	p.changed(ctx, id)
	return &types.ProductImageUpload{ObjectName: object, UploadUrl: url, ExpiresAt: types.TemporalDateTime(expires)}, nil
}

// changed publishes that the product changed, after the commit: a waiter
// reads it again. A store that is unavailable loses the wake-up, and the
// waiter's next read finds the change.
func (p *Products) changed(ctx context.Context, id types.IdentityUUID) {
	ch, err := shopcache.ProductChanged.For(id)
	if err == nil {
		err = ch.Publish(ctx, p.Cache, memorystore.Signal{})
	}
	if err != nil && !errors.Is(err, memorystore.ErrUnavailable) {
		p.logger().Warn("the product's change was not published", zap.Error(err))
	}
}

func (p *Products) logger() *zap.Logger {
	if p.Logger == nil {
		return zap.NewNop()
	}
	return p.Logger
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
