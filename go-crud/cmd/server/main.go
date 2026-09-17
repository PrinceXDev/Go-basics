package main

import (
	"log"
	"net/http"
	"time"

	"go-crud/internal/config"
	"go-crud/internal/database"
	"go-crud/internal/order"
	"go-crud/internal/product"
	"go-crud/internal/router"
	"go-crud/internal/user"
)

/*
┌─────────────────────────────────────────────────────────────────┐
│  $ go run ./cmd/server                                          │
└─────────────────────────────────────────────────────────────────┘
        │
        ▼
1. config.Load()
        │  reads APP_PORT, DATABASE_URL from env
        ▼
2. database.Connect(dsn)
        │  opens a connection pool, Ping()s it
        │  ❌ fails here -> log.Fatalf, process exits immediately
        ▼
3. database.Migrate(db, "migrations")   <-- the function we just dissected
        │
        │   ┌───────────────────────────────────────────┐
        │   │ a) ensure schema_migrations table exists  │
        │   │ b) read which versions are already applied│
        │   │ c) glob + sort migrations/*.sql           │
        │   │ d) for each NOT-yet-applied file:         │
        │   │      BEGIN -> run SQL -> record -> COMMIT │
        │   └───────────────────────────────────────────┘
        │
        │  after this line: your Postgres database is
        │  GUARANTEED to have users/products/orders/order_items
        │  tables, no matter how empty it was a second ago
        ▼
4. build user/product/order repositories, services, handlers
        │  (these all now safely assume the tables exist)
        ▼
5. router.New(...)  -- wire routes onto the mux
        ▼
6. srv.ListenAndServe()
        │  blocks here forever, handling requests
        ▼
(server is now live on :8080)
*/

func main() {
	cfg := config.Load()

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect db: %v", err)
	}
	defer db.Close()

	/* First ever run against a fresh DB → applied = 4.
	Restart the exact same server a second later with no new .sql files → applied = 0,
	because everything's already on the clipboard.
	That's the payoff: idempotent — safe to run any number of times, always converges to the same end state. */
	applied, err := database.Migrate(db, "migrations")
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Printf("migrations applied: %d", applied)

	userRepo := user.NewRepository(db)
	userSvc := user.NewService(userRepo)
	userHandler := user.NewHandler(userSvc)

	productRepo := product.NewRepository(db)
	productSvc := product.NewService(productRepo)
	productHandler := product.NewHandler(productSvc)

	orderRepo := order.NewRepository(db)
	orderSvc := order.NewService(orderRepo, userRepo)
	orderHandler := order.NewHandler(orderSvc)

	handler := router.New(userHandler, productHandler, orderHandler)

	// Start the server ( consider control room of your web server )
	// http.ServeMux (multiplexer)
	srv := &http.Server{
		Addr:              ":" + cfg.Port,   // Where should I listen?
		Handler:           handler,          // When an HTTP request arrives, which code should process it?
		ReadHeaderTimeout: 5 * time.Second,  // This controls how long the server waits for the HTTP request headers to arrive.
		ReadTimeout:       10 * time.Second, // This controls how long the server allows for reading the entire request, including the request body.
		WriteTimeout:      15 * time.Second, // This controls how long the server can spend writing the response.
		IdleTimeout:       60 * time.Second, // This controls how long an idle keep-alive connection can remain open.
	}

	log.Printf("listening on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}

	/* Browser / Frontend
	       |
	       | GET /users
	       v
	   HTTP Server
	       |
	       v
	   ServeMux
	       |
	       | "/users" matches
	       v
	 usersHandler */

	/*                  ┌──→ /products → Product Handler
	                    │
	HTTP Requests ──────┼──→ /users    → User Handler
	                    │
	                    ├──→ /login    → Login Handler
	                    │
	                    └──→ /orders   → Order Handler

	                         ServeMux */
}
