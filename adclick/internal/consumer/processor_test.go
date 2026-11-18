package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"adclick/internal/event"
	"adclick/internal/metrics"
	"adclick/internal/store"

	"io"
	"log/slog"
)

type fakeSink struct {
	mu      sync.Mutex
	fails   int // fail this many times, then succeed
	calls   int
	applied []event.Click
}

func (f *fakeSink) ApplyBatch(_ context.Context, evs []event.Click) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fails > 0 {
		f.fails--
		return 0, errors.New("db down")
	}
	f.applied = append(f.applied, evs...)
	return len(evs), nil
}

type fakeAds map[string]store.AdInfo

func (f fakeAds) Lookup(id string) (store.AdInfo, bool) { a, ok := f[id]; return a, ok }

type fakeParker struct {
	retries []Msg
	dlq     []string
	failDLQ bool
}

func (p *fakeParker) Retry(_ context.Context, m Msg, attempt int, _ time.Time) error {
	m.Attempt = attempt
	p.retries = append(p.retries, m)
	return nil
}
func (p *fakeParker) DLQ(_ context.Context, _ Msg, reason string) error {
	if p.failDLQ {
		return errors.New("kafka down")
	}
	p.dlq = append(p.dlq, reason)
	return nil
}

func msg(t *testing.T, c event.Click, attempt int) Msg {
	b, _ := json.Marshal(c)
	return Msg{Topic: "ad-click-events", Value: b, Attempt: attempt}
}

func click(ad string) event.Click {
	return event.Click{EventID: event.NewID(), AdID: ad, ClickedAt: time.Now(), Country: "US",
		DeviceType: "mobile", Browser: "chrome", Source: "web", IPHash: "h"}
}

func setup(sink *fakeSink, p *fakeParker) *Processor {
	return NewProcessor(sink, fakeAds{"ad-1": {CampaignID: "c1", AdvertiserID: "a1"}}, p, metrics.New(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		Options{MaxRetries: 2, RetryDelay: time.Millisecond, Limits: event.Limits{MaxAge: time.Hour, FutureSkew: time.Minute}})
}

func TestEnrichesFromAdsTable(t *testing.T) {
	s, p := &fakeSink{}, &fakeParker{}
	c := click("ad-1")
	c.CampaignID = "client-lies"
	if err := setup(s, p).Process(context.Background(), []Msg{msg(t, c, 0)}); err != nil {
		t.Fatal(err)
	}
	if len(s.applied) != 1 || s.applied[0].CampaignID != "c1" || s.applied[0].AdvertiserID != "a1" {
		t.Fatalf("%+v", s.applied)
	}
}

func TestPoisonMessageGoesToDLQNotBlocking(t *testing.T) {
	s, p := &fakeSink{}, &fakeParker{}
	batch := []Msg{{Topic: "x", Value: []byte("{not json")}, msg(t, click("ad-1"), 0)}
	if err := setup(s, p).Process(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if len(p.dlq) != 1 || len(s.applied) != 1 {
		t.Fatalf("dlq=%d applied=%d", len(p.dlq), len(s.applied))
	}
}

func TestUnknownAdRetriesThenDLQ(t *testing.T) {
	s, p := &fakeSink{}, &fakeParker{}
	proc := setup(s, p)
	ctx := context.Background()

	if err := proc.Process(ctx, []Msg{msg(t, click("ghost"), 0)}); err != nil {
		t.Fatal(err)
	}
	if len(p.retries) != 1 || p.retries[0].Attempt != 1 {
		t.Fatalf("retries=%+v", p.retries)
	}
	// After MaxRetries hops the message is parked in the DLQ.
	if err := proc.Process(ctx, []Msg{msg(t, click("ghost"), 2)}); err != nil {
		t.Fatal(err)
	}
	if len(p.dlq) != 1 {
		t.Fatalf("dlq=%v", p.dlq)
	}
}

func TestDatabaseOutageIsRetriedNotLost(t *testing.T) {
	s, p := &fakeSink{fails: 3}, &fakeParker{}
	if err := setup(s, p).Process(context.Background(), []Msg{msg(t, click("ad-1"), 0)}); err != nil {
		t.Fatal(err)
	}
	if s.calls != 4 || len(s.applied) != 1 {
		t.Fatalf("calls=%d applied=%d", s.calls, len(s.applied))
	}
}

func TestShutdownDuringOutageDoesNotCommit(t *testing.T) {
	s, p := &fakeSink{fails: 1 << 30}, &fakeParker{}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := setup(s, p).Process(ctx, []Msg{msg(t, click("ad-1"), 0)})
	if err == nil {
		t.Fatal("must return an error so offsets are NOT committed")
	}
}

func TestDLQPublishFailureBlocksCommit(t *testing.T) {
	s, p := &fakeSink{}, &fakeParker{failDLQ: true}
	err := setup(s, p).Process(context.Background(), []Msg{{Value: []byte("bad")}})
	if err == nil {
		t.Fatal("DLQ failure must fail the batch, otherwise the message is lost")
	}
}
