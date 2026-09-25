package scheduler

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// NewUnavailable is an equipment blackout added at reschedule time.
type NewUnavailable struct {
	Equipment string "json:\"equipment\""
	Start     int    "json:\"start\""
	End       int    "json:\"end\""
}

// RescheduleRequest extends the original planning problem with the state of
// the world at minute NowMinute.
type RescheduleRequest struct {
	Request
	OriginalSchedule []Assignment     "json:\"original_schedule\""
	NowMinute        int              "json:\"now_minute\""
	NewUnavailable   []NewUnavailable "json:\"new_unavailable\""
	LockedJobIDs     []string         "json:\"locked_job_ids,omitempty\""
}

// JobChange compares the original placement with the new one.
type JobChange struct {
	ID            string "json:\"id\""
	Equipment     string "json:\"equipment\""
	OriginalStart int    "json:\"original_start\""
	OriginalEnd   int    "json:\"original_end\""
	NewStart      int    "json:\"new_start\""
	NewEnd        int    "json:\"new_end\""
	StartDelta    int    "json:\"start_delta\""
}

// RescheduleMetrics quantifies how far the new plan departs from the old one.
type RescheduleMetrics struct {
	ChangedJobs     int "json:\"changed_jobs\""
	TotalStartShift int "json:\"total_start_shift_minutes\""
	MakespanMinutes int "json:\"makespan_minutes\""
}

// RescheduleResult is the outcome of a Reschedule call.
type RescheduleResult struct {
	Status   string             "json:\"status\""
	Makespan *int               "json:\"makespan_minutes,omitempty\""
	Schedule []Assignment       "json:\"schedule,omitempty\""
	Changes  []JobChange        "json:\"changes,omitempty\""
	Metrics  *RescheduleMetrics "json:\"metrics,omitempty\""
	Reason   string             "json:\"reason,omitempty\""
}

// ValidateReschedule checks the request, including that original_schedule is
// a complete feasible plan for the embedded problem.
func ValidateReschedule(req *RescheduleRequest) error {
	if err := Validate(&req.Request); err != nil {
		return err
	}
	if req.NowMinute < 0 || req.NowMinute > req.Horizon {
		return &FieldError{Field: "now_minute",
			Message: fmt.Sprintf("must be within [0, %d]", req.Horizon)}
	}
	jobIndex := map[string]int{}
	for i, j := range req.Jobs {
		jobIndex[j.ID] = i
	}
	equipIndex := map[string]int{}
	for e, eq := range req.Equipment {
		equipIndex[eq.ID] = e
	}
	if len(req.OriginalSchedule) != len(req.Jobs) {
		return &FieldError{Field: "original_schedule",
			Message: fmt.Sprintf("must contain exactly one assignment per job, got %d", len(req.OriginalSchedule))}
	}
	seen := map[string]bool{}
	starts := make([]int, len(req.Jobs))
	for i := range starts {
		starts[i] = -1
	}
	for i, a := range req.OriginalSchedule {
		field := fmt.Sprintf("original_schedule[%d]", i)
		ji, ok := jobIndex[a.ID]
		if !ok {
			return &FieldError{Field: field + ".id", Message: "unknown job id " + a.ID}
		}
		if seen[a.ID] {
			return &FieldError{Field: field + ".id", Message: "duplicate assignment for job " + a.ID}
		}
		seen[a.ID] = true
		j := req.Jobs[ji]
		if a.Equipment != "" && a.Equipment != j.Equipment {
			return &FieldError{Field: field + ".equipment",
				Message: "must match the job's equipment " + j.Equipment}
		}
		if a.Start < 0 || a.End != a.Start+j.Duration {
			return &FieldError{Field: field + ".start",
				Message: fmt.Sprintf("must satisfy 0 <= start and end == start + %d", j.Duration)}
		}
		if a.Start < j.EarliestStart {
			return &FieldError{Field: field + ".start", Message: "before the job's earliest_start"}
		}
		if a.End > req.Horizon {
			return &FieldError{Field: field + ".end", Message: "after horizon_minutes"}
		}
		starts[ji] = a.Start
	}
	for i, a := range req.OriginalSchedule {
		ji := jobIndex[a.ID]
		for _, p := range req.Jobs[ji].Predecessors {
			pi := jobIndex[p]
			if starts[pi]+req.Jobs[pi].Duration > a.Start {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d].start", i),
					Message: fmt.Sprintf("job %s starts before predecessor %s finishes", a.ID, p)}
			}
		}
	}
	// Minute-level checks: original blackouts, equipment exclusion, crew.
	equipBusy := make([][]bool, len(req.Equipment))
	for e := range equipBusy {
		equipBusy[e] = make([]bool, req.Horizon)
	}
	crewUsed := make([]int, req.Horizon)
	unavail := make([][]bool, len(req.Equipment))
	for e, eq := range req.Equipment {
		unavail[e] = make([]bool, req.Horizon)
		for _, iv := range eq.Unavailable {
			for m := iv.Start; m < iv.End; m++ {
				unavail[e][m] = true
			}
		}
	}
	order := make([]int, len(starts))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		if starts[order[a]] != starts[order[b]] {
			return starts[order[a]] < starts[order[b]]
		}
		return order[a] < order[b]
	})
	for _, i := range order {
		a := req.OriginalSchedule[i]
		j := req.Jobs[i]
		e := equipIndex[j.Equipment]
		for m := a.Start; m < a.End; m++ {
			if unavail[e][m] {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d]", i),
					Message: "runs during an equipment-unavailable interval"}
			}
			if equipBusy[e][m] {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d]", i),
					Message: "overlaps another job on equipment " + j.Equipment}
			}
			if crewUsed[m]+j.Crew > req.CrewSize {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d]", i),
					Message: "exceeds crew_size"}
			}
		}
		for m := a.Start; m < a.End; m++ {
			equipBusy[e][m] = true
			crewUsed[m] += j.Crew
		}
	}
	for i, nb := range req.NewUnavailable {
		field := fmt.Sprintf("new_unavailable[%d]", i)
		if _, ok := equipIndex[nb.Equipment]; !ok {
			return &FieldError{Field: field + ".equipment", Message: "unknown equipment " + nb.Equipment}
		}
		if nb.Start < req.NowMinute || nb.End <= nb.Start || nb.End > req.Horizon {
			return &FieldError{Field: field,
				Message: "must satisfy now_minute <= start < end <= horizon_minutes"}
		}
	}
	locked := map[string]bool{}
	for i, id := range req.LockedJobIDs {
		field := fmt.Sprintf("locked_job_ids[%d]", i)
		ji, ok := jobIndex[id]
		if !ok {
			return &FieldError{Field: field, Message: "unknown job id " + id}
		}
		if locked[id] {
			return &FieldError{Field: field, Message: "duplicate locked job id " + id}
		}
		locked[id] = true
		if starts[ji] < req.NowMinute {
			return &FieldError{Field: field,
				Message: "job " + id + " has already started or finished; only not-started jobs can be locked"}
		}
	}
	return nil
}

// Reschedule searches for the minimally perturbed feasible plan. The objective
// is lexicographic: number of moved not-started jobs, total absolute start
// shift, makespan, then the ID-sorted start sequence.
func Reschedule(ctx context.Context, req *RescheduleRequest) RescheduleResult {
	r := newRescheduler(req)
	r.run(ctx)
	return r.result()
}

type rescheduler struct {
	req       *RescheduleRequest
	n         int
	predIdx   [][]int
	equipIdx  []int
	unavail   [][]bool
	equipBusy [][]bool
	crewUsed  []int
	start     []int
	orig      []int
	fixed     []bool
	done      []bool
	placed    int
	curEnd    int
	curMoved  int
	curShift  int

	deadline time.Time
	nodes    int
	canceled bool
	timedOut bool
	conflict string

	best      []int
	bestMoved int
	bestShift int
	bestSpan  int
	order     []int
}

func newRescheduler(req *RescheduleRequest) *rescheduler {
	n := len(req.Jobs)
	r := &rescheduler{
		req:       req,
		n:         n,
		predIdx:   make([][]int, n),
		equipIdx:  make([]int, n),
		unavail:   make([][]bool, len(req.Equipment)),
		equipBusy: make([][]bool, len(req.Equipment)),
		crewUsed:  make([]int, req.Horizon),
		start:     make([]int, n),
		orig:      make([]int, n),
		fixed:     make([]bool, n),
		done:      make([]bool, n),
		bestMoved: n + 1,
		bestShift: 1 << 30,
		bestSpan:  req.Horizon + 1,
	}
	for i := range r.start {
		r.start[i] = -1
	}
	equipOf := map[string]int{}
	for e, eq := range req.Equipment {
		equipOf[eq.ID] = e
		r.unavail[e] = make([]bool, req.Horizon)
		r.equipBusy[e] = make([]bool, req.Horizon)
		for _, iv := range eq.Unavailable {
			for m := iv.Start; m < iv.End; m++ {
				r.unavail[e][m] = true
			}
		}
	}
	for _, nb := range req.NewUnavailable {
		e := equipOf[nb.Equipment]
		for m := nb.Start; m < nb.End; m++ {
			r.unavail[e][m] = true
		}
	}
	idIndex := map[string]int{}
	for i, j := range req.Jobs {
		idIndex[j.ID] = i
		r.equipIdx[i] = equipOf[j.Equipment]
		r.orig[i] = req.OriginalSchedule[i].Start
	}
	for i, j := range req.Jobs {
		for _, p := range j.Predecessors {
			r.predIdx[i] = append(r.predIdx[i], idIndex[p])
		}
	}
	lockedSet := map[string]bool{}
	for _, id := range req.LockedJobIDs {
		lockedSet[id] = true
	}
	for i, j := range req.Jobs {
		if r.orig[i] < req.NowMinute || lockedSet[j.ID] {
			r.fixed[i] = true
		}
	}
	r.order = make([]int, n)
	for i := range r.order {
		r.order[i] = i
	}
	sort.Slice(r.order, func(a, b int) bool {
		return req.Jobs[r.order[a]].ID < req.Jobs[r.order[b]].ID
	})
	return r
}

func (r *rescheduler) run(ctx context.Context) {
	r.deadline = time.Now().Add(time.Duration(r.req.BudgetMs) * time.Millisecond)
	for i := 0; i < r.n; i++ {
		if !r.fixed[i] {
			continue
		}
		st := r.orig[i]
		if !r.fits(i, st) {
			r.conflict = r.req.Jobs[i].ID
			return
		}
		r.place(i, st)
	}
	r.dfs(ctx)
}

func (r *rescheduler) predsDone(i int) bool {
	for _, p := range r.predIdx[i] {
		if !r.done[p] {
			return false
		}
	}
	return true
}

func (r *rescheduler) fits(i, st int) bool {
	j := r.req.Jobs[i]
	e := r.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		if r.unavail[e][m] || r.equipBusy[e][m] {
			return false
		}
		if r.crewUsed[m]+j.Crew > r.req.CrewSize {
			return false
		}
	}
	return true
}

func (r *rescheduler) place(i, st int) {
	j := r.req.Jobs[i]
	e := r.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		r.equipBusy[e][m] = true
		r.crewUsed[m] += j.Crew
	}
	r.start[i] = st
	r.done[i] = true
	r.placed++
	if st+j.Duration > r.curEnd {
		r.curEnd = st + j.Duration
	}
	if !r.fixed[i] {
		if st != r.orig[i] {
			r.curMoved++
		}
		r.curShift += abs(st - r.orig[i])
	}
}

func (r *rescheduler) unplace(i, st int) {
	j := r.req.Jobs[i]
	e := r.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		r.equipBusy[e][m] = false
		r.crewUsed[m] -= j.Crew
	}
	r.start[i] = -1
	r.done[i] = false
	r.placed--
	r.curEnd = 0
	for k := 0; k < r.n; k++ {
		if r.done[k] && r.start[k]+r.req.Jobs[k].Duration > r.curEnd {
			r.curEnd = r.start[k] + r.req.Jobs[k].Duration
		}
	}
	if !r.fixed[i] {
		if st != r.orig[i] {
			r.curMoved--
		}
		r.curShift -= abs(st - r.orig[i])
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// lowerBound estimates the smallest makespan reachable from the current state.
func (r *rescheduler) lowerBound() int {
	lb := r.curEnd
	for i := 0; i < r.n; i++ {
		if r.done[i] {
			continue
		}
		est := r.req.Jobs[i].EarliestStart
		if r.req.NowMinute > est {
			est = r.req.NowMinute
		}
		for _, p := range r.predIdx[i] {
			var pend int
			if r.done[p] {
				pend = r.start[p] + r.req.Jobs[p].Duration
			} else {
				pe := r.req.Jobs[p].EarliestStart
				if r.req.NowMinute > pe {
					pe = r.req.NowMinute
				}
				pend = pe + r.req.Jobs[p].Duration
			}
			if pend > est {
				est = pend
			}
		}
		if est+r.req.Jobs[i].Duration > lb {
			lb = est + r.req.Jobs[i].Duration
		}
	}
	return lb
}

// better reports whether the current complete plan beats the incumbent.
func (r *rescheduler) better() bool {
	if r.best == nil {
		return true
	}
	if r.curMoved != r.bestMoved {
		return r.curMoved < r.bestMoved
	}
	if r.curShift != r.bestShift {
		return r.curShift < r.bestShift
	}
	if r.curEnd != r.bestSpan {
		return r.curEnd < r.bestSpan
	}
	for _, i := range r.order {
		if r.start[i] != r.best[i] {
			return r.start[i] < r.best[i]
		}
	}
	return false
}

func (r *rescheduler) dfs(ctx context.Context) {
	r.nodes++
	if r.nodes%512 == 0 {
		if ctx.Err() != nil {
			r.canceled = true
			return
		}
		if time.Now().After(r.deadline) {
			r.timedOut = true
			return
		}
	}
	if r.canceled || r.timedOut {
		return
	}
	if r.placed == r.n {
		if r.better() {
			r.bestMoved = r.curMoved
			r.bestShift = r.curShift
			r.bestSpan = r.curEnd
			r.best = make([]int, r.n)
			copy(r.best, r.start)
		}
		return
	}
	// Prune: partial moved/shift already worse than the incumbent.
	if r.curMoved > r.bestMoved {
		return
	}
	if r.curMoved == r.bestMoved && r.curShift > r.bestShift {
		return
	}
	if r.curMoved == r.bestMoved && r.curShift == r.bestShift && r.lowerBound() > r.bestSpan {
		return
	}
	for i := 0; i < r.n; i++ {
		if r.done[i] || !r.predsDone(i) {
			continue
		}
		j := r.req.Jobs[i]
		est := j.EarliestStart
		if r.req.NowMinute > est {
			est = r.req.NowMinute
		}
		for _, p := range r.predIdx[i] {
			if end := r.start[p] + r.req.Jobs[p].Duration; end > est {
				est = end
			}
		}
		for st := est; st+j.Duration <= r.req.Horizon; st++ {
			if !r.fits(i, st) {
				continue
			}
			r.place(i, st)
			r.dfs(ctx)
			r.unplace(i, st)
			if r.canceled || r.timedOut {
				return
			}
		}
	}
}

func (r *rescheduler) result() RescheduleResult {
	res := RescheduleResult{}
	if r.conflict != "" {
		res.Status = StatusInfeasible
		res.Reason = "frozen or locked job " + r.conflict + " conflicts with the new unavailable intervals"
		return res
	}
	if r.best != nil {
		mk := r.bestSpan
		res.Makespan = &mk
		res.Metrics = &RescheduleMetrics{
			ChangedJobs:     r.bestMoved,
			TotalStartShift: r.bestShift,
			MakespanMinutes: r.bestSpan,
		}
		res.Schedule = make([]Assignment, 0, r.n)
		res.Changes = make([]JobChange, 0, r.n)
		for _, i := range r.order {
			j := r.req.Jobs[i]
			res.Schedule = append(res.Schedule, Assignment{
				ID: j.ID, Equipment: j.Equipment,
				Start: r.best[i], End: r.best[i] + j.Duration,
			})
			res.Changes = append(res.Changes, JobChange{
				ID:            j.ID,
				Equipment:     j.Equipment,
				OriginalStart: r.orig[i],
				OriginalEnd:   r.orig[i] + j.Duration,
				NewStart:      r.best[i],
				NewEnd:        r.best[i] + j.Duration,
				StartDelta:    r.best[i] - r.orig[i],
			})
		}
	}
	switch {
	case r.canceled:
		res.Status = StatusCanceled
		if r.best == nil {
			res.Schedule = nil
			res.Changes = nil
		}
	case r.timedOut:
		if r.best != nil {
			res.Status = StatusTimeoutFeasible
		} else {
			res.Status = StatusTimeoutNoSolution
		}
	case r.best != nil:
		res.Status = StatusOptimal
	default:
		res.Status = StatusInfeasible
	}
	return res
}
