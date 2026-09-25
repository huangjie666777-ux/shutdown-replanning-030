# Shutdown Scheduler

Go1.27.1 and Chi5.2.1 HTTP service that plans factory shutdown maintenance:
it schedules a small set of indivisible jobs onto equipment with limited
maintenance crew, searching for the earliest possible overall completion.

## Run

```sh
go test ./...
go build -o bin/server ./cmd/server
./bin/server -addr 127.0.0.1:8080
```

## API

### `GET /healthz`

Returns `{"status":"ok"}`.

### `POST /schedule`

Request body (all times are integer minutes from shutdown start):

```json
{
  "horizon_minutes": 480,
  "budget_ms": 200,
  "crew_size": 3,
  "equipment": [
    {"id": "E1", "unavailable": [{"start": 60, "end": 90}]},
    {"id": "E2", "unavailable": []}
  ],
  "jobs": [
    {"id": "A", "equipment": "E1", "duration_minutes": 45, "crew": 2,
     "earliest_start": 0, "predecessors": []},
    {"id": "B", "equipment": "E2", "duration_minutes": 30, "crew": 2,
     "earliest_start": 0, "predecessors": ["A"]}
  ]
}
```

- `horizon_minutes`: planning horizon, 1..480. Every job must finish within it.
- `budget_ms`: wall-clock search budget in milliseconds (positive).
- `crew_size`: total maintenance workers; per-minute crew usage never exceeds it.
- `equipment[].unavailable`: half-open intervals `[start, end)` during which the
  equipment cannot run jobs. Back-to-back placement at interval edges is allowed.
- `jobs`: 1..12 jobs. Each job is indivisible, starts no earlier than
  `earliest_start`, and starts only after all `predecessors` finish. One job per
  equipment at a time; jobs on different equipment may overlap.

Invalid values, duplicate IDs, unknown references, and dependency cycles are
rejected with `400` and a field-located error:

```json
{"error": {"field": "jobs[0].predecessors[0]", "message": "unknown job id ZZ"}}
```

Successful response:

```json
{
  "status": "optimal",
  "makespan_minutes": 75,
  "schedule": [
    {"id": "A", "equipment": "E1", "start": 0, "end": 45},
    {"id": "B", "equipment": "E2", "start": 45, "end": 75}
  ]
}
```

## Search strategy

The solver is an exact depth-first branch-and-bound over job start times. At
each node it branches on every unscheduled job whose predecessors are placed
and every feasible start minute for it, so deliberately waiting for a better
slot is explored — the plan is not a greedy list schedule. A lower bound on
the achievable makespan (predecessor chains plus earliest starts) prunes
branches that cannot beat the incumbent. Among minimum-makespan plans, the
one whose start-time sequence (jobs ordered by ID) is lexicographically
smallest wins. Optimality or infeasibility is only reported after the search
space is exhausted. Each request gets its own solver state, budget, and
deadline; cancellation of the HTTP request stops the search. The service
keeps no state between requests and talks to no external systems.

## Status values

- `optimal`: search exhausted; returned plan has the proven earliest makespan.
- `infeasible`: search exhausted; no complete plan exists within the horizon.
- `timeout_feasible`: budget spent; best complete plan found so far is
  returned, but a better one may exist.
- `timeout_no_solution`: budget spent before any complete plan was found;
  this does **not** mean the instance is infeasible.
- `canceled`: the client canceled the request mid-search.

### `POST /reschedule`

Stateless minimum-perturbation rescheduling after a temporary equipment
outage. The request carries the full original problem, the complete original
schedule (each of the 1..12 jobs exactly once; it must satisfy every original
constraint — optimality is not required), the current minute `t_minutes`, the
new outage windows, and optional locks:

```json
{
  "budget_ms": 500,
  "t_minutes": 30,
  "problem": {
    "horizon_minutes": 200,
    "crew_size": 3,
    "equipment": [{"id": "E1", "unavailable": []}, {"id": "E2", "unavailable": []}],
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
  ],
  "locked_job_ids": []
}
```

Rules:

- `t_minutes` is within `[0, horizon_minutes]`. A job whose original end is at
  or before T is `completed`; a job with original start `< T` and end `> T` is
  `in_progress`. Both are frozen: their start and end never change, and an
  in-progress job keeps occupying its equipment and crew over its remaining
  span. Jobs whose original start is at or after T are `unstarted` and may not
  start before T.
- New outage windows must not start before T; they are merged with the
  equipment's original outages when constraining the new plan.
- `locked_job_ids` only names unstarted jobs; a locked job keeps its original
  start and end. If a frozen or locked placement (including the remainder of an
  in-progress job) collides with a new outage, equipment/crew use, or
dependency, the result is `infeasible` — the conflict is never silently
dropped.
- All original constraints still hold: predecessors, one job per equipment per
  minute, crew capacity, indivisibility, and the 480-minute horizon.
- Invalid payloads, unknown ids, and schedules inconsistent with the problem
  are rejected with `400` and a field path such as
  `original_schedule[2].start` or `new_unavailable[0].equipment`.

The exact search lexicographically minimizes, in strict priority order:

1. the number of unstarted jobs whose start changes,
2. the sum of absolute start-minute shifts,
3. the overall completion (makespan),
4. the start-time sequence of jobs ordered by ID.

A plan is never changed just to finish earlier. Response:

```json
{
  "status": "optimal",
  "t_minutes": 30,
  "makespan_minutes": 75,
  "changed_jobs": 1,
  "total_start_shift_minutes": 15,
  "schedule": [
    {"id": "A", "equipment": "E1", "start": 0, "end": 20},
    {"id": "B", "equipment": "E1", "start": 20, "end": 40},
    {"id": "C", "equipment": "E2", "start": 55, "end": 75}
  ],
  "changes": [
    {"id": "A", "equipment": "E1", "state": "completed", "original_start": 0, "original_end": 20, "start": 0, "end": 20, "start_delta_minutes": 0, "changed": false},
    {"id": "B", "equipment": "E1", "state": "in_progress", "original_start": 20, "original_end": 40, "start": 20, "end": 40, "start_delta_minutes": 0, "changed": false},
    {"id": "C", "equipment": "E2", "state": "unstarted", "original_start": 40, "original_end": 60, "start": 55, "end": 75, "start_delta_minutes": 15, "changed": true}
  ]
}
```

The same status values as `/schedule` apply (`optimal`, `infeasible`,
`timeout_feasible`, `timeout_no_solution`, `canceled`). A timeout with a
complete plan returns that plan as `timeout_feasible` without claiming
optimality; a timeout before any complete plan is found returns
`timeout_no_solution` without claiming infeasibility. Canceling the HTTP
request stops the search; every request has independent solver state.
