# Shutdown Scheduler

Go1.27.1 and Chi5.2.1 HTTP starter. The current implementation only exposes `GET /healthz`.

Dependencies are locked in go.mod/go.sum and included in vendor. The Go toolchain is provided by WSL; no project-specific PATH setup is needed.

```sh
go test ./...
go build -o bin/server ./cmd/server
go run ./cmd/server -addr 127.0.0.1:8080
```

Use another free local port when needed. No scheduling behavior is implemented in this starter.
