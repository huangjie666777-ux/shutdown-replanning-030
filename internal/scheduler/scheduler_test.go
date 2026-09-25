package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"
)

func baseRequest() *Request {
	return &Request{
		Horizon:  120,
		BudgetMs: 2000,
		CrewSize: 3,
		Equipment: []Equipment{
			{ID: "E1", Unavailable: []Interval{{Start: 30, End: 40}}},
			{ID: "E2"},
		},
		Jobs: []Job{
			{ID: "A", Equipment: "E1", Duration: 20, Crew: 2, EarliestStart: 0},
			{ID: "B", Equipment: "E2", Duration: 10, Crew: 2, EarliestStart: 0, Predecessors: []string{"A"}},
		},
	}
}

func TestValidateOK(t *testing.T) {
	if err := Validate(baseRequest()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateFieldErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Request)
		field  string
	}{
		{"horizon too big", func(r *Request) { r.Horizon = 481 }, "horizon_minutes"},
		{"zero budget", func(r *Request) { r.BudgetMs = 0 }, "budget_ms"},
		{"zero crew", func(r *Request) { r.CrewSize = 0 }, "crew_size"},
		{"dup equipment", func(r *Request) { r.Equipment = append(r.Equipment, Equipment{ID: "E1"}) }, "equipment[2].id"},
		{"bad interval", func(r *Request) { r.Equipment[0].Unavailable = []Interval{{Start: 10, End: 10}} }, "equipment[0].unavailable[0]"},
		{"too many jobs", func(r *Request) {
			for i := 0; i < 12; i++ {
				r.Jobs = append(r.Jobs, Job{ID: strings.Repeat("x", i+1), Equipment: "E1", Duration: 1, Crew: 1})
			}
		}, "jobs"},
		{"dup job", func(r *Request) { r.Jobs = append(r.Jobs, r.Jobs[0]) }, "jobs[2].id"},
		{"unknown equipment", func(r *Request) { r.Jobs[0].Equipment = "ZZ" }, "jobs[0].equipment"},
		{"bad duration", func(r *Request) { r.Jobs[0].Duration = 0 }, "jobs[0].duration_minutes"},
		{"crew exceeds size", func(r *Request) { r.Jobs[0].Crew = 4 }, "jobs[0].crew"},
		{"bad earliest", func(r *Request) { r.Jobs[0].EarliestStart = -1 }, "jobs[0].earliest_start"},
		{"unknown pred", func(r *Request) { r.Jobs[0].Predecessors = []string{"ZZ"} }, "jobs[0].predecessors[0]"},
		{"self pred", func(r *Request) { r.Jobs[0].Predecessors = []string{"A"} }, "jobs[0].predecessors[0]"},
		{"cycle", func(r *Request) { r.Jobs[0].Predecessors = []string{"B"} }, "jobs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest()
			tc.mutate(req)
			err := Validate(req)
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

func TestSolveOptimalRespectsConstraints(t *testing.T) {
	res := Solve(context.Background(), baseRequest())
	if res.Status != StatusOptimal {
		t.Fatalf("status=%s", res.Status)
	}
	if *res.Makespan != 30 {
		t.Fatalf("makespan=%d want 30", *res.Makespan)
	}
	// A must end before the E1 outage at 30, B starts at 20 on E2.
	if res.Schedule[0].Start != 0 || res.Schedule[0].End != 20 {
		t.Fatalf("A placement: %+v", res.Schedule[0])
	}
	if res.Schedule[1].Start != 20 {
		t.Fatalf("B placement: %+v", res.Schedule[1])
	}
}

func TestSolveWaitsForBetterPlan(t *testing.T) {
	// Greedy-by-ID would start A at 0 on E1 (crew 2), blocking B (crew 2,
	// crew_size 3) until 50. Waiting lets B run first and finish sooner.
	req := &Request{
		Horizon:   200,
		BudgetMs:  2000,
		CrewSize:  3,
		Equipment: []Equipment{{ID: "E1"}, {ID: "E2"}},
		Jobs: []Job{
			{ID: "A", Equipment: "E1", Duration: 50, Crew: 2, EarliestStart: 0},
			{ID: "B", Equipment: "E2", Duration: 10, Crew: 2, EarliestStart: 0},
			{ID: "C", Equipment: "E2", Duration: 10, Crew: 1, EarliestStart: 0, Predecessors: []string{"B"}},
		},
	}
	res := Solve(context.Background(), req)
	if res.Status != StatusOptimal {
		t.Fatalf("status=%s", res.Status)
	}
	// Optimal makespan is 60 regardless; check it is found and feasible.
	if *res.Makespan != 60 {
		t.Fatalf("makespan=%d want 60", *res.Makespan)
	}
	assertFeasible(t, req, res)
}

func TestSolveTieBreakLexicographic(t *testing.T) {
	// Two independent jobs, plenty of crew: both can start at 0.
	req := &Request{
		Horizon:   100,
		BudgetMs:  2000,
		CrewSize:  4,
		Equipment: []Equipment{{ID: "E1"}, {ID: "E2"}},
		Jobs: []Job{
			{ID: "A", Equipment: "E1", Duration: 10, Crew: 1, EarliestStart: 0},
			{ID: "B", Equipment: "E2", Duration: 10, Crew: 1, EarliestStart: 0},
		},
	}
	res := Solve(context.Background(), req)
	if res.Status != StatusOptimal || *res.Makespan != 10 {
		t.Fatalf("status=%s makespan=%v", res.Status, res.Makespan)
	}
	for _, a := range res.Schedule {
		if a.Start != 0 {
			t.Fatalf("expected earliest lexicographic plan, got %+v", res.Schedule)
		}
	}
}

func TestSolveInfeasible(t *testing.T) {
	req := baseRequest()
	// E1 is down for the whole horizon; job A can never run.
	req.Equipment[0].Unavailable = []Interval{{Start: 0, End: 120}}
	res := Solve(context.Background(), req)
	if res.Status != StatusInfeasible || res.Makespan != nil {
		t.Fatalf("status=%s makespan=%v", res.Status, res.Makespan)
	}
}

func TestSolveTimeoutNoSolution(t *testing.T) {
	req := &Request{
		Horizon:   MaxHorizon,
		BudgetMs:  1, // effectively no time to finish the search
		CrewSize:  2,
		Equipment: []Equipment{{ID: "E1"}},
	}
	for i := 0; i < MaxJobs; i++ {
		req.Jobs = append(req.Jobs, Job{
			ID: string(rune('A' + i)), Equipment: "E1", Duration: 37, Crew: 1, EarliestStart: 0,
		})
	}
	res := Solve(context.Background(), req)
	if res.Status != StatusTimeoutNoSolution && res.Status != StatusTimeoutFeasible {
		t.Fatalf("status=%s", res.Status)
	}
	if res.Status == StatusTimeoutNoSolution && res.Schedule != nil {
		t.Fatalf("no-solution timeout must not return a schedule")
	}
}

func TestSolveCancel(t *testing.T) {
	req := &Request{
		Horizon:   MaxHorizon,
		BudgetMs:  60000,
		CrewSize:  2,
		Equipment: []Equipment{{ID: "E1"}},
	}
	for i := 0; i < MaxJobs; i++ {
		req.Jobs = append(req.Jobs, Job{
			ID: string(rune('A' + i)), Equipment: "E1", Duration: 31, Crew: 1, EarliestStart: 0,
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res := Solve(ctx, req)
	if res.Status != StatusCanceled {
		t.Fatalf("status=%s", res.Status)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancellation took too long")
	}
}

func assertFeasible(t *testing.T, req *Request, res Result) {
	t.Helper()
	if len(res.Schedule) != len(req.Jobs) {
		t.Fatalf("schedule covers %d of %d jobs", len(res.Schedule), len(req.Jobs))
	}
	byID := map[string]Assignment{}
	for _, a := range res.Schedule {
		byID[a.ID] = a
	}
	crew := make([]int, req.Horizon)
	busy := map[string][]bool{}
	for _, eq := range req.Equipment {
		busy[eq.ID] = make([]bool, req.Horizon)
	}
	for _, j := range req.Jobs {
		a := byID[j.ID]
		if a.End-a.Start != j.Duration || a.Start < j.EarliestStart || a.End > req.Horizon {
			t.Fatalf("job %s bad placement %+v", j.ID, a)
		}
		for _, p := range j.Predecessors {
			if byID[p].End > a.Start {
				t.Fatalf("job %s starts before predecessor %s ends", j.ID, p)
			}
		}
		for m := a.Start; m < a.End; m++ {
			crew[m] += j.Crew
			if crew[m] > req.CrewSize {
				t.Fatalf("crew exceeded at minute %d", m)
			}
			if busy[j.Equipment][m] {
				t.Fatalf("equipment %s double-booked at minute %d", j.Equipment, m)
			}
			busy[j.Equipment][m] = true
		}
		for _, eq := range req.Equipment {
			if eq.ID != j.Equipment {
				continue
			}
			for _, iv := range eq.Unavailable {
				if a.Start < iv.End && iv.Start < a.End {
					t.Fatalf("job %s overlaps outage of %s", j.ID, eq.ID)
				}
			}
		}
	}
}
