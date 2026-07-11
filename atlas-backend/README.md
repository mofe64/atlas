# Atlas Backend

This is the new Atlas product backend: a Go 1.25 HTTP service built with Gin.
It intentionally begins with process-level foundations rather than carrying old
domain assumptions into the new architecture.

The previous implementation is preserved in `../atlas-backend-deprecated`.

## Mental model

```text
cmd/atlas-backend/main.go
        │ composition: config + router + process lifecycle
        ├── internal/config       environment -> typed Config
        ├── internal/httpapi      HTTP transport, middleware, routes
        └── internal/server       listen + graceful shutdown
```

`main` is a composition root: the one place where concrete pieces are assembled.
Keeping business rules out of `main` makes future packages independently testable.

`internal/` is enforced by the Go toolchain. Code outside this module cannot import
these packages, so internal implementation remains free to evolve without becoming
a public SDK accidentally.

## Run it

Gin requires Go 1.25 or newer. Verify your active toolchain first:

```sh
go version
go run ./cmd/atlas-backend
```

Then check the process from another terminal:

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/api/v1/version
```

The default listener is loopback-only. This is deliberate for a desktop-first
foundation: other machines cannot reach the API until we explicitly design network
exposure, authentication, and transport security.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ATLAS_HTTP_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `ATLAS_SHUTDOWN_TIMEOUT` | `5s` | Time allowed for in-flight requests to finish |
| `ATLAS_ALLOWED_ORIGINS` | Tauri and Vite local origins | Comma-separated browser origins allowed by CORS |

Example:

```sh
ATLAS_HTTP_ADDR=127.0.0.1:8081 \
ATLAS_SHUTDOWN_TIMEOUT=10s \
go run ./cmd/atlas-backend
```

Configuration is parsed before the listener starts. Invalid durations fail fast
instead of leaving a partially configured service running.

## Request flow

1. `net/http` accepts a connection.
2. Gin runs logger, panic recovery, and CORS middleware in order.
3. Gin matches the method and path to a handler.
4. The handler writes a JSON response.
5. On SIGINT/SIGTERM, `server.Run` stops accepting new requests and gives active
   requests up to `ATLAS_SHUTDOWN_TIMEOUT` to finish.

Routes under `/api/v1` are versioned so later breaking API changes can coexist
during migrations. `/healthz` stays outside the product API because process probes
should not depend on a domain API version.

## Test and debug

```sh
go test ./...
go test ./internal/httpapi -run TestHealth -v
```

Debug from the outside inward:

1. `curl /healthz` checks listener, routing, and JSON without involving Tauri.
2. If curl works but the app says offline, inspect the webview console, API URL,
   and CORS origin.
3. If the process exits immediately, its JSON log names configuration or bind errors.
4. An `address already in use` error means another process owns port 8080; either
   stop it or set `ATLAS_HTTP_ADDR` and matching `VITE_ATLAS_API_URL`.

CORS is a browser rule, not authentication. It prevents an unapproved web origin
from reading responses, but it does not prove who sent a request. Authentication
must be designed separately when protected domain endpoints arrive.
