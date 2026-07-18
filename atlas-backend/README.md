# Atlas Backend

Atlas Backend is a Go 1.25 + Gin service with PostgreSQL-backed organization,
user, and session authentication. It is independently deployable and is not on
the current direct Atlas Native-to-Agent flight-control path.

For the system-wide boundary and newcomer reading path, see
[`../docs/README.md`](../docs/README.md). Backend architecture and its current
integration limits are documented in
[`../docs/atlas-backend.md`](../docs/atlas-backend.md).

## Start the complete local stack

Docker Compose starts PostgreSQL, applies migrations once, builds the API image,
then waits until the API can reach PostgreSQL:

```sh
cp .env.example .env
docker compose up --build
```

Verify it from another terminal:

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

Stop containers while keeping local database data:

```sh
docker compose down
```

## Authentication mental model

```text
HTTP handler
    -> auth service (rules, hashing, session lifetime)
        -> repository (SQL and transactions)
            -> PostgreSQL (constraints and persistence)
```

- Authentication proves who a user is.
- Authorization checks whether their role is `admin` or `operator`.
- The organization derived from the session is the tenant boundary.
- A client-supplied organization ID must never override the session organization.

Registration creates one organization and its first admin in a single transaction.
The desktop app does not expose registration, but the API remains available.
Public clients cannot choose their initial role. Additional operators will later
be created through an admin-controlled invitation flow.

## API

| Method | Route | Authentication | Purpose |
| --- | --- | --- | --- |
| `GET` | `/healthz` | None | Process liveness |
| `GET` | `/readyz` | None | PostgreSQL readiness |
| `GET` | `/api/v1/version` | None | Backend development version |
| `POST` | `/api/v1/auth/register` | None | Create organization, first admin, and session |
| `POST` | `/api/v1/auth/login` | None | Create a session from email/password |
| `GET` | `/api/v1/auth/me` | Bearer token | Resolve the current identity |
| `POST` | `/api/v1/auth/logout` | Bearer token | Revoke the current session |

### Create an organization through the API

```sh
curl -X POST http://127.0.0.1:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{
    "organizationName": "Sunnyside",
    "organizationSlug": "sunnyside",
    "displayName": "Atlas Admin",
    "email": "admin@example.com",
    "password": "a sufficiently long passphrase",
    "deviceName": "Development Mac"
  }'
```

The raw session token is returned only in successful registration/login responses.
Subsequent requests use:

```text
Authorization: Bearer atlas_session_...
```

## Passwords and sessions

Passwords use Argon2id with a unique salt. `password_hash` stores the algorithm,
parameters, salt, and digest; it never stores reversible password material.

Session tokens contain 256 random bits. PostgreSQL stores only their SHA-256
digest, so a sessions-table leak is not immediately usable as bearer credentials.

A session becomes invalid when any condition is true:

- no request has used it for 12 hours;
- it is more than seven days old;
- `revoked_at` is set;
- its user is disabled;
- its organization is suspended.

`last_seen_at` is updated at most every five minutes to avoid writing on every API
request. Records that have been inactive for 30 days are removed by a six-hour
cleanup loop. These values are configurable, but invalid combinations fail startup.

## Database schema and migrations

SQL migrations live in `migrations/` as paired up/down files. The Compose
`migrate` service uses `golang-migrate` and must finish successfully before the API
starts.

```text
organizations
    ├── users
    │     └── sessions
    ├── drones
    ├── vehicle_agents
    │     └── vehicle_agent_bindings
    │           └── communication_links
    └── vehicle_agent_enrollment_tokens
```

Database constraints are the final defense against invalid roles, statuses,
duplicate slugs, duplicate normalized emails, and malformed token digests.

Do not edit a migration after it has been applied to a shared environment. Add a
new numbered migration instead.

`golang-migrate` records the applied version in PostgreSQL's `schema_migrations`
table. Once version 1 is recorded, changing `000001_create_identity.up.sql` does
not cause it to run again. Create `000002_<description>.up.sql` and its matching
down migration for the next schema change.

### Reset the local backend database

To reset both the backend and native databases together, run
`./scripts/reset-databases.sh` from the repository root.

The following commands permanently delete the local Compose PostgreSQL volume,
including every organization, user, and session, and then create a fresh database:

```sh
docker compose down -v --remove-orphans
docker compose up --build -d
```

The second command starts PostgreSQL, applies all migrations to the empty database,
and starts the backend. Use this only for disposable local development data; never
reset a shared or production database this way.

## Transactions and TxManager

`internal/repositories` defines the repository contracts, the transaction-scoped
`Repositories` collection, and the `TxManager` boundary. `internal/database`
implements TxManager with pgx and exposes a transaction-only SQL interface. A
private marker prevents `pgxpool.Pool` from being passed to a repository.

The auth service receives one persistence dependency:

```go
repositories.TxManager
```

Every `Repositories` collection is constructed inside `WithinTransaction`. Its
accessors return repositories bound to
the same `pgx.Tx`. Application services have no pool-backed repositories. TxManager
commits only when the callback returns nil; any error or panic reaches rollback.

```text
auth service
    -> WithinTransaction
        -> one pgx.Tx
            -> Repositories
                -> Auth()
                    -> auth repository
                -> organization + user + session writes
```

Each persistence boundary has one accessor on `repositories.Repositories`:

```go
type Repositories interface {
    Auth() AuthRepository
    Drones() DroneRepository
    VehicleAgents() VehicleAgentRepository
    VehicleAgentBindings() VehicleAgentBindingRepository
    CommunicationLinks() CommunicationLinkRepository
    EnrollmentTokens() EnrollmentTokenRepository
}
```

The PostgreSQL collection constructs every repository from the `TxExecutor`
passed to it, ensuring cross-repository work shares one transaction. External APIs
and separate databases cannot join this local transaction; those workflows should
use an outbox, idempotent processing, or compensating actions.

## Vehicle-agent enrollment model

The vehicle-operations domain separates durable identity and attachment from
runtime connectivity:

```text
organization
    -> drone (physical vehicle)
    -> vehicle agent (installed software identity + Ed25519 public key)
        -> vehicle-agent binding (agent attached to one drone)
            -> communication links (one row per backend session)
```

An administrator creates a short-lived enrollment token. PostgreSQL stores only
its SHA-256 digest, and the token is scoped to the administrator's organization.
It can optionally be restricted to a pre-created drone. Enrollment atomically:

1. locks and validates the one-time token;
2. creates or matches the drone by flight-controller UID;
3. creates the vehicle-agent installation identity;
4. creates the agent-to-drone binding; and
5. records the exact agent and binding on the consumed token for idempotent retry.

The agent supplies a 32-byte Ed25519 public key and retains the private key on
the onboard computer. A future transport will authenticate that key. No vehicle-
agent HTTP or gRPC transport is exposed yet.

Communication links own all backend-session state. A future authenticated
transport calls the communication-link service when it accepts a channel,
creating a `connecting` link. The first heartbeat makes that same link `healthy`;
reconnects create new links under the unchanged vehicle-agent binding.

## Package map

- `cmd/atlas-backend`: process composition, signals, cleanup scheduling.
- `internal/config`: environment parsing and cross-field validation.
- `internal/data/models`: shared data structures with no business or SQL behavior.
- `internal/database`: pgx pool, TxExecutor adapter, and PostgreSQL TxManager.
- `internal/repositories`: repository contracts, errors, collection, and TxManager interface.
- `internal/repositories/postgres`: transaction-bound PostgreSQL implementations.
- `internal/services/auth`: authentication rules, password/token security, and orchestration.
- `internal/services/drones`: physical-drone creation and lookup rules.
- `internal/services/vehicleagents`: enrollment tokens, enrollment, and binding workflows.
- `internal/services/communicationlinks`: backend-session lifecycle and health.
- `internal/httpapi`: Gin transport, middleware, DTOs, rate limiting.
- `internal/server`: HTTP listener and graceful shutdown.
- `internal/validation`: reusable caller-correctable error shape and reason codes.

The interfaces are intentionally narrow. HTTP knows about request/response shapes;
the auth service knows security rules; the repository knows PostgreSQL.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ATLAS_HTTP_ADDR` | `127.0.0.1:8080` | HTTP listener |
| `ATLAS_DATABASE_URL` | Local Atlas PostgreSQL URL | pgx connection string |
| `ATLAS_ALLOWED_ORIGINS` | Tauri/Vite local origins | Browser CORS allow-list |
| `ATLAS_TRUSTED_PROXIES` | Empty | Comma-separated known proxy IPs/CIDRs |
| `ATLAS_SESSION_IDLE_TIMEOUT` | `12h` | Inactivity limit |
| `ATLAS_SESSION_ABSOLUTE_TIMEOUT` | `168h` | Maximum session age |
| `ATLAS_SESSION_RETENTION` | `720h` | Invalid-session record retention |
| `ATLAS_SHUTDOWN_TIMEOUT` | `5s` | Graceful request drain time |

Production should inject database credentials from its secret manager. Non-loopback
deployments must use TLS at the ingress or service boundary.

## Tests and debugging

Fast tests:

```sh
go test ./...
go vet ./...
```

PostgreSQL repository test after Compose is running:

```sh
ATLAS_TEST_DATABASE_URL='postgres://atlas:atlas@127.0.0.1:5432/atlas?sslmode=disable' \
  go test ./internal/repositories/postgres -run TestAuthRepositoryLifecycle -count=1 -v
```

Debug from the outside inward:

1. `docker compose ps` — are PostgreSQL and backend healthy?
2. `docker compose logs migrate backend` — did schema or startup fail?
3. `curl /readyz` — can the backend reach PostgreSQL?
4. Check HTTP status and safe error code.
5. Run the focused auth or repository test.

Expected failures:

- `400`: invalid caller input. The response includes a field and stable reason such
  as `required`, `invalid_format`, `too_short`, or `too_long`.
- `401`: missing, invalid, expired, or revoked session; or invalid credentials.
- `403`: insufficient role.
- `409`: organization slug or normalized email already exists.
- `429`: process-local authentication attempt limit reached.
- `503 /readyz`: PostgreSQL is unavailable.
