# go-crud

A small CRUD API in Go + PostgreSQL that models three related entities so
you can see how relationships work end to end:

```
User  1 ──< Order  (a user places many orders)
Order 1 ──< OrderItem  (an order has many line items)
Product 1 ──< OrderItem  (a product can appear in many orders)
```

So `orders` references `users` (`user_id`), and `order_items` is the join
table connecting `orders` to `products` — the classic many-to-many pattern.

## Structure

```
go-crud/
├── cmd/server/main.go       entry point: wires config, db, handlers, router
├── internal/
│   ├── config/              env var loading
│   ├── database/             connection + hand-rolled SQL migration runner
│   ├── httpx/                shared JSON response/decode helpers
│   ├── user/                 model, repository, service, handler
│   ├── product/              model, repository, service, handler
│   ├── order/                model, repository, service, handler
│   └── router/               mux + route registration + logging
├── migrations/                numbered .sql files, applied in order at startup
├── .env.example
└── go.mod
```

Each entity package follows the same layering:
- **model.go** – the Go struct + JSON tags
- **repository.go** – raw SQL against `*sql.DB` (parameterized queries)
- **service.go** – validation + business rules
- **handler.go** – HTTP request/response, wired onto the mux

## Running it

1. Start Postgres (adjust as needed):

   ```bash
   docker run --name go-crud-db -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=go_crud -p 5432:5432 -d postgres:16
   ```

2. Copy `.env.example` to `.env` and adjust `DATABASE_URL` if needed, then export it (or just rely on the defaults, which match the docker command above).

3. Run the server (migrations apply automatically on startup):

   ```bash
   go run ./cmd/server
   ```

Server listens on `:8080` by default.

## API

| Method | Path                    | Purpose                              |
|--------|-------------------------|---------------------------------------|
| POST   | /users                  | create a user                        |
| GET    | /users                  | list users                           |
| GET    | /users/{id}             | get one user                         |
| PUT    | /users/{id}             | update a user                        |
| DELETE | /users/{id}             | delete a user                        |
| GET    | /users/{id}/orders      | list a user's orders                 |
| POST   | /products               | create a product                     |
| GET    | /products               | list products                        |
| GET    | /products/{id}          | get one product                      |
| PUT    | /products/{id}          | update a product                     |
| DELETE | /products/{id}          | delete a product                     |
| POST   | /orders                 | place an order (see below)           |
| GET    | /orders                 | list all orders                      |
| GET    | /orders/{id}            | get one order with its line items    |
| PATCH  | /orders/{id}/status     | change status (pending/paid/shipped/cancelled) |
| DELETE | /orders/{id}            | delete an order                      |

### Example flow

```bash
# 1. create a user
curl -X POST localhost:8080/users -d '{"name":"Ada","email":"ada@example.com"}'

# 2. create a couple of products
curl -X POST localhost:8080/products -d '{"name":"Keyboard","price":49.99,"stock":10}'
curl -X POST localhost:8080/products -d '{"name":"Mouse","price":19.99,"stock":25}'

# 3. place an order for user 1 buying product 1 x2 and product 2 x1
curl -X POST localhost:8080/orders -d '{
  "user_id": 1,
  "items": [
    {"product_id": 1, "quantity": 2},
    {"product_id": 2, "quantity": 1}
  ]
}'

# 4. fetch the order — total_price and item prices are computed server-side,
#    and product stock has already been decremented
curl localhost:8080/orders/1

# 5. see all of that user's orders
curl localhost:8080/users/1/orders
```

Order creation runs in a single database transaction: it locks each
product row, checks stock, decrements it, computes the total from the
*current* product price (never trusting a client-supplied price), and
inserts the order + its items. If anything fails (missing product,
insufficient stock), the whole order is rolled back.
