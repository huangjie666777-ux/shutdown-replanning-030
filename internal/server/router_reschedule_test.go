package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const rescheduleBody = `{
  "horizon_minutes": 480,
  "budget_ms": 200,
  "crew_size": 4,
  "equipment": [{"id": "E1", "unavailable": []}, {"id": "E2", "unavailable": []}],
  "jobs": [
    {"id": "A", "equipment": "E1", "duration_minutes": 60, "crew": 2, "earliest_start": 0, "predecessors": []},
    {"id": "B", "equipment": "E1", "duration_minutes": 30, "crew": 1, "earliest_start": 0, "predecessors": ["A"]},
    {"id": "C", "equipment": "E2", "duration_minutes": 45, "crew": 2, "earliest_start": 0, "predecessors": []}
  ],
  "original_schedule": [
    {"id": "A", "equipment": "E1", "start": 0, "end": 60},
    {"id": "B", "equipment": "E1", "start": 60, "end": 90},
    {"id": "C", "equipment": "E2", "start": 0, "end": 45}
  ],
  "now_minute": 50,
  "new_unavailable": [{"equipment": "E1", "start": 60, "end": 100}]
}`

func TestRescheduleEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/reschedule", strings.NewReader(rescheduleBody))
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Status  string `json:"status"`
		Metrics struct {
			ChangedJobs     int `json:"changed_jobs"`
			TotalStartShift int `json:"total_start_shift_minutes"`
		} `json:"metrics"`
		Changes []struct {
			ID       string `json:"id"`
			NewStart int    `json:"new_start"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "optimal" {
		t.Fatalf("status = %s", res.Status)
	}
	if res.Metrics.ChangedJobs != 1 || res.Metrics.TotalStartShift != 40 {
		t.Fatalf("metrics = %+v, want 1 moved job shifted by 40", res.Metrics)
	}
	for _, ch := range res.Changes {
		if ch.ID == "B" && ch.NewStart != 100 {
			t.Fatalf("B new start = %d, want 100", ch.NewStart)
		}
	}
}

func TestRescheduleEndpointLockedConflict(t *testing.T) {
	body := strings.Replace(rescheduleBody, `"now_minute": 50`,
		`"now_minute": 50, "locked_job_ids": ["B"]`, 1)
	req := httptest.NewRequest(http.MethodPost, "/reschedule", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "infeasible" || res.Reason == "" {
		t.Fatalf("got %+v, want infeasible with reason", res)
	}
}

func TestRescheduleEndpointValidation(t *testing.T) {
	body := strings.Replace(rescheduleBody, `"now_minute": 50`, `"now_minute": 999`, 1)
	req := httptest.NewRequest(http.MethodPost, "/reschedule", strings.NewReader(body))
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Error struct {
			Field string `json:"field"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Error.Field != "now_minute" {
		t.Fatalf("field = %s, want now_minute", res.Error.Field)
	}
}
