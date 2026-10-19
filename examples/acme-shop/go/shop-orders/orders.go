package shoporders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	stocksdk "example.com/acme/sdk/go/shop-stock"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	stocktypes "example.com/acme/types/go/shop-stock"
	"github.com/jackc/pgx/v5/pgconn"
	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
	"github.com/parable-work/superschematic/runtime/http/go/search"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// Orders implements the order namespace of shop-orders, which the
// OrderQueries and OrderMutations operation sets share. A shopper reads,
// lists and cancels their own orders; staff, whose roles cover
// orders.any, every customer's.
type Orders struct {
	DB orm.DatabaseInterface

	// Stock is the client of shop-stock, which reserves an order's stock.
	// The server's entrypoint builds it with shop-orders' service
	// credential, and it forwards the shopper of the request each call's
	// context belongs to.
	Stock *stocksdk.ShopStockSDK
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
	ctx = actingUser(ctx)
	var placed *db.Order
	reservation := stocktypes.ReserveStockInput{Lines: make([]stocktypes.ReserveStockLine, 0, len(input.Lines))}
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
			reservation.Lines = append(reservation.Lines, stocktypes.ReserveStockLine{
				ProductId: *product.Id,
				Quantity:  stocktypes.GenericInt64(item.Quantity),
			})
		}
		// shop-stock takes every line off the shelf, or none, for the
		// shopper this request's context forwards. Its refusal rolls the
		// order back. Should the commit fail after it, the stock stays
		// reserved: this shop releases none.
		if err := o.reserve(ctx, reservation); err != nil {
			return err
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

// reserve reserves an order's stock on shop-stock. The warehouse's
// refusal, a 409 for a line it cannot fill, is the shopper's to read, so
// the order is refused with its message; any other failure is the
// server's.
func (o *Orders) reserve(ctx context.Context, reservation stocktypes.ReserveStockInput) error {
	_, err := o.Stock.StockReservationsNamespace.ReserveStock(ctx, reservation)
	var refused *stocksdk.APIError
	if errors.As(err, &refused) && refused.StatusCode == http.StatusConflict {
		return api.ConflictError(refused.Message, err)
	}
	if err != nil {
		return fmt.Errorf("reserve the order's stock on shop-stock: %w", err)
	}
	return nil
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

// orderKey is an order's place in ListOrders' order: when it was placed,
// then its id.
type orderKey struct {
	PlacedAt time.Time          `json:"placedAt"`
	ID       types.IdentityUUID `json:"id"`
}

func (key orderKey) rowID() types.IdentityUUID { return key.ID }

// ListOrders answers a page of the caller's orders, or of every
// customer's for staff, oldest first, with the given statuses, or of every
// status when there are none. A cursor names a place among the orders the
// caller lists, so a shopper's pages hold their own orders alone.
func (o *Orders) ListOrders(ctx context.Context, statuses []types.OrderStatus, page paging.Request) (paging.Page[types.OrderView], error) {
	query := &keyQuery{}
	if !seesEveryOrder(ctx) {
		customer, err := caller(ctx)
		if err != nil {
			return paging.Page[types.OrderView]{}, err
		}
		query.where("customer_id = %s::uuid", customer.ToUUID())
	}
	if len(statuses) > 0 {
		in := make([]string, 0, len(statuses))
		for _, status := range statuses {
			in = append(in, string(status))
		}
		query.where("status = ANY(%s::text[])", in)
	}
	if page.Cursor != "" {
		after, err := decodeKey[orderKey](page.Cursor)
		if err != nil {
			return paging.Page[types.OrderView]{}, err
		}
		query.where("(placed_at, id) > (%s::timestamptz, %s::uuid)", after.PlacedAt, after.ID.ToUUID())
	}
	// The table is named order, a keyword, so the keys' query quotes it.
	return readPage[orderKey](ctx, o.DB, query, `"order"`, "placed_at, id", page,
		func(tx orm.TxInterface, ids []types.IdentityUUID) ([]types.OrderView, error) {
			// The page's orders, with their lines, in the keys' order.
			orders, _, err := tx.GetOrderRepository().FindMany(ctx, &orm.OrderFilter{Id: &orm.UUIDFilter{In: ids}},
				&orm.OrderFindOptions{
					Fields:  withLines().Fields,
					OrderBy: []orm.OrderOrderBy{{Field: orm.OrderOrderByPlacedAt}, {Field: orm.OrderOrderById}},
					Limit:   len(ids),
				})
			if err != nil {
				return nil, err
			}
			views := make([]types.OrderView, 0, len(orders))
			for _, order := range orders {
				views = append(views, orderView(order))
			}
			return views, nil
		})
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

// callersOrderFilter filters the orders to the one with id when the caller
// may see it: their own, by its customer, as the SQLite shop reads it, or
// any order for staff. Another customer's order matches nothing, as one no
// one placed does.
func callersOrderFilter(ctx context.Context, id types.IdentityUUID) (*orm.OrderFilter, error) {
	customer, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	filter := &orm.OrderFilter{Id: &orm.UUIDFilter{Eq: &id}}
	if !seesEveryOrder(ctx) {
		filter.CustomerID = &orm.UUIDFilter{Eq: &customer}
	}
	return filter, nil
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

// reviewKey is a review's place in ListReviews' order: when it was
// written, then its id.
type reviewKey struct {
	CreatedAt time.Time          `json:"createdAt"`
	ID        types.IdentityUUID `json:"id"`
}

func (key reviewKey) rowID() types.IdentityUUID { return key.ID }

// ListReviews answers a page of a product's reviews, oldest first, those
// rated minRating or more when it is given. A search, q, keeps the reviews
// whose title or body holds each of its words, in any case (D62): each
// word is a condition on the table's search_text, which Postgres's trigram
// index serves. Deleted reviews are left out.
func (r *Reviews) ListReviews(ctx context.Context, productID types.IdentityUUID, minRating *float64, q *string, page paging.Request) (paging.Page[types.ReviewView], error) {
	query := &keyQuery{}
	query.where("product_id = %s::uuid", productID.ToUUID())
	query.where("deleted_at IS NULL")
	if minRating != nil {
		query.where("rating >= %s::double precision", *minRating)
	}
	if q != nil {
		for _, term := range search.Terms(*q) {
			query.where(`search_text ILIKE %s ESCAPE '\'`, search.LikePattern(term))
		}
	}
	if page.Cursor != "" {
		after, err := decodeKey[reviewKey](page.Cursor)
		if err != nil {
			return paging.Page[types.ReviewView]{}, err
		}
		query.where("(created_at, id) > (%s::timestamptz, %s::uuid)", after.CreatedAt, after.ID.ToUUID())
	}
	return readPage[reviewKey](ctx, r.DB, query, "review", "created_at, id", page,
		func(tx orm.TxInterface, ids []types.IdentityUUID) ([]types.ReviewView, error) {
			reviews, err := tx.GetReviewRepository().GetManyByIDs(ctx, ids)
			if err != nil {
				return nil, err
			}
			return inKeyOrder(ids, reviews, reviewView), nil
		})
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
