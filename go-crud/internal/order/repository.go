package order

import (
	"database/sql"
	"errors"

	"go-crud/internal/product"
)

var (
	ErrNotFound = errors.New("order not found")
	ErrNoItems  = errors.New("order must have at least one item")
)

// ItemInput is what the caller supplies per line item; UnitPrice and the
// running total are filled in from the current product price, not trusted
// from the client.
type ItemInput struct {
	ProductID int64
	Quantity  int
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts the order, its items, and decrements product stock, all in
// one transaction — if any product is missing or out of stock, everything
// rolls back and no partial order is left behind.
func (r *Repository) Create(userID int64, items []ItemInput) (*Order, error) {
	if len(items) == 0 {
		return nil, ErrNoItems
	}

	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck

	o := &Order{UserID: userID, Status: "pending"}
	var total float64
	built := make([]OrderItem, 0, len(items))

	for _, in := range items {
		var price float64
		err := tx.QueryRow(`SELECT price FROM products WHERE id = $1 FOR UPDATE`, in.ProductID).Scan(&price)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, product.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if err := product.AdjustStock(tx, in.ProductID, -in.Quantity); err != nil {
			return nil, err
		}
		total += price * float64(in.Quantity)
		built = append(built, OrderItem{ProductID: in.ProductID, Quantity: in.Quantity, UnitPrice: price})
	}
	o.TotalPrice = total

	if err := tx.QueryRow(
		`INSERT INTO orders (user_id, status, total_price) VALUES ($1, $2, $3) RETURNING id, created_at`,
		o.UserID, o.Status, o.TotalPrice,
	).Scan(&o.ID, &o.CreatedAt); err != nil {
		return nil, err
	}

	for i := range built {
		built[i].OrderID = o.ID
		if err := tx.QueryRow(
			`INSERT INTO order_items (order_id, product_id, quantity, unit_price) VALUES ($1, $2, $3, $4) RETURNING id`,
			built[i].OrderID, built[i].ProductID, built[i].Quantity, built[i].UnitPrice,
		).Scan(&built[i].ID); err != nil {
			return nil, err
		}
	}
	o.Items = built

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return o, nil
}

// GetByID loads an order together with its items, joined against products
// for the display name — this is the "relationship" payoff: one call
// returns the user's order, what's in it, and what each product is called.
func (r *Repository) GetByID(id int64) (*Order, error) {
	o := &Order{}
	err := r.db.QueryRow(
		`SELECT id, user_id, status, total_price, created_at FROM orders WHERE id = $1`, id,
	).Scan(&o.ID, &o.UserID, &o.Status, &o.TotalPrice, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := r.db.Query(`
		SELECT oi.id, oi.order_id, oi.product_id, p.name, oi.quantity, oi.unit_price
		FROM order_items oi
		JOIN products p ON p.id = oi.product_id
		WHERE oi.order_id = $1
		ORDER BY oi.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var it OrderItem
		if err := rows.Scan(&it.ID, &it.OrderID, &it.ProductID, &it.ProductName, &it.Quantity, &it.UnitPrice); err != nil {
			return nil, err
		}
		o.Items = append(o.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return o, nil
}

// ListByUser returns every order placed by one user (the reverse side of
// the User -> Orders relationship), without their line items.
func (r *Repository) ListByUser(userID int64) ([]Order, error) {
	rows, err := r.db.Query(
		`SELECT id, user_id, status, total_price, created_at FROM orders WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.Status, &o.TotalPrice, &o.CreatedAt); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func (r *Repository) List() ([]Order, error) {
	rows, err := r.db.Query(`SELECT id, user_id, status, total_price, created_at FROM orders ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.Status, &o.TotalPrice, &o.CreatedAt); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func (r *Repository) UpdateStatus(id int64, status string) error {
	res, err := r.db.Exec(`UPDATE orders SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) Delete(id int64) error {
	res, err := r.db.Exec(`DELETE FROM orders WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
