package order

import (
	"errors"
	"net/http"
	"strconv"

	"go-crud/internal/httpx"
	"go-crud/internal/product"
	"go-crud/internal/user"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /orders", h.create)
	mux.HandleFunc("GET /orders", h.list)
	mux.HandleFunc("GET /orders/{id}", h.get)
	mux.HandleFunc("PATCH /orders/{id}/status", h.updateStatus)
	mux.HandleFunc("DELETE /orders/{id}", h.delete)
	mux.HandleFunc("GET /users/{id}/orders", h.listByUser)
}

type itemRequest struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type createRequest struct {
	UserID int64         `json:"user_id"`
	Items  []itemRequest `json:"items"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	items := make([]ItemInput, len(req.Items))
	for i, it := range req.Items {
		items[i] = ItemInput{ProductID: it.ProductID, Quantity: it.Quantity}
	}
	o, err := h.svc.Create(req.UserID, items)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, o)
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) {
	orders, err := h.svc.List()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to list orders")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orders)
}

func (h *Handler) listByUser(w http.ResponseWriter, r *http.Request) {
	userID, err := parseID(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	orders, err := h.svc.ListByUser(userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "failed to list orders")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, orders)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	o, err := h.svc.Get(id)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
}

type statusRequest struct {
	Status string `json:"status"`
}

func (h *Handler) updateStatus(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req statusRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	o, err := h.svc.UpdateStatus(id, req.Status)
	if err != nil {
		writeServiceErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.Delete(id); err != nil {
		writeServiceErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseID(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func writeServiceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, user.ErrNotFound), errors.Is(err, product.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrValidation), errors.Is(err, ErrNoItems), errors.Is(err, ErrInvalidStatus):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}
