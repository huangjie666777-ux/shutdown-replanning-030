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
