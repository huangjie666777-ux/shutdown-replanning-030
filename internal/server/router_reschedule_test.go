package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postReschedule(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/reschedule", strings.NewReader(body))
	NewRouter().ServeHTTP(rec, req)
	return rec
}

const rescheduleBody = `{
  "budget_ms": 1000,
  "t_minutes": 30,
  "problem": {
    "horizon_minutes": 200,
    "crew_size": 3,
    "equipment": [{"id": "E1"}, {"id": "E2"}],
    "jobs": [
      {"id": "A", "equipment": "E1", "duration_minutes": 20, "crew": 1, "earliest_start": 0, "predecessors": []},
      {"id": "B", "equipment": "E1", "duration_minutes": 20, "crew": 1, "earliest_start": 0, "predecessors": ["A"]},
      {"id": "C", "equipment": "E2", "duration_minutes": 20, "crew": 1, "earliest_start": 0, "predecessors": ["B"]}
    ]
  },
  "original_schedule": [
    {"id": "A", "equipment": "E1", "start": 0, "end": 20},
    {"id": "B", "equipment": "E1", "start": 20, "end": 40},
    {"id": "C", "equipment": "E2", "start": 40, "end": 60}
  ],
  "new_unavailable": [
    {"equipment": "E2", "intervals": [{"start": 40, "end": 55}]}
  ]
}`

func TestRescheduleOK(t *testing.T) {
	rec := postReschedule(t, rescheduleBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"status":"optimal"`) {
		t.Fatalf("body=%s", body)
	}
	if !strings.Contains(body, `"changed_jobs":1`) {
		t.Fatalf("body=%s", body)
	}
	if !strings.Contains(body, `"start":55`) {
		t.Fatalf("C should move to 55: %s", body)
	}
}

func TestRescheduleLockConflict(t *testing.T) {
	body := strings.Replace(rescheduleBody, `  ]
}`, `  ],
  "locked_job_ids": ["C"]
}`, 1)
	rec := postReschedule(t, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"status":"infeasible"`) {
		t.Fatalf("body=%s", rec.Body)
	}
}

func TestRescheduleFieldError(t *testing.T) {
	body := strings.Replace(rescheduleBody, `"t_minutes": 30`, `"t_minutes": 999`, 1)
	rec := postReschedule(t, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"field":"t_minutes"`) {
		t.Fatalf("body=%s", rec.Body)
	}
}
