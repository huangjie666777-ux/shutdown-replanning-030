package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/huangjie666777-ux/shutdown-scheduler-025/internal/scheduler"
)

// NewRouter builds the HTTP handler with health and schedule endpoints.
func NewRouter() http.Handler {
	router := chi.NewRouter()
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	router.Post("/schedule", handleSchedule)
	return router
}

func handleSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduler.Request
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"field": "body", "message": "invalid JSON: " + err.Error()},
		})
		return
	}
	if err := scheduler.Validate(&req); err != nil {
		var fe *scheduler.FieldError
		if errors.As(err, &fe) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": fe})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"field": "body", "message": err.Error()},
		})
		return
	}
	// The request context cancels the search if the client goes away.
	res := scheduler.Solve(r.Context(), &req)
	writeJSON(w, http.StatusOK, res)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
