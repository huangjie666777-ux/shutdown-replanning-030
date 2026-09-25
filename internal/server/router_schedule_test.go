package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/schedule", strings.NewReader(body))
	NewRouter().ServeHTTP(rec, req)
	return rec
}

func TestScheduleOK(t *testing.T) {
	rec := post(t, `{
	  "horizon_minutes": 120, "budget_ms": 500, "crew_size": 2,
	  "equipment": [{"id": "E1", "unavailable": [{"start": 0, "end": 10}]}],
	  "jobs": [{"id": "A", "equipment": "E1", "duration_minutes": 10, "crew": 1, "earliest_start": 0, "predecessors": []}]
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"status":"optimal"`) {
		t.Fatalf("body=%s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"start":10`) {
		t.Fatalf("expected job pushed after outage, body=%s", rec.Body)
	}
}

func TestScheduleValidationError(t *testing.T) {
	rec := post(t, `{"horizon_minutes": 999, "budget_ms": 10, "crew_size": 1, "jobs": []}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"field":"horizon_minutes"`) {
		t.Fatalf("body=%s", rec.Body)
	}
}

func TestScheduleBadJSON(t *testing.T) {
	rec := post(t, `{"horizon_minutes":`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}
