// Package httpapi exposes click ingestion and the analytics query API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"adclick/internal/event"
	"adclick/internal/kafka"
	"adclick/internal/metrics"
	"adclick/internal/store"
)

type Publisher interface {
	Publish(ctx context.Context, c event.Click) error
}

type Queries interface {
	Summary(ctx context.Context, from, to time.Time) (store.Summary, error)
	Timeseries(ctx context.Context, gran string, from, to time.Time, campaignID, adID string) ([]store.Point, error)
	TopAds(ctx context.Context, from, to time.Time, limit int) ([]store.AdStat, error)
	Campaigns(ctx context.Context, from, to time.Time) ([]store.CampaignStat, error)
	Breakdown(ctx context.Context, dim string, from, to time.Time) ([]store.Slice, error)
	AddImpressions(ctx context.Context, adID string, n int64, at time.Time) error
}

type AdLookup interface {
	Lookup(adID string) (store.AdInfo, bool)
	Loaded() bool
}

type Deps struct {
	Pub     Publisher
	Queries Queries
	Ads     AdLookup
	Metrics *metrics.Metrics
	Log     *slog.Logger
	Salt    string
	Limits  event.Limits
	Ready   func(ctx context.Context) error // dependency check for /readyz
	Now     func() time.Time
}

type Server struct{ Deps }

func New(d Deps) *Server {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Server{d}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/clicks", s.postClick)
	mux.HandleFunc("GET /c/{ad_id}", s.trackClick)
	mux.HandleFunc("POST /v1/impressions", s.postImpressions)

	mux.HandleFunc("GET /api/v1/stats/summary", s.summary)
	mux.HandleFunc("GET /api/v1/stats/timeseries", s.timeseries)
	mux.HandleFunc("GET /api/v1/stats/top-ads", s.topAds)
	mux.HandleFunc("GET /api/v1/stats/campaigns", s.campaigns)
	mux.HandleFunc("GET /api/v1/stats/countries", s.breakdown("country"))
	mux.HandleFunc("GET /api/v1/stats/devices", s.breakdown("device_type"))
	mux.HandleFunc("GET /api/v1/stream", s.stream)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", s.Metrics.Handler())
	return s.middleware(mux)
}

// Click ingestion -----------------------------------------------------------

type clickRequest struct {
	EventID    string     `json:"event_id"` // optional client idempotency key
	AdID       string     `json:"ad_id"`
	UserID     string     `json:"user_id"`
	ClickedAt  *time.Time `json:"clicked_at"`
	Country    string     `json:"country"`
	DeviceType string     `json:"device_type"`
	Browser    string     `json:"browser"`
	Source     string     `json:"source"`
}

func (s *Server) postClick(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req clickRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	s.ingest(w, r, req)
}

// trackClick is the beacon form: GET /c/{ad_id}?src=email. It records the click
// and returns 204 (no redirect, so there is no open-redirect risk).
func (s *Server) trackClick(w http.ResponseWriter, r *http.Request) {
	s.ingest(w, r, clickRequest{AdID: r.PathValue("ad_id"), Source: r.URL.Query().Get("src"), UserID: r.URL.Query().Get("uid")})
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request, req clickRequest) {
	now := s.Now().UTC()
	ua := r.UserAgent()
	c := event.Click{
		EventID:    req.EventID,
		AdID:       req.AdID,
		UserID:     req.UserID,
		Country:    firstNonEmpty(strings.ToUpper(req.Country), strings.ToUpper(r.Header.Get("X-Country")), "ZZ"),
		DeviceType: firstNonEmpty(req.DeviceType, deviceFromUA(ua)),
		Browser:    firstNonEmpty(req.Browser, browserFromUA(ua)),
		Source:     firstNonEmpty(req.Source, "direct"),
		IPHash:     event.HashIP(s.Salt, clientIP(r)),
		ClickedAt:  now,
	}
	if req.ClickedAt != nil {
		c.ClickedAt = req.ClickedAt.UTC()
	}
	if c.EventID == "" {
		c.EventID = event.NewID()
	}
	if err := c.Validate(now, s.Limits); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reject unknown ads early, but only if the cache has data. If Postgres was
	// down at startup we still accept clicks; the consumer will check later.
	if s.Ads != nil && s.Ads.Loaded() {
		info, ok := s.Ads.Lookup(c.AdID)
		if !ok {
			writeErr(w, http.StatusNotFound, "unknown ad_id")
			return
		}
		c.CampaignID, c.AdvertiserID = info.CampaignID, info.AdvertiserID
	}

	// Respond only AFTER Kafka acknowledged the write (acks=all).
	// 202 means "durably accepted", never "maybe".
	if err := s.Pub.Publish(r.Context(), c); err != nil {
		s.Metrics.Produced.WithLabelValues("error").Inc()
		s.Log.Error("publish failed", "err", err, "event_id", c.EventID)
		w.Header().Set("Retry-After", "1")
		if errors.Is(err, kafka.ErrBackpressure) {
			writeErr(w, http.StatusServiceUnavailable, "overloaded, retry shortly")
		} else {
			writeErr(w, http.StatusServiceUnavailable, "event pipeline unavailable, retry with the same event_id")
		}
		return
	}
	s.Metrics.Produced.WithLabelValues("ok").Inc()

	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"event_id": c.EventID})
}

func (s *Server) postImpressions(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req struct {
		AdID  string `json:"ad_id"`
		Count int64  `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AdID == "" || req.Count < 1 || req.Count > 1_000_000 {
		writeErr(w, http.StatusBadRequest, "need ad_id and count 1..1000000")
		return
	}
	if err := s.Queries.AddImpressions(r.Context(), req.AdID, req.Count, s.Now()); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.Ready != nil {
		if err := s.Ready(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// helpers -------------------------------------------------------------------

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func itoa(n int) string { return strconv.Itoa(n) }
