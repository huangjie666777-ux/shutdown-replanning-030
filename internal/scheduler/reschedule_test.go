package scheduler

import (
	"context"
	"testing"
)

func baseProblem() Request {
	return Request{
		Horizon:  480,
		BudgetMs: 500,
		CrewSize: 4,
		Equipment: []Equipment{
			{ID: "E1"},
			{ID: "E2"},
		},
		Jobs: []Job{
			{ID: "A", Equipment: "E1", Duration: 60, Crew: 2, EarliestStart: 0},
			{ID: "B", Equipment: "E1", Duration: 30, Crew: 1, EarliestStart: 0, Predecessors: []string{"A"}},
			{ID: "C", Equipment: "E2", Duration: 45, Crew: 2, EarliestStart: 0},
			{ID: "D", Equipment: "E2", Duration: 30, Crew: 1, EarliestStart: 0, Predecessors: []string{"C"}},
		},
	}
}

func baseSchedule() []Assignment {
	return []Assignment{
		{ID: "A", Equipment: "E1", Start: 0, End: 60},
		{ID: "B", Equipment: "E1", Start: 60, End: 90},
		{ID: "C", Equipment: "E2", Start: 0, End: 45},
		{ID: "D", Equipment: "E2", Start: 45, End: 75},
	}
}

func TestRescheduleKeepsUntouchedJobs(t *testing.T) {
	req := &RescheduleRequest{
		Request:          baseProblem(),
		OriginalSchedule: baseSchedule(),
		NowMinute:        50,
		NewUnavailable:   []NewUnavailable{{Equipment: "E2", Start: 100, End: 120}},
	}
	if err := ValidateReschedule(req); err != nil {
		t.Fatalf("validate: %v", err)
	}
	res := Reschedule(context.Background(), req)
	if res.Status != StatusOptimal {
		t.Fatalf("status = %s, want optimal", res.Status)
	}
	// A is in progress (0..50..60), C finished at 45; both frozen.
	// B and D unaffected by the blackout: zero changes expected.
	if res.Metrics.ChangedJobs != 0 || res.Metrics.TotalStartShift != 0 {
		t.Fatalf("metrics = %+v, want no changes", res.Metrics)
	}
	for _, ch := range res.Changes {
		if ch.ID == "A" && (ch.NewStart != 0 || ch.NewEnd != 60) {
			t.Fatalf("in-progress job A moved: %+v", ch)
		}
	}
}

func TestRescheduleMovesOnlyWhatIsNeeded(t *testing.T) {
	req := &RescheduleRequest{
		Request:          baseProblem(),
		OriginalSchedule: baseSchedule(),
		NowMinute:        50,
		// Blackout covers B's original slot 60..90 on E1.
		NewUnavailable: []NewUnavailable{{Equipment: "E1", Start: 60, End: 100}},
	}
	if err := ValidateReschedule(req); err != nil {
		t.Fatalf("validate: %v", err)
	}
	res := Reschedule(context.Background(), req)
	if res.Status != StatusOptimal {
		t.Fatalf("status = %s, want optimal", res.Status)
	}
	if res.Metrics.ChangedJobs != 1 {
		t.Fatalf("changed = %d, want 1 (only B)", res.Metrics.ChangedJobs)
	}
	for _, ch := range res.Changes {
		if ch.ID == "B" {
			if ch.NewStart != 100 {
				t.Fatalf("B new start = %d, want 100", ch.NewStart)
			}
			if ch.StartDelta != 40 {
				t.Fatalf("B delta = %d, want 40", ch.StartDelta)
			}
		} else if ch.StartDelta != 0 {
			t.Fatalf("job %s moved unexpectedly: %+v", ch.ID, ch)
		}
	}
	if res.Metrics.TotalStartShift != 40 {
		t.Fatalf("shift = %d, want 40", res.Metrics.TotalStartShift)
	}
}

func TestRescheduleLockedConflictIsInfeasible(t *testing.T) {
	req := &RescheduleRequest{
		Request:          baseProblem(),
		OriginalSchedule: baseSchedule(),
		NowMinute:        50,
		NewUnavailable:   []NewUnavailable{{Equipment: "E1", Start: 60, End: 100}},
		LockedJobIDs:     []string{"B"},
	}
	if err := ValidateReschedule(req); err != nil {
		t.Fatalf("validate: %v", err)
	}
	res := Reschedule(context.Background(), req)
	if res.Status != StatusInfeasible {
		t.Fatalf("status = %s, want infeasible", res.Status)
	}
	if res.Reason == "" {
		t.Fatal("expected a conflict reason")
	}
}

func TestRescheduleLockedJobKept(t *testing.T) {
	req := &RescheduleRequest{
		Request:          baseProblem(),
		OriginalSchedule: baseSchedule(),
		NowMinute:        0,
		// Blackout hits D's slot; locking D must keep it and force infeasible.
		NewUnavailable: []NewUnavailable{{Equipment: "E2", Start: 50, End: 80}},
		LockedJobIDs:   []string{"D"},
	}
	if err := ValidateReschedule(req); err != nil {
		t.Fatalf("validate: %v", err)
	}
	res := Reschedule(context.Background(), req)
	if res.Status != StatusInfeasible {
		t.Fatalf("status = %s, want infeasible (locked D inside blackout)", res.Status)
	}
}

func TestValidateRescheduleErrors(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*RescheduleRequest)
		field string
	}{
		{"now out of range", func(r *RescheduleRequest) { r.NowMinute = 481 }, "now_minute"},
		{"unknown job in schedule", func(r *RescheduleRequest) {
			r.OriginalSchedule[0].ID = "ZZ"
		}, "original_schedule[0].id"},
		{"duplicate assignment", func(r *RescheduleRequest) {
			r.OriginalSchedule[1].ID = "A"
		}, "original_schedule[1].id"},
		{"wrong duration", func(r *RescheduleRequest) {
			r.OriginalSchedule[0].End = 55
		}, "original_schedule[0].start"},
		{"predecessor violated", func(r *RescheduleRequest) {
			r.OriginalSchedule[1].Start = 30
			r.OriginalSchedule[1].End = 60
		}, "original_schedule[1].start"},
		{"equipment overlap", func(r *RescheduleRequest) {
			r.OriginalSchedule[1].Start = 30
			r.OriginalSchedule[1].End = 60
			r.Jobs[1].Predecessors = nil
		}, "original_schedule[1]"},
		{"blackout before now", func(r *RescheduleRequest) {
			r.NowMinute = 50
			r.NewUnavailable = []NewUnavailable{{Equipment: "E1", Start: 10, End: 20}}
		}, "new_unavailable[0]"},
		{"unknown blackout equipment", func(r *RescheduleRequest) {
			r.NewUnavailable = []NewUnavailable{{Equipment: "EZ", Start: 100, End: 120}}
		}, "new_unavailable[0].equipment"},
		{"unknown locked id", func(r *RescheduleRequest) {
			r.LockedJobIDs = []string{"ZZ"}
		}, "locked_job_ids[0]"},
		{"lock started job", func(r *RescheduleRequest) {
			r.NowMinute = 50
			r.LockedJobIDs = []string{"A"}
		}, "locked_job_ids[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &RescheduleRequest{
				Request:          baseProblem(),
				OriginalSchedule: baseSchedule(),
				NowMinute:        0,
			}
			tc.mut(req)
			err := ValidateReschedule(req)
			fe, ok := err.(*FieldError)
			if !ok {
				t.Fatalf("err = %v, want FieldError", err)
			}
			if fe.Field != tc.field {
				t.Fatalf("field = %q, want %q (msg: %s)", fe.Field, tc.field, fe.Message)
			}
		})
	}
}

func TestRescheduleTimeoutStatuses(t *testing.T) {
	req := &RescheduleRequest{
		Request:          baseProblem(),
		OriginalSchedule: baseSchedule(),
		NowMinute:        0,
		NewUnavailable:   []NewUnavailable{{Equipment: "E1", Start: 0, End: 480}},
	}
	req.BudgetMs = 1
	res := Reschedule(context.Background(), req)
	switch res.Status {
	case StatusTimeoutNoSolution, StatusTimeoutFeasible, StatusInfeasible, StatusOptimal:
	default:
		t.Fatalf("unexpected status %s", res.Status)
	}
	if res.Status == StatusTimeoutNoSolution && res.Schedule != nil {
		t.Fatal("timeout_no_solution must not carry a schedule")
	}
}

func TestRescheduleCancel(t *testing.T) {
	req := &RescheduleRequest{
		Request:          baseProblem(),
		OriginalSchedule: baseSchedule(),
		NowMinute:        0,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Reschedule(ctx, req)
	if res.Status != StatusCanceled && res.Status != StatusOptimal {
		t.Fatalf("status = %s, want canceled (or optimal if it finished first)", res.Status)
	}
}
