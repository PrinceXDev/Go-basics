package main

// ============================================================================
// CONCEPT: the layer above database/sql — ORM (GORM) vs query codegen (sqlc)
// vs staying raw. Which one should you pick, and why?
//
// WHY THIS MATTERS
// Lessons 36-37 gave you the foundation everything is built on. But writing
// Scan(&a, &b, &c, &d, &e, &f) by hand for every query gets old, and it's
// where bugs live (wrong column order compiles fine and silently corrupts
// your data). The ecosystem has two serious answers, and they pull in
// opposite directions.
//
// This file RUNS the GORM half, and explains the sqlc half — sqlc is a
// code generator you install as a binary, so it can't be demonstrated in a
// single `go run`. The README in this folder has the full sqlc walkthrough.
//
// JS/TS comparison:
//   GORM  ~ TypeORM / Sequelize    (objects first, SQL generated for you)
//   sqlc  ~ Prisma / Kysely        (schema/SQL first, types generated)
//   raw   ~ pg / better-sqlite3    (lesson 36)
// ============================================================================

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite" // pure-Go SQLite driver for GORM (no CGO)
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ---------------------------------------------------------------------------
// GORM MODELS
// GORM reads struct tags (lesson 32) to figure out columns, constraints and
// relationships. By convention it pluralises the struct name for the table:
// Author -> "authors".
// ---------------------------------------------------------------------------

type Author struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:100;not null;uniqueIndex"`
	Country   string `gorm:"size:2;default:NZ"`
	CreatedAt time.Time
	UpdatedAt time.Time // GORM maintains CreatedAt/UpdatedAt automatically

	// A has-many relationship. GORM infers the foreign key as AuthorID on
	// the Book side from the type name.
	Books []Book
}

type Book struct {
	ID       uint   `gorm:"primaryKey"`
	Title    string `gorm:"size:200;not null;index"`
	Pages    int    `gorm:"check:pages >= 0"`
	AuthorID uint   // the foreign key
	Author   Author // the belongs-to side; populated only when you Preload
}

func main() {
	if err := realMain(); err != nil {
		log.Fatal(err)
	}
}

func realMain() error {
	dbPath := filepath.Join("38_sqlc_or_gorm", "library.db")

	// ---------- connecting ----------
	// gorm.Open wraps a *sql.DB. You can always get the underlying pool
	// back with db.DB() and drop to raw database/sql when GORM gets in the
	// way — which you will need to do sooner than you'd like.
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		// Logging every statement is the only way to see what SQL GORM is
		// actually producing. Keep it on while learning; in production use
		// logger.Warn and hook it into slog (lesson 40).
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}

	sqlDB, err := db.DB() // the *sql.DB from lesson 36 — same pool settings
	if err != nil {
		return fmt.Errorf("underlying pool: %w", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(10)

	// ---------- AutoMigrate ----------
	// GORM can create/alter tables to match your structs. It is ADDITIVE
	// ONLY: it adds tables, columns and indexes, and never drops or renames
	// anything. Convenient in development; NOT a substitute for the real
	// migrations in lesson 37, because it gives you no version history, no
	// review, and no way to express a data backfill.
	if err := db.Migrator().DropTable(&Book{}, &Author{}); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	if err := db.AutoMigrate(&Author{}, &Book{}); err != nil {
		return fmt.Errorf("automigrate: %w", err)
	}
	fmt.Println("== schema ready (AutoMigrate) ==")

	// ---------- Create ----------
	// GORM writes the struct AND its associations in one call, inside a
	// transaction, and fills the generated IDs back into your struct.
	fmt.Println()
	fmt.Println("== Create (with nested associations) ==")
	donovan := Author{
		Name:    "Alan Donovan",
		Country: "US",
		Books: []Book{
			{Title: "The Go Programming Language", Pages: 380},
		},
	}
	if err := db.Create(&donovan).Error; err != nil { // note: &, GORM mutates it
		return fmt.Errorf("create author: %w", err)
	}
	fmt.Printf("author id=%d, first book id=%d\n", donovan.ID, donovan.Books[0].ID)

	// Batch insert.
	more := []Author{
		{Name: "Jon Bodner", Country: "US", Books: []Book{{Title: "Learning Go", Pages: 375}}},
		{Name: "Katherine Cox-Buday", Books: []Book{{Title: "Concurrency in Go", Pages: 238}}},
	}
	if err := db.Create(&more).Error; err != nil {
		return fmt.Errorf("batch create: %w", err)
	}
	fmt.Println("batch inserted:", len(more))

	// ---------- Read ----------
	fmt.Println()
	fmt.Println("== queries ==")

	// First = ORDER BY primary key LIMIT 1, and it ERRORS if nothing matches.
	var a Author
	if err := db.First(&a, "name = ?", "Jon Bodner").Error; err != nil {
		return fmt.Errorf("find author: %w", err)
	}
	fmt.Printf("First: %s (%s)\n", a.Name, a.Country)

	// gorm.ErrRecordNotFound is GORM's sql.ErrNoRows. Same rule as lesson
	// 37: translate it into YOUR domain error at the repository boundary.
	err = db.First(&Author{}, "name = ?", "Nobody").Error
	fmt.Println("missing record ->", errors.Is(err, gorm.ErrRecordNotFound))

	// Find fills a slice and does NOT error on zero rows — check len().
	// This asymmetry between First and Find catches everyone once.
	var shortBooks []Book
	if err := db.Where("pages < ?", 300).Order("pages desc").Limit(5).
		Find(&shortBooks).Error; err != nil {
		return fmt.Errorf("find books: %w", err)
	}
	fmt.Printf("books under 300 pages: %d\n", len(shortBooks))

	// Preload is how you avoid the N+1 problem: it issues ONE extra query
	// with `WHERE author_id IN (...)` instead of one query per author.
	// Without it, a.Books is empty and it is very easy not to notice.
	var authors []Author
	if err := db.Preload("Books").Find(&authors).Error; err != nil {
		return fmt.Errorf("preload: %w", err)
	}
	for _, au := range authors {
		fmt.Printf("  %-22s %d book(s)\n", au.Name, len(au.Books))
	}

	// Aggregates need a target struct or a scalar.
	var totalPages int
	if err := db.Model(&Book{}).Select("COALESCE(SUM(pages), 0)").
		Scan(&totalPages).Error; err != nil {
		return fmt.Errorf("sum: %w", err)
	}
	fmt.Println("total pages:", totalPages)

	// ---------- Update ----------
	fmt.Println()
	fmt.Println("== updates ==")

	// Save writes ALL fields, including zero values. Updates with a struct
	// writes only NON-ZERO fields — so `Updates(Book{Pages: 0})` silently
	// does nothing. Use a map when you need to write a zero.
	if err := db.Model(&Book{}).Where("title = ?", "Learning Go").
		Updates(map[string]any{"pages": 400}).Error; err != nil {
		return fmt.Errorf("update: %w", err)
	}
	fmt.Println("updated Learning Go to 400 pages")

	// GORM refuses a global update without a WHERE — a good guardrail.
	err = db.Model(&Book{}).Update("pages", 1).Error
	fmt.Println("blocked global update:", errors.Is(err, gorm.ErrMissingWhereClause))

	// ---------- Transactions ----------
	fmt.Println()
	fmt.Println("== transaction ==")
	// db.Transaction commits if the closure returns nil and rolls back if
	// it returns an error or panics. Much harder to get wrong than manual
	// Begin/Commit/Rollback.
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&Author{Name: "Temp Author"}).Error; err != nil {
			return err
		}
		return errors.New("simulated failure after the insert")
	})
	fmt.Println("rolled back:", err)

	var count int64
	db.Model(&Author{}).Where("name = ?", "Temp Author").Count(&count)
	fmt.Println("Temp Author rows remaining:", count, "(0 == rollback worked)")

	// ---------- the escape hatch ----------
	// Every ORM eventually can't express the query you need. GORM lets you
	// drop to raw SQL and still scan into your structs — reach for this
	// early rather than fighting the query builder.
	fmt.Println()
	fmt.Println("== raw SQL escape hatch ==")
	type row struct {
		Name  string
		Books int
	}
	var rows []row
	if err := db.Raw(`
		SELECT a.name, COUNT(b.id) AS books
		FROM authors a LEFT JOIN books b ON b.author_id = a.id
		GROUP BY a.id ORDER BY books DESC, a.name`).Scan(&rows).Error; err != nil {
		return fmt.Errorf("raw: %w", err)
	}
	for _, r := range rows {
		fmt.Printf("  %-22s %d\n", r.Name, r.Books)
	}

	return nil
}

// ----------------------------------------------------------------------------
// GORM GOTCHAS THAT WILL BITE YOU
//   1. Updates(struct) skips ZERO values. Pass a map to write 0/""/false.
//   2. First errors on no rows; Find does not. Check len() after Find.
//   3. Forgetting Preload gives you empty associations, not an error —
//      the classic silent N+1 / missing-data bug.
//   4. AutoMigrate never drops or renames. Your prod schema will drift.
//      Use real migrations (lesson 37) and treat AutoMigrate as a dev toy.
//   5. Always pass a POINTER to Create/Find/First — GORM writes into it.
//   6. Errors hide behind `.Error`; a chain that ends without it is a
//      silently ignored failure. errcheck/golangci-lint (lesson 45) catches
//      this.
//   7. Soft deletes: embedding gorm.DeletedAt makes Delete an UPDATE and
//      adds `deleted_at IS NULL` to every query. Surprising if inherited.
//
// ----------------------------------------------------------------------------
// HOW TO CHOOSE
//
//   sqlc      You write SQL; it generates typed Go. Compile-time-checked
//   (pick     against your real schema, zero runtime reflection, no N+1
//    this)    surprises, and the SQL in code review IS the SQL that runs.
//             Costs: a codegen step in your build, and dynamic queries are
//             awkward. This is the current default choice for new Go
//             services, and it pairs perfectly with lesson 37's migrations.
//
//   GORM      Fastest to a CRUD prototype, great for deeply nested
//             relationships, familiar if you come from TypeORM. Costs:
//             reflection at runtime, surprising semantics (see above), and
//             opaque generated SQL you'll end up bypassing anyway.
//
//   sqlx      A thin helper over lesson 36: StructScan, named parameters,
//             `IN (?)` expansion. Minimal magic, no codegen. A fine middle
//             ground for a small service.
//
//   raw       Lessons 36-37. Always correct, always verbose.
//
// See README.md in this folder for the full sqlc setup (schema.sql,
// query.sql, sqlc.yaml, and what the generated code looks like).
// ----------------------------------------------------------------------------
