// Package scheduler validates shutdown-maintenance requests and searches for
// an optimal schedule with an exact branch-and-bound over start times.
package scheduler

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Limits from the problem statement.
const (
	MaxJobs    = 12
	MaxHorizon = 480
)

// Interval is a half-open minute range [Start, End).
type Interval struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Equipment describes one machine and its unavailable windows.
type Equipment struct {
	ID          string     `json:"id"`
	Unavailable []Interval `json:"unavailable"`
}

// Job is one indivisible maintenance task.
type Job struct {
	ID            string   `json:"id"`
	Equipment     string   `json:"equipment"`
	Duration      int      `json:"duration_minutes"`
	Crew          int      `json:"crew"`
	EarliestStart int      `json:"earliest_start"`
	Predecessors  []string `json:"predecessors"`
}

// Request is the payload accepted by the schedule endpoint.
type Request struct {
	Horizon   int         `json:"horizon_minutes"`
	BudgetMs  int         `json:"budget_ms"`
	CrewSize  int         `json:"crew_size"`
	Equipment []Equipment `json:"equipment"`
	Jobs      []Job       `json:"jobs"`
}

// Assignment is the decided placement of one job.
type Assignment struct {
	ID        string `json:"id"`
	Equipment string `json:"equipment"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
}

// Solve status values.
const (
	StatusOptimal           = "optimal"             // search exhausted, proven best makespan
	StatusInfeasible        = "infeasible"          // search exhausted, no complete plan exists
	StatusTimeoutFeasible   = "timeout_feasible"    // budget spent, best plan so far returned
	StatusTimeoutNoSolution = "timeout_no_solution" // budget spent, nothing complete found yet
	StatusCanceled          = "canceled"            // caller canceled the request
)

// Result is the outcome of a solve call.
type Result struct {
	Status   string       `json:"status"`
	Makespan *int         `json:"makespan_minutes"`
	Schedule []Assignment `json:"schedule,omitempty"`
}

// FieldError reports a validation failure pinned to a JSON field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *FieldError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Message) }

// Validate checks the request and returns the first field error found.
func Validate(req *Request) error {
	if req.Horizon < 1 || req.Horizon > MaxHorizon {
		return &FieldError{Field: "horizon_minutes", Message: fmt.Sprintf("must be between 1 and %d", MaxHorizon)}
	}
	if req.BudgetMs < 1 {
		return &FieldError{Field: "budget_ms", Message: "must be a positive number of milliseconds"}
	}
	if req.CrewSize < 1 {
		return &FieldError{Field: "crew_size", Message: "must be at least 1"}
	}
	equipIDs := map[string]bool{}
	for i, eq := range req.Equipment {
		field := fmt.Sprintf("equipment[%d]", i)
		if eq.ID == "" {
			return &FieldError{Field: field + ".id", Message: "must not be empty"}
		}
		if equipIDs[eq.ID] {
			return &FieldError{Field: field + ".id", Message: "duplicate equipment id " + eq.ID}
		}
		equipIDs[eq.ID] = true
		for j, iv := range eq.Unavailable {
			if iv.Start < 0 || iv.End <= iv.Start || iv.End > req.Horizon {
				return &FieldError{Field: fmt.Sprintf("%s.unavailable[%d]", field, j),
					Message: "must satisfy 0 <= start < end <= horizon_minutes"}
			}
		}
	}
	if len(req.Jobs) == 0 {
		return &FieldError{Field: "jobs", Message: "at least one job is required"}
	}
	if len(req.Jobs) > MaxJobs {
		return &FieldError{Field: "jobs", Message: fmt.Sprintf("at most %d jobs are supported", MaxJobs)}
	}
	jobIDs := map[string]bool{}
	for i, j := range req.Jobs {
		field := fmt.Sprintf("jobs[%d]", i)
		if j.ID == "" {
			return &FieldError{Field: field + ".id", Message: "must not be empty"}
		}
		if jobIDs[j.ID] {
			return &FieldError{Field: field + ".id", Message: "duplicate job id " + j.ID}
		}
		jobIDs[j.ID] = true
		if !equipIDs[j.Equipment] {
			return &FieldError{Field: field + ".equipment", Message: "unknown equipment " + j.Equipment}
		}
		if j.Duration < 1 || j.Duration > req.Horizon {
			return &FieldError{Field: field + ".duration_minutes", Message: "must be between 1 and horizon_minutes"}
		}
		if j.Crew < 1 || j.Crew > req.CrewSize {
			return &FieldError{Field: field + ".crew", Message: "must be between 1 and crew_size"}
		}
		if j.EarliestStart < 0 || j.EarliestStart >= req.Horizon {
			return &FieldError{Field: field + ".earliest_start", Message: "must be within [0, horizon_minutes)"}
		}
		for k, p := range j.Predecessors {
			if p == j.ID {
				return &FieldError{Field: fmt.Sprintf("%s.predecessors[%d]", field, k), Message: "a job cannot depend on itself"}
			}
		}
	}
	for i, j := range req.Jobs {
		for k, p := range j.Predecessors {
			if !jobIDs[p] {
				return &FieldError{Field: fmt.Sprintf("jobs[%d].predecessors[%d]", i, k), Message: "unknown job id " + p}
			}
		}
	}
	if cyc := findCycle(req.Jobs); cyc != "" {
		return &FieldError{Field: "jobs", Message: "dependency cycle involving job " + cyc}
	}
	return nil
}

func findCycle(jobs []Job) string {
	const (
		white, gray, black = 0, 1, 2
	)
	color := map[string]int{}
	preds := map[string][]string{}
	for _, j := range jobs {
		preds[j.ID] = j.Predecessors
	}
	var visit func(id string) bool
	visit = func(id string) bool {
		color[id] = gray
		for _, p := range preds[id] {
			if color[p] == gray || (color[p] == white && visit(p)) {
				return true
			}
		}
		color[id] = black
		return false
	}
	for _, j := range jobs {
		if color[j.ID] == white && visit(j.ID) {
			return j.ID
		}
	}
	return ""
}

// Solve searches for a minimum-makespan complete schedule. It respects
// ctx cancellation and the request's millisecond budget.
func Solve(ctx context.Context, req *Request) Result {
	s := newSolver(req)
	s.run(ctx)
	return s.result()
}

type solver struct {
	req       *Request
	n         int
	idx       map[string]int
	predIdx   [][]int
	equipIdx  []int
	unavail   [][]bool // per equipment, per minute
	crewUsed  []int
	equipBusy [][]bool
	start     []int // -1 when unscheduled
	done      []bool
	scheduled int
	curEnd    int // max end among scheduled jobs

	deadline time.Time
	nodes    int
	canceled bool
	timedOut bool
	best     []int // best starts by job index, nil if none
	bestSpan int
	order    []int // job indices sorted by ID for lexicographic compare
}

func newSolver(req *Request) *solver {
	n := len(req.Jobs)
	s := &solver{
		req:      req,
		n:        n,
		idx:      map[string]int{},
		predIdx:  make([][]int, n),
		equipIdx: make([]int, n),
		unavail:  make([][]bool, len(req.Equipment)),
		crewUsed: make([]int, req.Horizon),
		start:    make([]int, n),
		done:     make([]bool, n),
		bestSpan: req.Horizon + 1,
	}
	for i := range s.start {
		s.start[i] = -1
	}
	for i, j := range req.Jobs {
		s.idx[j.ID] = i
	}
	for i, j := range req.Jobs {
		for _, p := range j.Predecessors {
			s.predIdx[i] = append(s.predIdx[i], s.idx[p])
		}
	}
	for e, eq := range req.Equipment {
		s.unavail[e] = make([]bool, req.Horizon)
		for _, iv := range eq.Unavailable {
			for m := iv.Start; m < iv.End; m++ {
				s.unavail[e][m] = true
			}
		}
	}
	equipOf := map[string]int{}
	for e, eq := range req.Equipment {
		equipOf[eq.ID] = e
	}
	for i, j := range req.Jobs {
		s.equipIdx[i] = equipOf[j.Equipment]
	}
	s.equipBusy = make([][]bool, len(req.Equipment))
	for e := range s.equipBusy {
		s.equipBusy[e] = make([]bool, req.Horizon)
	}
	s.order = make([]int, n)
	for i := range s.order {
		s.order[i] = i
	}
	sort.Slice(s.order, func(a, b int) bool { return req.Jobs[s.order[a]].ID < req.Jobs[s.order[b]].ID })
	return s
}

func (s *solver) run(ctx context.Context) {
	s.deadline = time.Now().Add(time.Duration(s.req.BudgetMs) * time.Millisecond)
	s.dfs(ctx)
}

func (s *solver) result() Result {
	res := Result{}
	if s.best != nil {
		mk := s.bestSpan
		res.Makespan = &mk
		res.Schedule = make([]Assignment, 0, s.n)
		for _, i := range s.order {
			j := s.req.Jobs[i]
			res.Schedule = append(res.Schedule, Assignment{
				ID: j.ID, Equipment: j.Equipment, Start: s.best[i], End: s.best[i] + j.Duration,
			})
		}
	}
	switch {
	case s.canceled:
		res.Status = StatusCanceled
		if s.best == nil {
			res.Schedule = nil
		}
	case s.timedOut:
		if s.best != nil {
			res.Status = StatusTimeoutFeasible
		} else {
			res.Status = StatusTimeoutNoSolution
		}
	case s.best != nil:
		res.Status = StatusOptimal
	default:
		res.Status = StatusInfeasible
	}
	return res
}

// lowerBound estimates the best achievable makespan from the current state.
func (s *solver) lowerBound() int {
	lb := s.curEnd
	for i := 0; i < s.n; i++ {
		if s.done[i] {
			continue
		}
		est := s.req.Jobs[i].EarliestStart
		for _, p := range s.predIdx[i] {
			if !s.done[p] {
				est = max(est, s.req.Jobs[p].EarliestStart+s.req.Jobs[p].Duration)
			} else if end := s.start[p] + s.req.Jobs[p].Duration; end > est {
				est = end
			}
		}
		lb = max(lb, est+s.req.Jobs[i].Duration)
	}
	return lb
}

func (s *solver) dfs(ctx context.Context) {
	s.nodes++
	if s.nodes%512 == 0 {
		if ctx.Err() != nil {
			s.canceled = true
			return
		}
		if time.Now().After(s.deadline) {
			s.timedOut = true
			return
		}
	}
	if s.canceled || s.timedOut {
		return
	}
	if s.scheduled == s.n {
		s.maybeUpdateBest()
		return
	}
	if s.lowerBound() > s.bestSpan {
		return
	}
	for i := 0; i < s.n; i++ {
		if s.done[i] || !s.predsDone(i) {
			continue
		}
		est := s.req.Jobs[i].EarliestStart
		for _, p := range s.predIdx[i] {
			est = max(est, s.start[p]+s.req.Jobs[p].Duration)
		}
		dur := s.req.Jobs[i].Duration
		for st := est; st+dur <= s.req.Horizon; st++ {
			if max(s.curEnd, st+dur) > s.bestSpan {
				break
			}
			if !s.fits(i, st) {
				continue
			}
			s.place(i, st)
			s.dfs(ctx)
			s.unplace(i, st)
			if s.canceled || s.timedOut {
				return
			}
		}
	}
}

func (s *solver) predsDone(i int) bool {
	for _, p := range s.predIdx[i] {
		if !s.done[p] {
			return false
		}
	}
	return true
}

func (s *solver) fits(i, st int) bool {
	j := s.req.Jobs[i]
	e := s.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		if s.unavail[e][m] || s.equipBusy[e][m] {
			return false
		}
		if s.crewUsed[m]+j.Crew > s.req.CrewSize {
			return false
		}
	}
	return true
}

func (s *solver) place(i, st int) {
	j := s.req.Jobs[i]
	e := s.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		s.equipBusy[e][m] = true
		s.crewUsed[m] += j.Crew
	}
	s.start[i] = st
	s.done[i] = true
	s.scheduled++
	s.curEnd = max(s.curEnd, st+j.Duration)
}

func (s *solver) unplace(i, st int) {
	j := s.req.Jobs[i]
	e := s.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		s.equipBusy[e][m] = false
		s.crewUsed[m] -= j.Crew
	}
	s.start[i] = -1
	s.done[i] = false
	s.scheduled--
	s.curEnd = 0
	for k := 0; k < s.n; k++ {
		if s.done[k] {
			s.curEnd = max(s.curEnd, s.start[k]+s.req.Jobs[k].Duration)
		}
	}
}

// maybeUpdateBest keeps the smallest makespan, breaking ties by the
// lexicographically smallest start sequence over ID-sorted jobs.
func (s *solver) maybeUpdateBest() {
	if s.curEnd > s.bestSpan {
		return
	}
	if s.curEnd == s.bestSpan && s.best != nil {
		for _, i := range s.order {
			if s.start[i] != s.best[i] {
				if s.start[i] > s.best[i] {
					return
				}
				break
			}
		}
	}
	s.bestSpan = s.curEnd
	s.best = make([]int, s.n)
	copy(s.best, s.start)
}
