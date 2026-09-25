package scheduler

import (
	"context"
	"strings"
	"testing"
)

func reschedBase() *RescheduleRequest {
	return &RescheduleRequest{
		BudgetMs: 2000,
		T:        30,
		Problem: Problem{
			Horizon:  200,
			CrewSize: 3,
			Equipment: []Equipment{
				{ID: "E1"},
				{ID: "E2"},
			},
			Jobs: []Job{
				{ID: "A", Equipment: "E1", Duration: 20, Crew: 1, EarliestStart: 0},
				{ID: "B", Equipment: "E1", Duration: 20, Crew: 1, EarliestStart: 0, Predecessors: []string{"A"}},
				{ID: "C", Equipment: "E2", Duration: 20, Crew: 1, EarliestStart: 0, Predecessors: []string{"B"}},
			},
		},
		OriginalSchedule: []Assignment{
			{ID: "A", Equipment: "E1", Start: 0, End: 20},
			{ID: "B", Equipment: "E1", Start: 20, End: 40},
			{ID: "C", Equipment: "E2", Start: 40, End: 60},
		},
	}
}

func startsOf(res RescheduleResult) map[string]int {
	m := map[string]int{}
	for _, a := range res.Schedule {
		m[a.ID] = a.Start
	}
	return m
}

func TestRescheduleKeepAll(t *testing.T) {
	req := reschedBase()
	// Outage after everything finishes: original plan must be kept untouched.
	req.NewUnavailable = []OutageAddition{{EquipmentID: "E1", Intervals: []Interval{{Start: 100, End: 120}}}}
	res := Resolve(context.Background(), req)
	if res.Status != StatusOptimal {
		t.Fatalf("status=%s", res.Status)
	}
	if res.ChangedJobs != 0 || res.TotalStartShift != 0 {
		t.Fatalf("metrics: changed=%d shift=%d", res.ChangedJobs, res.TotalStartShift)
	}
	st := startsOf(res)
	if st["A"] != 0 || st["B"] != 20 || st["C"] != 40 {
		t.Fatalf("schedule moved: %+v", st)
	}
	for _, ch := range res.Changes {
		if ch.State == StateCompleted && ch.Changed {
			t.Fatalf("completed job reported changed: %+v", ch)
		}
	}
}

func TestRescheduleMinPerturbation(t *testing.T) {
	req := reschedBase()
	// B is in progress at T=30 on E1; E1 gets an outage [50,70) that the
	// in-progress remainder [30,40) survives. Nothing else needs to move.
	req.NewUnavailable = []OutageAddition{{EquipmentID: "E1", Intervals: []Interval{{Start: 50, End: 70}}}}
	res := Resolve(context.Background(), req)
	if res.Status != StatusOptimal {
		t.Fatalf("status=%s", res.Status)
	}
	st := startsOf(res)
	if st["A"] != 0 || st["B"] != 20 || st["C"] != 40 {
		t.Fatalf("expected zero moves, got %+v", st)
	}

	// Outage [35,60) collides with B's in-progress remainder: infeasible.
	req2 := reschedBase()
	req2.NewUnavailable = []OutageAddition{{EquipmentID: "E1", Intervals: []Interval{{Start: 35, End: 60}}}}
	res2 := Resolve(context.Background(), req2)
	if res2.Status != StatusInfeasible || res2.Schedule != nil {
		t.Fatalf("status=%s schedule=%v", res2.Status, res2.Schedule)
	}

	// Outage blocks C's original slot on E2; C must shift, B must stay.
	req3 := reschedBase()
	req3.NewUnavailable = []OutageAddition{{EquipmentID: "E2", Intervals: []Interval{{Start: 40, End: 55}}}}
	res3 := Resolve(context.Background(), req3)
	if res3.Status != StatusOptimal {
		t.Fatalf("status=%s", res3.Status)
	}
	st3 := startsOf(res3)
	if st3["B"] != 20 {
		t.Fatalf("in-progress/frozen job B moved: %d", st3["B"])
	}
	if st3["C"] != 55 {
		t.Fatalf("C should move to 55, got %d", st3["C"])
	}
	if res3.ChangedJobs != 1 || res3.TotalStartShift != 15 {
		t.Fatalf("metrics: changed=%d shift=%d", res3.ChangedJobs, res3.TotalStartShift)
	}
}

func TestRescheduleDoesNotShortenAtCostOfChange(t *testing.T) {
	// Two independent jobs at T=0; outage delays A by 10. B is unaffected and
	// starting B earlier is impossible, so only A moves. This also guards the
	// strict objective priority: no extra changes to chase a shorter makespan.
	req := &RescheduleRequest{
		BudgetMs: 2000,
		T:        0,
		Problem: Problem{
			Horizon:   200,
			CrewSize:  2,
			Equipment: []Equipment{{ID: "E1"}, {ID: "E2"}},
			Jobs: []Job{
				{ID: "A", Equipment: "E1", Duration: 30, Crew: 1, EarliestStart: 0},
				{ID: "B", Equipment: "E2", Duration: 10, Crew: 1, EarliestStart: 50},
			},
		},
		OriginalSchedule: []Assignment{
			{ID: "A", Equipment: "E1", Start: 0, End: 30},
			{ID: "B", Equipment: "E2", Start: 50, End: 60},
		},
		NewUnavailable: []OutageAddition{{EquipmentID: "E1", Intervals: []Interval{{Start: 0, End: 10}}}},
	}
	res := Resolve(context.Background(), req)
	if res.Status != StatusOptimal {
		t.Fatalf("status=%s", res.Status)
	}
	st := startsOf(res)
	if st["A"] != 10 || st["B"] != 50 {
		t.Fatalf("unexpected plan: %+v", st)
	}
	if res.ChangedJobs != 1 || res.TotalStartShift != 10 {
		t.Fatalf("metrics changed=%d shift=%d", res.ChangedJobs, res.TotalStartShift)
	}
}

func TestRescheduleLockConflict(t *testing.T) {
	req := reschedBase()
	req.LockedJobIDs = []string{"C"}
	req.NewUnavailable = []OutageAddition{{EquipmentID: "E2", Intervals: []Interval{{Start: 45, End: 55}}}}
	res := Resolve(context.Background(), req)
	if res.Status != StatusInfeasible {
		t.Fatalf("locked job conflicts with outage, status=%s", res.Status)
	}
	if res.Schedule != nil {
		t.Fatalf("conflict must not return a schedule")
	}

	// Same outage, but C unlocked: it moves and the result is feasible.
	req.LockedJobIDs = nil
	res2 := Resolve(context.Background(), req)
	if res2.Status != StatusOptimal {
		t.Fatalf("status=%s", res2.Status)
	}
	if startsOf(res2)["C"] != 55 {
		t.Fatalf("C should shift to 55")
	}
}

func TestRescheduleLockFinishedRejected(t *testing.T) {
	req := reschedBase()
	req.LockedJobIDs = []string{"A"} // A finished at 20, T=30
	err := ValidateReschedule(req)
	if err == nil || !strings.Contains(err.Error(), "locked_job_ids[0]") {
		t.Fatalf("err=%v", err)
	}
}

func TestRescheduleValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*RescheduleRequest)
		field  string
	}{
		{"bad budget", func(r *RescheduleRequest) { r.BudgetMs = 0 }, "budget_ms"},
		{"bad nested problem", func(r *RescheduleRequest) { r.Problem.Horizon = 999 }, "problem.horizon_minutes"},
		{"t out of range", func(r *RescheduleRequest) { r.T = 999 }, "t_minutes"},
		{"missing schedule entry", func(r *RescheduleRequest) { r.OriginalSchedule = r.OriginalSchedule[:2] }, "original_schedule"},
		{"unknown job in schedule", func(r *RescheduleRequest) { r.OriginalSchedule[0].ID = "ZZ" }, "original_schedule[0].id"},
		{"wrong end", func(r *RescheduleRequest) { r.OriginalSchedule[0].End = 99 }, "original_schedule[0].end"},
		{"bad equipment", func(r *RescheduleRequest) { r.OriginalSchedule[0].Equipment = "E2" }, "original_schedule[0].equipment"},
		{"overlapping jobs", func(r *RescheduleRequest) {
			r.OriginalSchedule[1].Start = 10
			r.OriginalSchedule[1].End = 30
		}, "original_schedule[1].start"},
		{"predecessor violation", func(r *RescheduleRequest) {
			r.OriginalSchedule[2].Start = 10
			r.OriginalSchedule[2].End = 30
		}, "original_schedule[2].start"},
		{"outage before T", func(r *RescheduleRequest) {
			r.NewUnavailable = []OutageAddition{{EquipmentID: "E1", Intervals: []Interval{{Start: 10, End: 20}}}}
		}, "new_unavailable[0].intervals[0].start"},
		{"unknown outage equipment", func(r *RescheduleRequest) {
			r.NewUnavailable = []OutageAddition{{EquipmentID: "ZZ", Intervals: []Interval{{Start: 30, End: 40}}}}
		}, "new_unavailable[0].equipment"},
		{"unknown lock", func(r *RescheduleRequest) { r.LockedJobIDs = []string{"ZZ"} }, "locked_job_ids[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := reschedBase()
			tc.mutate(req)
			err := ValidateReschedule(req)
			fe, ok := err.(*FieldError)
			if !ok {
				t.Fatalf("expected FieldError, got %v", err)
			}
			if fe.Field != tc.field {
				t.Fatalf("field=%q want %q (%s)", fe.Field, tc.field, fe.Message)
			}
		})
	}
}

func TestRescheduleCanceled(t *testing.T) {
	req := reschedBase()
	req.BudgetMs = 60000
	req.Problem.Horizon = MaxHorizon
	// Replace with many long jobs on one equipment to create a large search.
	req.Problem.Equipment = []Equipment{{ID: "E1"}}
	req.Problem.Jobs = nil
	req.OriginalSchedule = nil
	for i := 0; i < MaxJobs; i++ {
		id := string(rune('A' + i))
		req.Problem.Jobs = append(req.Problem.Jobs, Job{
			ID: id, Equipment: "E1", Duration: 31, Crew: 1, EarliestStart: 0,
		})
		req.OriginalSchedule = append(req.OriginalSchedule, Assignment{
			ID: id, Equipment: "E1", Start: i * 31, End: (i + 1) * 31,
		})
	}
	req.NewUnavailable = []OutageAddition{{EquipmentID: "E1", Intervals: []Interval{{Start: MaxHorizon - 10, End: MaxHorizon}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { cancel() }()
	res := Resolve(ctx, req)
	if res.Status != StatusCanceled {
		t.Fatalf("status=%s", res.Status)
	}
}
