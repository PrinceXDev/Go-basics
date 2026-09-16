package api

import (
	"net/http"
	"runtime"
	"strconv"
	"time"

	"example.com/go-basics/47_capstone/internal/store"
)

// ---------------------------------------------------------------------------
// probes
// ---------------------------------------------------------------------------

// handleHealthz is LIVENESS: "is this process wedged?" Deliberately checks
// nothing external — a failing liveness probe gets you restarted, and a
// restart does not fix a downed database.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"uptime_s":   int(time.Since(s.started).Seconds()),
		"goroutines": runtime.NumGoroutine(),
	})
}

// handleReadyz is READINESS: "should traffic come to me?" THIS is where
// dependency checks belong.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	// A trivial query proves the pool is alive and the schema is there.
	if _, _, err := s.repo.List(r.Context(), store.Filter{Limit: 1}); err != nil {
		LoggerFrom(r.Context()).Error("readiness check failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "not ready", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// ---------------------------------------------------------------------------
// tasks
// ---------------------------------------------------------------------------

type createTaskRequest struct {
	Title    string `json:"title"`
	Priority *int   `json:"priority"` // pointer so we can default it when absent
	Tag      string `json:"tag"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSON[createTaskRequest](w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", map[string]string{"reason": err.Error()})
		return
	}

	priority := 3 // the default lives here, in the transport layer
	if req.Priority != nil {
		priority = *req.Priority
	}

	// The store owns validation, so a CLI or queue consumer would get the
	// same rules for free.
	t, err := s.repo.Create(r.Context(), store.NewTask{
		Title: req.Title, Priority: priority, Tag: req.Tag,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	w.Header().Set("Location", "/tasks/"+strconv.FormatInt(t.ID, 10))
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := s.repo.Get(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// listResponse wraps the page in an envelope carrying pagination metadata.
// Returning a bare array leaves clients no room for a total or a cursor
// later without a breaking change.
type listResponse struct {
	Tasks  []store.Task `json:"tasks"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	done, err := queryBool(r, "done")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	maxPrio, err := queryInt(r, "max_priority")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	limit, err := queryInt(r, "limit")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	offset, err := queryInt(r, "offset")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}

	f := store.Filter{Done: done, Tag: r.URL.Query().Get("tag"), MaxPrio: maxPrio}
	if limit != nil {
		f.Limit = *limit
	}
	if offset != nil {
		f.Offset = *offset
	}

	tasks, total, err := s.repo.List(r.Context(), f)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}

	// Report the EFFECTIVE page size, not what the client asked for — a
	// client that requests 10,000 needs to know it got 100.
	writeJSON(w, http.StatusOK, listResponse{
		Tasks:  tasks,
		Total:  total,
		Limit:  store.ClampLimit(f.Limit),
		Offset: max(f.Offset, 0),
	})
}

// updateTaskRequest uses POINTERS throughout — this is what makes PATCH
// semantics correct. A nil field means "not supplied"; `"done": false`
// means "set it to false". With plain bools the two are indistinguishable
// and you can never un-set anything.
type updateTaskRequest struct {
	Title    *string `json:"title"`
	Done     *bool   `json:"done"`
	Priority *int    `json:"priority"`
	Tag      *string `json:"tag"`
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSON[updateTaskRequest](w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", map[string]string{"reason": err.Error()})
		return
	}
	if req.Title == nil && req.Done == nil && req.Priority == nil && req.Tag == nil {
		writeError(w, http.StatusBadRequest, "no fields to update", nil)
		return
	}

	t, err := s.repo.Update(r.Context(), id, store.Patch{
		Title: req.Title, Done: req.Done, Priority: req.Priority, Tag: req.Tag,
	})
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.repo.Delete(r.Context(), id); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent) // 204: no body at all
}
