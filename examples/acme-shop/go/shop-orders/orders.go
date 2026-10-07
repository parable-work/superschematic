package shoporders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	"github.com/jackc/pgx/v5/pgconn"
	scalars "github.com/parable-work/superscalar/go"
)

// Orders implements the order namespace of shop-orders, which the
// OrderQueries and OrderMutations operation sets share.
type Orders struct {
	DB orm.DatabaseInterface
}

var _ api.OrderImplementation = (*Orders)(nil)

// withLines reads an order and its lines in one call.
func withLines() *orm.OrderGetOptions {
	lines := orm.OrderLineFields{}.All()
	fields := orm.OrderFields{}.All()
	fields.LinesNested = &lines
	return &orm.OrderGetOptions{Fields: fields}
}

func (o *Orders) PlaceOrder(ctx context.Context, input *types.PlaceOrderInput) (*types.OrderView, error) {
	customer, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var placed *db.Order
	err = o.DB.Transaction(ctx, func(tx orm.TxInterface) error {
		order, err := tx.GetOrderRepository().CreateOne(ctx, &db.Order{
			Customer:        db.User{Id: &customer},
			PlacedAt:        db.TemporalDateTime(time.Now()),
			ShippingAddress: input.ShippingAddress,
		})
		if err != nil {
			return err
		}
		for _, item := range input.Lines {
			product, err := tx.GetProductRepository().GetOne(ctx, item.ProductId, nil)
			if errors.Is(err, orm.ErrNotFound) {
				return api.BadRequestError(fmt.Sprintf("no product has id %s", item.ProductId), err)
			}
			if err != nil {
				return err
			}
			line, err := tx.GetOrderLineRepository().CreateOne(ctx, &db.OrderLine{
				Order:          db.Order{Id: order.Id},
				Product:        db.Product{Id: product.Id},
				Quantity:       db.GenericInt64(item.Quantity),
				UnitPriceCents: product.PriceCents,
			})
			if err != nil {
				return err
			}
			order.Lines = append(order.Lines, *line)
		}
		placed = order
		return nil
	})
	if err != nil {
		return nil, err
	}
	view := orderView(placed)
	return &view, nil
}

func (o *Orders) GetOrder(ctx context.Context, id types.IdentityUUID) (*types.OrderView, error) {
	order, err := o.DB.GetOrderRepository().GetOne(ctx, id, withLines())
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("order", err)
	}
	if err != nil {
		return nil, err
	}
	view := orderView(order)
	return &view, nil
}

func (o *Orders) ListOrders(ctx context.Context, statuses []types.OrderStatus, limit *float64) ([]types.OrderView, error) {
	filter := &orm.OrderFilter{}
	if len(statuses) > 0 {
		in := make([]string, 0, len(statuses))
		for _, status := range statuses {
			in = append(in, string(status))
		}
		filter.Status = &orm.StringFilter{In: in}
	}
	options := &orm.OrderFindOptions{Fields: withLines().Fields, Limit: 20}
	if limit != nil {
		options.Limit = int(*limit)
	}
	orders, _, err := o.DB.GetOrderRepository().FindMany(ctx, filter, options)
	if err != nil {
		return nil, err
	}
	views := make([]types.OrderView, 0, len(orders))
	for _, order := range orders {
		views = append(views, orderView(order))
	}
	return views, nil
}

func (o *Orders) CancelOrder(ctx context.Context, id types.IdentityUUID, reason string) (*types.OrderView, error) {
	order, err := o.DB.GetOrderRepository().GetOne(ctx, id, nil)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("order", err)
	}
	if err != nil {
		return nil, err
	}
	if order.Status != db.OrderStatus_Placed {
		return nil, api.ConflictError(fmt.Sprintf("order %s is %s", id, order.Status), nil)
	}
	cancelled := db.OrderStatus_Cancelled
	update := &orm.OrderUpdate{Status: &cancelled}
	if reason != "" {
		update.CancelReason = &reason
	}
	if _, err := o.DB.GetOrderRepository().UpdateOne(ctx, id, update); err != nil {
		return nil, err
	}
	return o.GetOrder(ctx, id)
}

// orderView copies an order and its lines into the view a caller sees, and
// computes the fields the view declares @virtual.
func orderView(order *db.Order) types.OrderView {
	view := types.OrderView{
		Status:          order.Status,
		PlacedAt:        order.PlacedAt,
		ShippingAddress: order.ShippingAddress,
		CancelReason:    order.CancelReason,
		Lines:           make([]types.OrderLineView, 0, len(order.Lines)),
	}
	if order.Id != nil {
		view.Id = *order.Id
	}
	for _, line := range order.Lines {
		lineView := types.OrderLineView{Quantity: line.Quantity, UnitPriceCents: line.UnitPriceCents}
		if line.Id != nil {
			lineView.Id = *line.Id
		}
		if line.Product.Id != nil {
			lineView.ProductId = *line.Product.Id
		}
		view.Lines = append(view.Lines, lineView)
		view.TotalCents += line.Quantity * line.UnitPriceCents
	}
	return view
}

// Reviews implements the product-reviews namespace of shop-orders.
type Reviews struct {
	DB orm.DatabaseInterface
}

var _ api.ProductReviewsImplementation = (*Reviews)(nil)

func (r *Reviews) ListReviews(ctx context.Context, productID types.IdentityUUID, minRating *float64) ([]types.ReviewView, error) {
	filter := &orm.ReviewFilter{ProductID: &orm.UUIDFilter{Eq: &productID}}
	if minRating != nil {
		filter.Rating = &orm.FloatFilter{Gte: minRating}
	}
	// Deleted reviews are left out unless the options ask for them.
	reviews, _, err := r.DB.GetReviewRepository().FindMany(ctx, filter, &orm.ReviewFindOptions{
		Fields: orm.ReviewFields{}.All(),
	})
	if err != nil {
		return nil, err
	}
	views := make([]types.ReviewView, 0, len(reviews))
	for _, review := range reviews {
		views = append(views, reviewView(review))
	}
	return views, nil
}

func (r *Reviews) WriteReview(ctx context.Context, productID types.IdentityUUID, input *types.WriteReviewInput) (*types.ReviewView, error) {
	author, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	review, err := r.DB.GetReviewRepository().CreateOne(ctx, &db.Review{
		Product: db.Product{Id: &productID},
		Author:  db.User{Id: &author},
		Rating:  input.Rating,
		Title:   input.Title,
		Body:    input.Body,
	})
	// The one_per_author unique index refuses a second review of a product.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, api.ConflictError("you have already reviewed this product", err)
	}
	if err != nil {
		return nil, err
	}
	view := reviewView(review)
	return &view, nil
}

func reviewView(review *db.Review) types.ReviewView {
	view := types.ReviewView{
		Rating:    review.Rating,
		Title:     review.Title,
		Body:      review.Body,
		CreatedAt: review.CreatedAt,
	}
	if review.Id != nil {
		view.Id = *review.Id
	}
	return view
}

// caller is the signed-in user the auth middleware put on the context.
func caller(ctx context.Context) (db.IdentityUUID, error) {
	id, err := scalars.ParseUUID(api.GetPrincipalID(ctx))
	if err != nil {
		return db.IdentityUUID{}, api.UnauthorizedError(err)
	}
	return id, nil
}

// actingUser tells the ORM who is making the request, so a soft delete
// records deletedBy. The generated server authenticates the caller; the ORM
// reads the user from its own context key.
func actingUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if id, err := scalars.ParseUUID(api.GetPrincipalID(ctx)); err == nil {
			ctx = orm.WithUserID(ctx, id)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
