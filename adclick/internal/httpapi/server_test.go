package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"adclick/internal/event"
	"adclick/internal/kafka"
	"adclick/internal/metrics"
	"adclick/internal/store"
)

type fakePub struct {
	err  error
	sent []event.Click
}

func (f *fakePub) Publish(_ context.Context, c event.Click) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, c)
	return nil
}

type fakeAds struct{ loaded bool }

func (f fakeAds) Loaded() bool { return f.loaded }
func (f fakeAds) Lookup(id string) (store.AdInfo, bool) {
	if id == "ad-1" {
		return store.AdInfo{CampaignID: "c1", AdvertiserID: "a1"}, true
	}
	return store.AdInfo{}, false
}

func newSrv(p *fakePub, ads fakeAds) http.Handler {
	return New(Deps{
		Pub: p, Ads: ads, Metrics: metrics.New(), Salt: "s",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Limits: event.Limits{MaxAge: time.Hour, FutureSkew: time.Minute},
	}).Handler()
}

func post(h http.Handler, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/clicks", strings.NewReader(body))
	r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone) Mobile Safari/604.1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAcceptedClickIsPublishedWithEnrichment(t *testing.T) {
	p := &fakePub{}
	w := post(newSrv(p, fakeAds{true}), `{"ad_id":"ad-1","user_id":"u1","country":"de"}`)
	if w.Code != 202 || len(p.sent) != 1 {
		t.Fatalf("code=%d sent=%d body=%s", w.Code, len(p.sent), w.Body)
	}
	c := p.sent[0]
	if c.CampaignID != "c1" || c.Country != "DE" || c.DeviceType != "mobile" || c.IPHash == "" || strings.Contains(c.IPHash, "192.0.2") {
		t.Fatalf("%+v", c)
	}
}

func TestClientEventIDMakesRetriesDeduplicatable(t *testing.T) {
	p := &fakePub{}
	h := newSrv(p, fakeAds{true})
	id := event.NewID()
	post(h, `{"ad_id":"ad-1","event_id":"`+id+`"}`)
	post(h, `{"ad_id":"ad-1","event_id":"`+id+`"}`)
	if len(p.sent) != 2 || p.sent[0].EventID != id || p.sent[1].EventID != id {
		t.Fatal("same event_id must be preserved so the consumer can dedup")
	}
}

func TestRejections(t *testing.T) {
	h := newSrv(&fakePub{}, fakeAds{true})
	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"bad json":    {`{`, 400},
		"missing ad":  {`{}`, 400},
		"bad device":  {`{"ad_id":"ad-1","device_type":"fridge"}`, 400},
		"unknown ad":  {`{"ad_id":"nope"}`, 404},
		"future time": {`{"ad_id":"ad-1","clicked_at":"2999-01-01T00:00:00Z"}`, 400},
	} {
		if w := post(h, tc.body); w.Code != tc.code {
			t.Errorf("%s: got %d want %d", name, w.Code, tc.code)
		}
	}
}

func TestUnknownAdAcceptedWhenCacheNotLoaded(t *testing.T) {
	if w := post(newSrv(&fakePub{}, fakeAds{false}), `{"ad_id":"anything"}`); w.Code != 202 {
		t.Fatalf("code=%d", w.Code)
	}
}

func TestKafkaDownReturns503WithRetryAfter(t *testing.T) {
	for _, err := range []error{errors.New("broker down"), kafka.ErrBackpressure} {
		w := post(newSrv(&fakePub{err: err}, fakeAds{true}), `{"ad_id":"ad-1"}`)
		if w.Code != 503 || w.Header().Get("Retry-After") == "" {
			t.Fatalf("err=%v code=%d", err, w.Code)
		}
	}
}

func TestBeaconReturns204(t *testing.T) {
	p := &fakePub{}
	r := httptest.NewRequest("GET", "/c/ad-1?src=email", nil)
	w := httptest.NewRecorder()
	newSrv(p, fakeAds{true}).ServeHTTP(w, r)
	if w.Code != 204 || len(p.sent) != 1 || p.sent[0].Source != "email" {
		t.Fatalf("code=%d sent=%+v", w.Code, p.sent)
	}
}
