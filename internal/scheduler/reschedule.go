package scheduler

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Problem is the original scheduling instance carried by a reschedule call.
type Problem struct {
	Horizon   int         `json:"horizon_minutes"`
	CrewSize  int         `json:"crew_size"`
	Equipment []Equipment `json:"equipment"`
	Jobs      []Job       `json:"jobs"`
}

// OutageAddition is a newly imposed equipment outage.
type OutageAddition struct {
	EquipmentID string     `json:"equipment"`
	Intervals   []Interval `json:"intervals"`
}

// RescheduleRequest is the payload accepted by the reschedule endpoint.
type RescheduleRequest struct {
	BudgetMs         int              `json:"budget_ms"`
	T                int              `json:"t_minutes"`
	Problem          Problem          `json:"problem"`
	OriginalSchedule []Assignment     `json:"original_schedule"`
	NewUnavailable   []OutageAddition `json:"new_unavailable"`
	LockedJobIDs     []string         `json:"locked_job_ids,omitempty"`
}

// Job state labels at time T.
const (
	StateCompleted  = "completed"
	StateInProgress = "in_progress"
	StateUnstarted  = "unstarted"
)

// JobChange compares the original and new placement of one job.
type JobChange struct {
	ID            string `json:"id"`
	Equipment     string `json:"equipment"`
	State         string `json:"state"`
	OriginalStart int    `json:"original_start"`
	OriginalEnd   int    `json:"original_end"`
	Start         int    `json:"start"`
	End           int    `json:"end"`
	StartDelta    int    `json:"start_delta_minutes"`
	Changed       bool   `json:"changed"`
}

// RescheduleResult is the outcome of a reschedule call.
type RescheduleResult struct {
	Status          string       `json:"status"`
	T               int          `json:"t_minutes"`
	Makespan        *int         `json:"makespan_minutes,omitempty"`
	ChangedJobs     int          `json:"changed_jobs"`
	TotalStartShift int          `json:"total_start_shift_minutes"`
	Schedule        []Assignment `json:"schedule,omitempty"`
	Changes         []JobChange  `json:"changes,omitempty"`
}

// ValidateReschedule validates the request and verifies that the supplied
// original schedule is a complete, consistent solution of the original problem.
func ValidateReschedule(req *RescheduleRequest) error {
	if req.BudgetMs < 1 {
		return &FieldError{Field: "budget_ms", Message: "must be a positive number of milliseconds"}
	}
	p := &Request{
		Horizon:   req.Problem.Horizon,
		BudgetMs:  1,
		CrewSize:  req.Problem.CrewSize,
		Equipment: req.Problem.Equipment,
		Jobs:      req.Problem.Jobs,
	}
	if err := Validate(p); err != nil {
		if fe, ok := err.(*FieldError); ok {
			return &FieldError{Field: "problem." + fe.Field, Message: fe.Message}
		}
		return err
	}
	horizon := req.Problem.Horizon
	if req.T < 0 || req.T > horizon {
		return &FieldError{Field: "t_minutes", Message: "must be within [0, horizon_minutes]"}
	}

	jobByID := map[string]int{}
	for i, j := range req.Problem.Jobs {
		jobByID[j.ID] = i
	}
	if len(req.OriginalSchedule) != len(req.Problem.Jobs) {
		return &FieldError{Field: "original_schedule",
			Message: fmt.Sprintf("must contain exactly one entry per job, got %d want %d",
				len(req.OriginalSchedule), len(req.Problem.Jobs))}
	}
	origStart := make([]int, len(req.Problem.Jobs))
	origEntry := make([]int, len(req.Problem.Jobs))
	seen := make([]bool, len(req.Problem.Jobs))
	for i, a := range req.OriginalSchedule {
		field := fmt.Sprintf("original_schedule[%d]", i)
		if a.ID == "" {
			return &FieldError{Field: field + ".id", Message: "must not be empty"}
		}
		ji, ok := jobByID[a.ID]
		if !ok {
			return &FieldError{Field: field + ".id", Message: "unknown job id " + a.ID}
		}
		if seen[ji] {
			return &FieldError{Field: field + ".id", Message: "duplicate schedule entry for job " + a.ID}
		}
		seen[ji] = true
		j := req.Problem.Jobs[ji]
		if a.Equipment != j.Equipment {
			return &FieldError{Field: field + ".equipment",
				Message: "does not match job equipment " + j.Equipment}
		}
		if a.Start < 0 {
			return &FieldError{Field: field + ".start", Message: "must not be negative"}
		}
		if a.End != a.Start+j.Duration {
			return &FieldError{Field: field + ".end",
				Message: fmt.Sprintf("must equal start + duration_minutes (%d)", j.Duration)}
		}
		if a.Start < j.EarliestStart {
			return &FieldError{Field: field + ".start", Message: "before job earliest_start"}
		}
		if a.End > horizon {
			return &FieldError{Field: field + ".end", Message: "past horizon_minutes"}
		}
		origStart[ji] = a.Start
		origEntry[ji] = i
	}
	for i, j := range req.Problem.Jobs {
		for _, pid := range j.Predecessors {
			pj := jobByID[pid]
			if origStart[pj]+req.Problem.Jobs[pj].Duration > origStart[i] {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d].start", origEntry[i]),
					Message: fmt.Sprintf("job %s starts before predecessor %s finishes", j.ID, pid)}
			}
		}
	}
	eqOf := map[string]int{}
	for e, eq := range req.Problem.Equipment {
		eqOf[eq.ID] = e
	}
	eqBusy := make([][]bool, len(req.Problem.Equipment))
	for e := range eqBusy {
		eqBusy[e] = make([]bool, horizon)
	}
	crewUsed := make([]int, horizon)
	for ji, j := range req.Problem.Jobs {
		st := origStart[ji]
		en := st + j.Duration
		e := eqOf[j.Equipment]
		for _, iv := range req.Problem.Equipment[e].Unavailable {
			if st < iv.End && iv.Start < en {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d].start", origEntry[ji]),
					Message: fmt.Sprintf("job %s overlaps an original outage of %s", j.ID, j.Equipment)}
			}
		}
		for m := st; m < en; m++ {
			if eqBusy[e][m] {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d].start", origEntry[ji]),
					Message: fmt.Sprintf("equipment %s double-booked at minute %d", j.Equipment, m)}
			}
			eqBusy[e][m] = true
			crewUsed[m] += j.Crew
			if crewUsed[m] > req.Problem.CrewSize {
				return &FieldError{Field: fmt.Sprintf("original_schedule[%d].start", origEntry[ji]),
					Message: fmt.Sprintf("crew_size exceeded at minute %d", m)}
			}
		}
	}

	for i, ad := range req.NewUnavailable {
		field := fmt.Sprintf("new_unavailable[%d]", i)
		if _, ok := eqOf[ad.EquipmentID]; !ok {
			return &FieldError{Field: field + ".equipment", Message: "unknown equipment " + ad.EquipmentID}
		}
		for k, iv := range ad.Intervals {
			if iv.Start < req.T {
				return &FieldError{Field: fmt.Sprintf("%s.intervals[%d].start", field, k),
					Message: "new outage must not start before t_minutes"}
			}
			if iv.End <= iv.Start || iv.End > horizon {
				return &FieldError{Field: fmt.Sprintf("%s.intervals[%d]", field, k),
					Message: "must satisfy t_minutes <= start < end <= horizon_minutes"}
			}
		}
	}

	lockedSeen := map[string]bool{}
	for i, id := range req.LockedJobIDs {
		field := fmt.Sprintf("locked_job_ids[%d]", i)
		ji, ok := jobByID[id]
		if !ok {
			return &FieldError{Field: field, Message: "unknown job id " + id}
		}
		if lockedSeen[id] {
			return &FieldError{Field: field, Message: "duplicate locked job id " + id}
		}
		lockedSeen[id] = true
		if origStart[ji] < req.T {
			return &FieldError{Field: field,
				Message: "job has already started or finished by t_minutes; locks apply only to unstarted jobs"}
		}
	}
	return nil
}

// Resolve reschedules the unstarted jobs with minimum perturbation.
func Resolve(ctx context.Context, req *RescheduleRequest) RescheduleResult {
	rs := newRescheduler(req)
	rs.run(ctx)
	return rs.result()
}

type rescheduler struct {
	n        int
	jobs     []Job
	horizon  int
	crewSize int
	t        int

	idx      map[string]int
	predIdx  [][]int
	equipIdx []int
	orig     []int
	state    []string
	locked   []bool
	fixed    []bool

	unavail   [][]bool
	equipBusy [][]bool
	crewUsed  []int
	start     []int
	placed    []bool
	nPlaced   int
	curEnd    int
	changed   int
	shift     int

	budgetMs int

	infeasible bool
	deadline   time.Time
	nodes      int
	canceled   bool
	timedOut   bool

	bestStarts  []int
	bestChanged int
	bestShift   int
	bestSpan    int
	order       []int
}

func newRescheduler(req *RescheduleRequest) *rescheduler {
	n := len(req.Problem.Jobs)
	rs := &rescheduler{
		budgetMs: req.BudgetMs,
		n:        n,
		jobs:     req.Problem.Jobs,
		horizon:  req.Problem.Horizon,
		crewSize: req.Problem.CrewSize,
		t:        req.T,
		idx:      map[string]int{},
		predIdx:  make([][]int, n),
		equipIdx: make([]int, n),
		orig:     make([]int, n),
		state:    make([]string, n),
		locked:   make([]bool, n),
		fixed:    make([]bool, n),
		crewUsed: make([]int, req.Problem.Horizon),
		start:    make([]int, n),
		placed:   make([]bool, n),
		bestSpan: req.Problem.Horizon + 1,
	}
	for i := range rs.start {
		rs.start[i] = -1
	}
	eqOf := map[string]int{}
	for e, eq := range req.Problem.Equipment {
		eqOf[eq.ID] = e
	}
	origOf := map[string]int{}
	for _, a := range req.OriginalSchedule {
		origOf[a.ID] = a.Start
	}
	for i, j := range req.Problem.Jobs {
		rs.idx[j.ID] = i
		rs.equipIdx[i] = eqOf[j.Equipment]
		rs.orig[i] = origOf[j.ID]
		for _, p := range j.Predecessors {
			rs.predIdx[i] = append(rs.predIdx[i], rs.idx[p])
		}
		switch {
		case rs.orig[i]+j.Duration <= req.T:
			rs.state[i] = StateCompleted
			rs.fixed[i] = true
		case rs.orig[i] < req.T:
			rs.state[i] = StateInProgress
			rs.fixed[i] = true
		default:
			rs.state[i] = StateUnstarted
		}
	}
	for _, id := range req.LockedJobIDs {
		rs.locked[rs.idx[id]] = true
	}
	rs.unavail = make([][]bool, len(req.Problem.Equipment))
	for e, eq := range req.Problem.Equipment {
		rs.unavail[e] = make([]bool, rs.horizon)
		for _, iv := range eq.Unavailable {
			for m := iv.Start; m < iv.End; m++ {
				rs.unavail[e][m] = true
			}
		}
	}
	for _, ad := range req.NewUnavailable {
		e := eqOf[ad.EquipmentID]
		for _, iv := range ad.Intervals {
			for m := iv.Start; m < iv.End; m++ {
				rs.unavail[e][m] = true
			}
		}
	}
	rs.equipBusy = make([][]bool, len(req.Problem.Equipment))
	for e := range rs.equipBusy {
		rs.equipBusy[e] = make([]bool, rs.horizon)
	}
	rs.order = make([]int, n)
	for i := range rs.order {
		rs.order[i] = i
	}
	sort.Slice(rs.order, func(a, b int) bool { return rs.jobs[rs.order[a]].ID < rs.jobs[rs.order[b]].ID })
	return rs
}

func (rs *rescheduler) run(ctx context.Context) {
	rs.deadline = time.Now().Add(time.Duration(rs.budgetMs) * time.Millisecond)
	// Frozen jobs keep their full intervals; in-progress jobs keep occupying
	// equipment and crew over their remaining span.
	for i := 0; i < rs.n; i++ {
		if !rs.fixed[i] {
			continue
		}
		if !rs.canOccupy(i, rs.orig[i]) {
			rs.infeasible = true
			return
		}
		rs.occupyFixed(i, rs.orig[i])
	}
	// Locked, unstarted jobs keep their original windows.
	for i := 0; i < rs.n; i++ {
		if !rs.locked[i] {
			continue
		}
		if !rs.canOccupy(i, rs.orig[i]) {
			rs.infeasible = true
			return
		}
		rs.occupyFree(i, rs.orig[i])
	}
	rs.dfs(ctx)
}

func (rs *rescheduler) canOccupy(i, st int) bool {
	j := rs.jobs[i]
	e := rs.equipIdx[i]
	if st < rs.t && !rs.fixed[i] {
		return false
	}
	for m := st; m < st+j.Duration; m++ {
		if rs.unavail[e][m] || rs.equipBusy[e][m] {
			return false
		}
		if rs.crewUsed[m]+j.Crew > rs.crewSize {
			return false
		}
	}
	return true
}

func (rs *rescheduler) mark(i, st int) {
	j := rs.jobs[i]
	e := rs.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		rs.equipBusy[e][m] = true
		rs.crewUsed[m] += j.Crew
	}
	rs.start[i] = st
	rs.placed[i] = true
	rs.nPlaced++
	rs.curEnd = max(rs.curEnd, st+j.Duration)
}

func (rs *rescheduler) unmark(i, st int) {
	j := rs.jobs[i]
	e := rs.equipIdx[i]
	for m := st; m < st+j.Duration; m++ {
		rs.equipBusy[e][m] = false
		rs.crewUsed[m] -= j.Crew
	}
	rs.start[i] = -1
	rs.placed[i] = false
	rs.nPlaced--
	rs.curEnd = 0
	for k := 0; k < rs.n; k++ {
		if rs.placed[k] {
			rs.curEnd = max(rs.curEnd, rs.start[k]+rs.jobs[k].Duration)
		}
	}
}

func (rs *rescheduler) occupyFixed(i, st int) {
	rs.mark(i, st)
}

func (rs *rescheduler) occupyFree(i, st int) {
	rs.mark(i, st)
	if st != rs.orig[i] {
		rs.changed++
		rs.shift += abs(st - rs.orig[i])
	}
}

func (rs *rescheduler) releaseFree(i, st int) {
	rs.unmark(i, st)
	if st != rs.orig[i] {
		rs.changed--
		rs.shift -= abs(st - rs.orig[i])
	}
}

func (rs *rescheduler) predsPlaced(i int) bool {
	for _, p := range rs.predIdx[i] {
		if !rs.placed[p] {
			return false
		}
	}
	return true
}

// earliestStart returns the earliest legal start for an unplaced free job.
func (rs *rescheduler) earliestStart(i int) int {
	est := max(rs.t, rs.jobs[i].EarliestStart)
	for _, p := range rs.predIdx[i] {
		est = max(est, rs.start[p]+rs.jobs[p].Duration)
	}
	return est
}

func (rs *rescheduler) dfs(ctx context.Context) {
	rs.nodes++
	if rs.nodes%256 == 0 {
		if ctx.Err() != nil {
			rs.canceled = true
			return
		}
		if time.Now().After(rs.deadline) {
			rs.timedOut = true
			return
		}
	}
	if rs.canceled || rs.timedOut {
		return
	}
	if rs.nPlaced == rs.n {
		rs.maybeUpdateBest()
		return
	}
	if !rs.boundOK() {
		return
	}
	i := rs.pickJob()
	if i < 0 {
		return
	}
	dur := rs.jobs[i].Duration
	for st := rs.earliestStart(i); st+dur <= rs.horizon; st++ {
		if !rs.canOccupy(i, st) {
			continue
		}
		rs.occupyFree(i, st)
		rs.dfs(ctx)
		rs.releaseFree(i, st)
		if rs.canceled || rs.timedOut {
			return
		}
	}
}

func (rs *rescheduler) pickJob() int {
	best := -1
	bestChoices := 1 << 30
	for i := 0; i < rs.n; i++ {
		if rs.placed[i] || rs.fixed[i] || rs.locked[i] || !rs.predsPlaced(i) {
			continue
		}
		choices := 0
		dur := rs.jobs[i].Duration
		for st := rs.earliestStart(i); st+dur <= rs.horizon; st++ {
			if rs.canOccupy(i, st) {
				choices++
			}
		}
		if choices == 0 {
			return i
		}
		if choices < bestChoices {
			bestChoices = choices
			best = i
		}
	}
	return best
}

// boundOK prunes nodes that cannot beat the incumbent under the strict
// objective order: changed count, total shift, makespan, start sequence.
func (rs *rescheduler) boundOK() bool {
	if rs.bestStarts == nil {
		return true
	}
	if rs.changed > rs.bestChanged {
		return false
	}
	if rs.changed < rs.bestChanged {
		return true
	}
	shiftLB := rs.shift
	for i := 0; i < rs.n; i++ {
		if rs.placed[i] || rs.fixed[i] || rs.locked[i] {
			continue
		}
		est := rs.earliestStart(i)
		lo := max(rs.t, est)
		hi := rs.horizon - rs.jobs[i].Duration
		var d int
		switch {
		case rs.orig[i] < lo:
			d = lo - rs.orig[i]
		case rs.orig[i] > hi:
			d = rs.orig[i] - hi
		default:
			d = 0
		}
		shiftLB += d
	}
	if shiftLB > rs.bestShift {
		return false
	}
	if shiftLB < rs.bestShift {
		return true
	}
	return rs.makespanLB() <= rs.bestSpan
}

// makespanLB is the longest remaining predecessor chain from decided
// placements and earliest starts, together with the current max end.
func (rs *rescheduler) makespanLB() int {
	lb := rs.curEnd
	memo := make([]int, rs.n)
	var chain func(i int) int
	chain = func(i int) int {
		if memo[i] != 0 {
			return memo[i]
		}
		var st int
		if rs.placed[i] {
			st = rs.start[i]
		} else {
			st = max(rs.t, rs.jobs[i].EarliestStart)
			for _, p := range rs.predIdx[i] {
				st = max(st, chain(p))
			}
		}
		end := st + rs.jobs[i].Duration
		memo[i] = end
		return end
	}
	for i := 0; i < rs.n; i++ {
		if !rs.placed[i] && !rs.fixed[i] && !rs.locked[i] {
			lb = max(lb, chain(i))
		}
	}
	return lb
}

func (rs *rescheduler) maybeUpdateBest() {
	if rs.bestStarts != nil {
		if rs.changed > rs.bestChanged {
			return
		}
		if rs.changed == rs.bestChanged {
			if rs.shift > rs.bestShift {
				return
			}
			if rs.shift == rs.bestShift {
				if rs.curEnd > rs.bestSpan {
					return
				}
				if rs.curEnd == rs.bestSpan {
					for _, i := range rs.order {
						if rs.start[i] != rs.bestStarts[i] {
							if rs.start[i] > rs.bestStarts[i] {
								return
							}
							break
						}
					}
				}
			}
		}
	}
	rs.bestChanged = rs.changed
	rs.bestShift = rs.shift
	rs.bestSpan = rs.curEnd
	rs.bestStarts = make([]int, rs.n)
	copy(rs.bestStarts, rs.start)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (rs *rescheduler) result() RescheduleResult {
	res := RescheduleResult{T: rs.t}
	switch {
	case rs.canceled:
		res.Status = StatusCanceled
	case rs.infeasible:
		res.Status = StatusInfeasible
	case rs.timedOut:
		if rs.bestStarts != nil {
			res.Status = StatusTimeoutFeasible
		} else {
			res.Status = StatusTimeoutNoSolution
		}
	case rs.bestStarts != nil:
		res.Status = StatusOptimal
	default:
		res.Status = StatusInfeasible
	}
	if rs.bestStarts != nil {
		mk := rs.bestSpan
		res.Makespan = &mk
		res.ChangedJobs = rs.bestChanged
		res.TotalStartShift = rs.bestShift
		res.Schedule = make([]Assignment, 0, rs.n)
		res.Changes = make([]JobChange, 0, rs.n)
		for _, i := range rs.order {
			j := rs.jobs[i]
			st := rs.bestStarts[i]
			res.Schedule = append(res.Schedule, Assignment{
				ID: j.ID, Equipment: j.Equipment, Start: st, End: st + j.Duration,
			})
			res.Changes = append(res.Changes, JobChange{
				ID: j.ID, Equipment: j.Equipment, State: rs.state[i],
				OriginalStart: rs.orig[i], OriginalEnd: rs.orig[i] + j.Duration,
				Start: st, End: st + j.Duration,
				StartDelta: st - rs.orig[i], Changed: st != rs.orig[i],
			})
		}
	}
	return res
}
