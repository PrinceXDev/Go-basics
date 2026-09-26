// Package api wires HTTP handlers to the registry and hub. It's the layer
// that would matter most in production: this is where a real server's
// health-check agent would POST its stats.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"serverdashboard/internal/hub"
	"serverdashboard/internal/registry"
)

type API struct {
	reg *registry.Registry
	hub *hub.Hub
}

func New(reg *registry.Registry, h *hub.Hub) *API {
	return &API{reg: reg, hub: h}
}

func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/servers", a.listServers)
	mux.HandleFunc("POST /api/servers/{id}/health", a.ingestHealth)
	mux.HandleFunc("GET /ws", a.serveWS)
}

// listServers is the REST fallback / initial page load path: a plain
// snapshot, no WebSocket required.
func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.reg.Snapshot())
}

// healthReport is what a real server's agent would POST every few seconds.
type healthReport struct {
	Status      registry.Status `json:"status"`
	CPUPercent  float64         `json:"cpuPercent"`
	MemPercent  float64         `json:"memPercent"`
	RequestRate float64         `json:"requestRate"`
}

// ingestHealth is the production entry point this demo's simulator
// bypasses: POST /api/servers/{id}/health {"status":"healthy","cpuPercent":42.1,...}
// It updates the shared registry and immediately fans the update out over
// the hub, so every open dashboard tab sees it within one broadcast cycle
// (no polling delay).
func (a *API) ingestHealth(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing server id", http.StatusBadRequest)
		return
	}

	var report healthReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	stats := registry.ServerStats{
		ID:          id,
		Status:      report.Status,
		CPUPercent:  report.CPUPercent,
		MemPercent:  report.MemPercent,
		RequestRate: report.RequestRate,
		UpdatedAt:   time.Now(),
	}

	a.IngestStats(stats)

	w.WriteHeader(http.StatusAccepted)
}

// serveWS is what the React dashboard connects to for live updates.
func (a *API) serveWS(w http.ResponseWriter, r *http.Request) {
	initial, err := json.Marshal(snapshotMessage{Type: "snapshot", Servers: a.reg.Snapshot()})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := hub.ServeWS(a.hub, w, r, initial); err != nil {
		slog.Error("websocket upgrade failed", "err", err)
	}
}

// IngestStats records a server's latest stats and broadcasts the update to
// every connected dashboard. It's the single choke point both the real
// HTTP ingest handler and the in-process simulator go through, so there is
// exactly one place that decides how a stats update becomes a WebSocket
// message. The dashboard distinguishes "snapshot" (full replace, sent once
// on connect) from "update" (merge one server) by the Type field.
func (a *API) IngestStats(s registry.ServerStats) {
	a.reg.Upsert(s)

	msg, err := json.Marshal(updateMessage{Type: "update", Server: s})
	if err != nil {
		slog.Error("marshal update", "err", err)
		return
	}
	a.hub.Broadcast(msg)
}

type snapshotMessage struct {
	Type    string                 `json:"type"`
	Servers []registry.ServerStats `json:"servers"`
}

type updateMessage struct {
	Type   string               `json:"type"`
	Server registry.ServerStats `json:"server"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// CORS is deliberately permissive here (Vite dev server on a different
// port than the Go API) — lock this down to a real allowlist in
// production.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
