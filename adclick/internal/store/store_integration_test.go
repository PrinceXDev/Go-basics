package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"adclick/internal/event"
	"adclick/migrations"
)

// Runs against a real throw-away Postgres (downloaded once by embedded-postgres).
// Skipped with -short, or if Postgres cannot start.
func newTestStore(t testing.TB) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test")
	}
	dir, err := os.MkdirTemp("", "adclick-pg-*")
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(54000 + os.Getpid()%900)
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).DataPath(dir + "/data").RuntimePath(dir + "/run").BinariesPath(dir + "/bin").
		Username("adclick").Password("adclick").Database("adclick"))
	if err := pg.Start(); err != nil {
		t.Skipf("cannot start embedded postgres: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop(); _ = os.RemoveAll(dir) })

	ctx := context.Background()
	s, err := New(ctx, fmt.Sprintf("postgres://adclick:adclick@localhost:%d/adclick?sslmode=disable", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return s
}

func mk(user string, at time.Time) event.Click {
	return event.Click{
		EventID: event.NewID(), AdID: "ad-1001", CampaignID: "camp-summer", AdvertiserID: "adv-acme",
		UserID: user, ClickedAt: at, Country: "US", DeviceType: "mobile", Browser: "chrome", Source: "web", IPHash: "h-" + user,
	}
}

func TestApplyBatchIsIdempotentAndCountsUniques(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	batch := []event.Click{mk("u1", now), mk("u1", now), mk("u2", now)}
	batch = append(batch, batch[0]) // duplicate delivery inside the same batch

	n, err := s.ApplyBatch(ctx, batch)
	if err != nil || n != 3 {
		t.Fatalf("first apply: n=%d err=%v", n, err)
	}
	// Simulate Kafka redelivering the whole batch after a crash.
	n, err = s.ApplyBatch(ctx, batch)
	if err != nil || n != 0 {
		t.Fatalf("replay: n=%d err=%v", n, err)
	}

	sum, err := s.Summary(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalClicks != 3 || sum.UniqueClicks != 2 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestReadQueriesRunAndAgree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.ApplyBatch(ctx, []event.Click{mk("a", now), mk("b", now), mk("a", now)}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddImpressions(ctx, "ad-1001", 100, now); err != nil {
		t.Fatal(err)
	}
	from, to := now.Add(-2*time.Hour), now.Add(time.Hour)

	sum, err := s.Summary(ctx, from, to)
	if err != nil || sum.TotalClicks != 3 || sum.Impressions != 100 || sum.CTR != 0.03 {
		t.Fatalf("summary %+v err=%v", sum, err)
	}
	if sum.ClicksPerMinute <= 0 {
		t.Fatalf("clicks per minute %v", sum.ClicksPerMinute)
	}
	ads, err := s.TopAds(ctx, from, to, 5)
	if err != nil || len(ads) != 1 || ads[0].Clicks != 3 || ads[0].CTR != 0.03 {
		t.Fatalf("top ads %+v err=%v", ads, err)
	}
	camps, err := s.Campaigns(ctx, from, to)
	if err != nil || len(camps) != 1 || camps[0].Name != "Summer Sale" {
		t.Fatalf("campaigns %+v err=%v", camps, err)
	}
	for _, dim := range []string{"country", "device_type"} {
		b, err := s.Breakdown(ctx, dim, from, to)
		if err != nil || len(b) != 1 || b[0].Clicks != 3 {
			t.Fatalf("%s %+v err=%v", dim, b, err)
		}
	}
	for _, g := range []string{"minute", "hour", "day"} {
		p, err := s.Timeseries(ctx, g, from.Add(-48*time.Hour), to, "camp-summer", "")
		if err != nil || len(p) == 0 {
			t.Fatalf("%s %+v err=%v", g, p, err)
		}
	}
	if _, err := s.Breakdown(ctx, "1; DROP TABLE ads", from, to); err == nil {
		t.Fatal("dimension must be whitelisted")
	}
}

func TestLateEventLandsInItsOwnBucket(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	late := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := s.ApplyBatch(ctx, []event.Click{mk("late", late)}); err != nil {
		t.Fatal(err)
	}
	pts, err := s.Timeseries(ctx, "day", late.Add(-24*time.Hour), late.Add(24*time.Hour), "", "")
	if err != nil || len(pts) != 1 || pts[0].Clicks != 1 {
		t.Fatalf("pts=%v err=%v", pts, err)
	}
}

func TestConcurrentBatchesDoNotDeadlock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				b := make([]event.Click, 50)
				for j := range b {
					b[j] = mk(fmt.Sprintf("w%d-%d-%d", w, i, j), now)
				}
				if _, err := s.ApplyBatch(ctx, b); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	sum, _ := s.Summary(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if sum.TotalClicks != 8*10*50 {
		t.Fatalf("total=%d", sum.TotalClicks)
	}
}

// BenchmarkApplyBatch measures the consumer's write path (dedup + uniques +
// 3 aggregate upserts) on a laptop Postgres. Run: go test ./internal/store -bench ApplyBatch -benchtime 40x
func BenchmarkApplyBatch(b *testing.B) {
	s := newTestStore(b)
	ctx := context.Background()
	const batchSize = 500
	now := time.Now().UTC()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := make([]event.Click, batchSize)
		for j := range batch {
			batch[j] = mk(fmt.Sprintf("u%d", (i*batchSize+j)%20000), now)
		}
		if _, err := s.ApplyBatch(ctx, batch); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N*batchSize)/b.Elapsed().Seconds(), "events/s")
}
