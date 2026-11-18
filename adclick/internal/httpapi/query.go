package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const maxRange = 400 * 24 * time.Hour

func (s *Server) parseRange(r *http.Request) (from, to time.Time, err error) {
	now := s.Now().UTC()
	to, from = now.Add(time.Minute), now.Add(-24*time.Hour)
	q := r.URL.Query()
	if v := q.Get("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			return from, to, fmt.Errorf("from must be RFC3339")
		}
	}
	if v := q.Get("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			return from, to, fmt.Errorf("to must be RFC3339")
		}
	}
	if !from.Before(to) || to.Sub(from) > maxRange {
		return from, to, fmt.Errorf("invalid range (from < to, max %d days)", int(maxRange.Hours()/24))
	}
	return from, to, nil
}

// queryHandler removes the repeated parse-range / error / JSON boilerplate.
func (s *Server) queryHandler(fn func(r *http.Request, from, to time.Time) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		from, to, err := s.parseRange(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, err := fn(r, from, to)
		if err != nil {
			s.Log.Error("query failed", "path", r.URL.Path, "err", err)
			writeErr(w, http.StatusServiceUnavailable, "analytics temporarily unavailable")
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	s.queryHandler(func(r *http.Request, from, to time.Time) (any, error) {
		return s.Queries.Summary(r.Context(), from, to)
	})(w, r)
}

func (s *Server) timeseries(w http.ResponseWriter, r *http.Request) {
	s.queryHandler(func(r *http.Request, from, to time.Time) (any, error) {
		q := r.URL.Query()
		g := q.Get("granularity")
		if g == "" {
			g = "hour"
		}
		return s.Queries.Timeseries(r.Context(), g, from, to, q.Get("campaign_id"), q.Get("ad_id"))
	})(w, r)
}

func (s *Server) topAds(w http.ResponseWriter, r *http.Request) {
	s.queryHandler(func(r *http.Request, from, to time.Time) (any, error) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit < 1 || limit > 100 {
			limit = 10
		}
		return s.Queries.TopAds(r.Context(), from, to, limit)
	})(w, r)
}

func (s *Server) campaigns(w http.ResponseWriter, r *http.Request) {
	s.queryHandler(func(r *http.Request, from, to time.Time) (any, error) {
		return s.Queries.Campaigns(r.Context(), from, to)
	})(w, r)
}

func (s *Server) breakdown(dim string) http.HandlerFunc {
	return s.queryHandler(func(r *http.Request, from, to time.Time) (any, error) {
		return s.Queries.Breakdown(r.Context(), dim, from, to)
	})
}

// stream pushes the live summary over Server-Sent Events every 2 seconds.
// SSE is one-way, works over plain HTTP and reconnects by itself in the browser.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	send := func() bool {
		now := s.Now().UTC()
		sum, err := s.Queries.Summary(r.Context(), now.Add(-time.Hour), now.Add(time.Minute))
		if err != nil {
			fmt.Fprint(w, "event: error\ndata: {\"error\":\"unavailable\"}\n\n")
			fl.Flush()
			return r.Context().Err() == nil
		}
		b, _ := jsonMarshal(sum)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
		return true
	}
	if !send() {
		return
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			if !send() {
				return
			}
		}
	}
}
