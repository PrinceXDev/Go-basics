package product

import (
	"database/sql"
	"errors"
)

var ErrNotFound = errors.New("product not found")

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(p *Product) error {
	return r.db.QueryRow(
		`INSERT INTO products (name, price, stock) VALUES ($1, $2, $3) RETURNING id, created_at`,
		p.Name, p.Price, p.Stock,
	).Scan(&p.ID, &p.CreatedAt)
}

func (r *Repository) GetByID(id int64) (*Product, error) {
	p := &Product{}
	err := r.db.QueryRow(
		`SELECT id, name, price, stock, created_at FROM products WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.Price, &p.Stock, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (r *Repository) List() ([]Product, error) {
	rows, err := r.db.Query(`SELECT id, name, price, stock, created_at FROM products ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Stock, &p.CreatedAt); err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

func (r *Repository) Update(p *Product) error {
	res, err := r.db.Exec(
		`UPDATE products SET name = $1, price = $2, stock = $3 WHERE id = $4`,
		p.Name, p.Price, p.Stock, p.ID,
	)
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
	res, err := r.db.Exec(`DELETE FROM products WHERE id = $1`, id)
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

// AdjustStock changes stock by delta (negative to decrement) inside the
// caller's transaction, and fails if that would push stock below zero —
// this is what order creation calls to reserve inventory.
func AdjustStock(tx *sql.Tx, productID int64, delta int) error {
	res, err := tx.Exec(
		`UPDATE products SET stock = stock + $1 WHERE id = $2 AND stock + $1 >= 0`,
		delta, productID,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("insufficient stock or product not found")
	}
	return nil
}
