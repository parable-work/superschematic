package shoporders

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	"github.com/jackc/pgx/v5/pgconn"
	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// Orders implements the order namespace of shop-orders, which the
// OrderQueries and OrderMutations operation sets share. A shopper reads,
// lists and cancels their own orders; staff, whose roles cover
// orders.any, every customer's.
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

// PlaceOrder records an order taken in one step, at the products' prices
// now, which the Topcoat app and the SDKs' tests place: it holds no stock
// and takes no payment, which a checkout does (checkouts.go).
func (o *Orders) PlaceOrder(ctx context.Context, input *types.PlaceOrderInput) (*types.OrderView, error) {
	customer, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ctx = actingUser(ctx)
	var placed *db.Order
	err = o.DB.Transaction(ctx, func(tx orm.TxInterface) error {
		// Each line's product, at its price now. The order stores its
		// total, the sum of its lines, which do not change after.
		products := make([]*db.Product, 0, len(input.Lines))
		var total db.GenericInt64
		for _, item := range input.Lines {
			product, err := tx.GetProductRepository().GetOne(ctx, item.ProductId, nil)
			if errors.Is(err, orm.ErrNotFound) {
				return api.BadRequestError(fmt.Sprintf("no product has id %s", item.ProductId), err)
			}
			if err != nil {
				return err
			}
			products = append(products, product)
			total += db.GenericInt64(item.Quantity) * product.PriceCents
		}
		order, err := tx.GetOrderRepository().CreateOne(ctx, &db.Order{
			Customer:        db.User{Id: &customer},
			PlacedAt:        db.TemporalDateTime(time.Now()),
			ShippingAddress: input.ShippingAddress,
			TotalCents:      total,
		})
		if err != nil {
			return err
		}
		for i, item := range input.Lines {
			product := products[i]
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
		// FulfilOrders, shop-orders' worker, fulfils the order. The message
		// commits with the order, or not at all (D53).
		return orm.EnqueueOrderPlaced(ctx, tx, &db.OrderPlaced{OrderId: *order.Id})
	})
	if err != nil {
		return nil, err
	}
	view := orderView(placed)
	return &view, nil
}

// everyOrder is the permission of staff, who read and cancel every
// customer's orders. A shopper holds orders.read and orders.write, and
// reads and cancels their own: orders.any is nested under orders, so a
// role that grants orders grants it too.
const everyOrder = "orders.any"

// seesEveryOrder reports whether the caller is staff: one of their roles
// covers orders.any.
func seesEveryOrder(ctx context.Context) bool {
	return session.AnyRoleCovers(api.GetRoles(ctx), everyOrder)
}

// callersOrder reads the order with id, with opts, when the caller may see
// it: their own, or any order for staff. getOrder and cancelOrder are
// @requireOwnership, which leaves the check to the implementation:
// another customer's order is the 404 of one no one placed, so an id
// tells a shopper nothing of whose it is.
func callersOrder(ctx context.Context, orders orm.OrderRepositoryInterface, id types.IdentityUUID, opts *orm.OrderGetOptions) (*db.Order, error) {
	customer, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	order, err := orders.GetOne(ctx, id, opts)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("order", err)
	}
	if err != nil {
		return nil, err
	}
	if !seesEveryOrder(ctx) && (order.Customer.Id == nil || *order.Customer.Id != customer) {
		return nil, api.NotFoundError("order", orm.ErrNotFound)
	}
	return order, nil
}

func (o *Orders) GetOrder(ctx context.Context, id types.IdentityUUID) (*types.OrderView, error) {
	order, err := callersOrder(ctx, o.DB.GetOrderRepository(), id, withLines())
	if err != nil {
		return nil, err
	}
	view := orderView(order)
	return &view, nil
}

// ListOrders answers a page of the caller's orders, or of every
// customer's for staff, in the page's sort, by when each was placed or by
// its total, with the given statuses, or of every status when there are
// none. The ORM reads the page, with each order's lines. A cursor names
// the order a page ends at, and the filter holds every page, so a
// shopper's pages hold their own orders alone.
func (o *Orders) ListOrders(ctx context.Context, statuses []types.OrderStatus, page paging.Request) (paging.Page[types.OrderView], error) {
	filter, err := callersOrders(ctx)
	if err != nil {
		return paging.Page[types.OrderView]{}, err
	}
	if len(statuses) > 0 {
		in := make([]string, 0, len(statuses))
		for _, status := range statuses {
			in = append(in, string(status))
		}
		filter.Status = &orm.StringFilter{In: in}
	}
	sort, err := orm.ParseOrderSort(page.Sort.String())
	if err != nil {
		return paging.Page[types.OrderView]{}, err
	}
	after, err := pageKey[orm.OrderPageKey](page)
	if err != nil {
		return paging.Page[types.OrderView]{}, err
	}
	found, err := o.DB.GetOrderRepository().FindPage(ctx, filter, &orm.OrderPageOptions{
		Fields: withLines().Fields,
		Sort:   sort,
		After:  after,
		Limit:  page.Limit,
	})
	if err != nil {
		return paging.Page[types.OrderView]{}, pageError(err)
	}
	return pageOf(found.Rows, found.Next, orderView)
}

// CancelOrder cancels the caller's order, or any order for staff, while
// it is placed, recording why. The update is the check: it cancels the
// order only while it is placed, as FulfilOrders fulfils it only while it
// is, so the worker fulfilling it between a read and the write cannot be
// undone. When it cancels nothing, the order, read as the caller may see
// it, is the 409 of one no longer placed, or the 404 of one the caller
// may not see or no one placed.
func (o *Orders) CancelOrder(ctx context.Context, id types.IdentityUUID, reason string) (*types.OrderView, error) {
	ctx = actingUser(ctx)
	filter, err := callersOrderFilter(ctx, id)
	if err != nil {
		return nil, err
	}
	placed := string(db.OrderStatus_Placed)
	whilePlaced := *filter
	whilePlaced.Status = &orm.StringFilter{Eq: &placed}
	cancelled := db.OrderStatus_Cancelled
	update := &orm.OrderUpdate{Status: &cancelled}
	if reason != "" {
		update.CancelReason = &reason
	}
	orders := o.DB.GetOrderRepository()
	n, err := orders.UpdateMany(ctx, &whilePlaced, update)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		order, err := orders.FindOne(ctx, filter, nil)
		if errors.Is(err, orm.ErrNotFound) {
			return nil, api.NotFoundError("order", err)
		}
		if err != nil {
			return nil, err
		}
		return nil, api.ConflictError(fmt.Sprintf("order %s is %s", id, order.Status), nil)
	}
	return o.GetOrder(ctx, id)
}

// callersOrders filters the orders to those the caller may see: their
// own, by its customer, as the SQLite shop reads them, or every order for
// staff.
func callersOrders(ctx context.Context) (*orm.OrderFilter, error) {
	filter := &orm.OrderFilter{}
	if seesEveryOrder(ctx) {
		return filter, nil
	}
	customer, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	filter.CustomerID = &orm.UUIDFilter{Eq: &customer}
	return filter, nil
}

// callersOrderFilter filters the orders to the one with id when the caller
// may see it (callersOrders). Another customer's order matches nothing, as
// one no one placed does.
func callersOrderFilter(ctx context.Context, id types.IdentityUUID) (*orm.OrderFilter, error) {
	filter, err := callersOrders(ctx)
	if err != nil {
		return nil, err
	}
	filter.Id = &orm.UUIDFilter{Eq: &id}
	return filter, nil
}

// orderView copies an order and its lines into the view a caller sees,
// its lines being the one field the view declares @virtual.
func orderView(order *db.Order) types.OrderView {
	view := types.OrderView{
		Status:          order.Status,
		PlacedAt:        order.PlacedAt,
		ShippingAddress: order.ShippingAddress,
		CancelReason:    order.CancelReason,
		TotalCents:      order.TotalCents,
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
	}
	return view
}

// Reviews implements the product-reviews namespace of shop-orders.
type Reviews struct {
	DB orm.DatabaseInterface
}

var _ api.ProductReviewsImplementation = (*Reviews)(nil)

// ListReviews answers a page of a product's reviews in the page's sort,
// by when each was written or by its rating, those rated minRating or more
// when it is given. A search, q, keeps the reviews whose title or body
// holds each of its words, in any case (D62): the ORM's
// ReviewFilter.SearchText makes each word a condition on the table's
// search_text, which Postgres's trigram index serves. Deleted reviews are
// left out.
func (r *Reviews) ListReviews(ctx context.Context, productID types.IdentityUUID, minRating *float64, q *string, page paging.Request) (paging.Page[types.ReviewView], error) {
	filter := &orm.ReviewFilter{ProductID: &orm.UUIDFilter{Eq: &productID}, SearchText: q}
	if minRating != nil {
		filter.Rating = &orm.FloatFilter{Gte: minRating}
	}
	sort, err := orm.ParseReviewSort(page.Sort.String())
	if err != nil {
		return paging.Page[types.ReviewView]{}, err
	}
	after, err := pageKey[orm.ReviewPageKey](page)
	if err != nil {
		return paging.Page[types.ReviewView]{}, err
	}
	found, err := r.DB.GetReviewRepository().FindPage(ctx, filter, &orm.ReviewPageOptions{
		Fields: orm.ReviewFields{}.All(),
		Sort:   sort,
		After:  after,
		Limit:  page.Limit,
	})
	if err != nil {
		return paging.Page[types.ReviewView]{}, pageError(err)
	}
	return pageOf(found.Rows, found.Next, reviewView)
}

func (r *Reviews) WriteReview(ctx context.Context, productID types.IdentityUUID, input *types.WriteReviewInput) (*types.ReviewView, error) {
	author, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ctx = actingUser(ctx)
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
// records deletedBy. The generated server authenticates the caller and puts
// their id on the context; the ORM reads the user from its own context key,
// so each method that writes rows hands the caller over.
func actingUser(ctx context.Context) context.Context {
	if id, err := scalars.ParseUUID(api.GetPrincipalID(ctx)); err == nil {
		return orm.WithUserID(ctx, id)
	}
	return ctx
}
