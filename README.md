# imp — Multi-Venue Inventory Management Platform (backend)

Go + Fiber + MongoDB backend for tracking physical assets (laptops, chairs,
etc.) across multiple venues. Every asset gets a QR code that resolves to an
authenticated asset page; authorized users move it between venues, change
custody, send to repair, and receive purchase orders. See [`docs/prd.md`](docs/prd.md)
for the product spec and [`openapi.yaml`](openapi.yaml) for the API contract.

## Status

MVP feature-complete. Auth, RBAC, venues, categories, users, assets (with
state machine), transfers, custody, repairs, purchase orders (with
transactional `/receive`), bulk CSV/XLSX PO import, QR codes (with
logo overlay) + bulk label PDFs, authenticated scan, email notifications via Gmail
SMTP (custody / transfer / repair-close triggers + daily overdue digest),
dashboard, and reports are all live.

## Requirements

- **Go 1.25+**
- **MongoDB replica set** (transactions are required for PO receive + bulk
  import). Standalone `mongod` will reject the receive flow at runtime — start
  with `mongod --replSet rs0` and `rs.initiate()`.
- [`oapi-codegen` v2](https://github.com/oapi-codegen/oapi-codegen) — only
  needed to regenerate models:
  ```bash
  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
  ```

`openapi.yaml` (OpenAPI 3.0.3) is the **single source of truth** for endpoints
and data shapes. Go model structs in `internal/models/models.gen.go` are
generated from it and carry both `json` and `bson` tags — one struct serves
both API I/O and Mongo persistence. **Never hand-edit generated files**: change
`openapi.yaml`, then `make generate`.

## Setup

```bash
cp .env.example .env        # fill in MONGO_URI, JWT_SECRET, etc.
make tidy                   # download deps
make run                    # start the API on :8080
```

The server refuses to start if `MONGO_URI`, `MONGO_DB`, or `JWT_SECRET` is
missing. If `SEED_ADMIN_EMAIL` + `SEED_ADMIN_PASSWORD` are set AND no admin
exists, it seeds one on first boot (idempotent — safe to leave set or unset
after).

### Health checks

```bash
curl http://localhost:8080/healthz   # liveness
curl http://localhost:8080/readyz    # readiness (also pings Mongo)
```

## Configuration (`.env.example`)

| Env                                    | Purpose                                                            |
|----------------------------------------|--------------------------------------------------------------------|
| `PORT`                                 | HTTP port (default `8080`)                                         |
| `APP_BASE_URL`                         | Public host of THIS API                                            |
| `FRONTEND_BASE_URL`                    | Public host of the web app — QR codes & email links resolve here  |
| `MONGO_URI` / `MONGO_DB`               | Mongo connection (required)                                        |
| `JWT_SECRET`                           | HMAC signing secret (required)                                     |
| `JWT_ACCESS_TTL` / `JWT_REFRESH_TTL`   | Token lifetimes (default `15m` / `720h`)                           |
| `GMAIL_USER` / `GMAIL_APP_PASSWORD`    | Gmail SMTP credentials (App Password, not regular password)        |
| `SMTP_HOST` / `SMTP_PORT`              | Default `smtp.gmail.com` / `587` (STARTTLS)                        |
| `SEED_ADMIN_EMAIL` / `..._PASSWORD`    | First-run admin seed; idempotent                                   |
| `SEED_ADMIN_NAME`                      | Display name for the seed admin (default `Admin`)                  |
| `OVERDUE_CRON`                         | Daily overdue scan schedule (default `0 9 * * *`)                  |
| `QR_LOGO_PATH`                         | Override for the QR center logo; falls back to embedded default    |
| `LOG_LEVEL`                            | `debug` / `info` / `warn` / `error` (default `info`)               |

If Gmail credentials are unset, the API falls back to a **log-only mailer**
(emails get logged as JSON instead of sent) — useful for local dev.

## Layout

```
cmd/api/                HTTP entrypoint, DI wiring, graceful shutdown
internal/
  apperror/             typed errors → HTTP status mapping
  config/               env loader
  database/             Mongo connection + index setup (driver v2)
  handler/              Fiber handlers (one file per resource)
  jwtauth/              JWT issuer (HS256, access + refresh)
  middleware/           auth, RBAC, request logging
  models/               OpenAPI-generated structs (DO NOT EDIT models.gen.go)
  notification/         outbox, Gmail mailer, async worker, event triggers
  qr/                   QR PNG + bulk-label PDF; logo compositing
  repository/           per-collection Mongo data access
  router/               central route registration
  scheduler/            robfig/cron — daily overdue scan
  service/              business logic (state machine, transfers, custody, etc.)
  validate/             go-playground/validator wrapper
pkg/response/           standard JSON envelope helpers
docs/prd.md             product spec
openapi.yaml            authoritative API + schema
```

## Architecture in one paragraph

Stateless Fiber API behind JWT. Layered: `handler` → `service` → `repository`
→ MongoDB (driver v2). All API and data shapes come from `openapi.yaml`;
`make generate` produces Go structs with paired `json` + `bson` tags so a
single type serves both wire and storage. Email is decoupled via an
**outbox**: triggers write notification docs synchronously, a goroutine
worker drains them via the Gmail SMTP interface (swappable for any
`notification.Mailer`). A `robfig/cron` scheduler runs the daily overdue scan
and enqueues digest emails. Asset state transitions are validated by a pure
table-driven state machine — same approach for repair ticket states.

## API surface (selected)

All endpoints under `/api/v1` are JWT-protected. See `openapi.yaml` for the
full spec.

```
POST   /auth/login                    # email + password → access + refresh
POST   /auth/refresh
GET    /auth/me

GET    /users               (admin)
POST   /users               (admin)
GET    /users/:id           (admin)
PUT    /users/:id           (admin)
DELETE /users/:id           (admin; 409 if user still referenced)

GET    /venues
POST   /venues              (admin)
GET    /venues/:id
PUT    /venues/:id          (admin)
DELETE /venues/:id          (admin; 409 if assets reference it)

GET    /categories          # same admin-write pattern as venues
POST   /categories          (admin)
...

GET    /assets              # filters: venue, currentVenue, category, status,
                            #          responsible, away, overdue, q
POST   /assets
GET    /assets/:id
PUT    /assets/:id
DELETE /assets/:id          (admin)
GET    /assets/:id/history          # movement timeline
GET    /assets/:id/qr               # PNG with optional center logo
POST   /assets/qr/bulk              # multi-page A4 PDF of labels
POST   /assets/:id/transfer         # body: { toVenueId, expectedReturnDate?, notes? }
POST   /assets/:id/status           # body: { status, reason? } — state-machine validated
POST   /assets/:id/assign           # body: { responsibleUserId, notes? } — emails new custodian

GET    /scan/:qrToken               # authed + per-asset RBAC — full contacts, resolves for lost/retired

GET    /purchase-orders
POST   /purchase-orders             (admin)
GET    /purchase-orders/:id
PUT    /purchase-orders/:id         (admin; 409 if received)
POST   /purchase-orders/:id/receive (admin)   # body: { venueId } — TRANSACTIONAL

# Bulk PO import (admin) — validate-then-commit, one Mongo tx per PO
GET    /imports/purchase-orders/template
POST   /imports/purchase-orders/validate     # multipart upload → preview
POST   /imports/purchase-orders/commit       # body: { importJobId, options? }
GET    /imports/:id
GET    /imports/:id/report                   # downloadable CSV result

GET    /repairs                              # filters: status, assetId
POST   /repairs                              # opens ticket + transitions asset to in_repair
GET    /repairs/:id
PUT    /repairs/:id                          # close → transitions asset to available/retired + emails

GET    /dashboard/summary
GET    /reports/{inventory-by-venue,assets-away,assets-overdue,in-repair,by-responsible}

GET    /me/notification-preferences          # self-service (any authed user)
PUT    /me/notification-preferences
POST   /me/password                          # body: { current, next }

GET    /notifications        (admin)         # outbox log
```

## Notable behaviour

- **State machine:** asset status transitions (`available`/`in_use`/`in_repair`/`retired`/`lost`)
  are validated by [`internal/service/transitions.go`](internal/service/transitions.go).
  Every transition writes a `movements` audit record.
- **Soft-block delete:** venues, categories, and users refuse to delete while
  any asset (or PO, for users) references them.
- **Custody changes** trigger an email to the new custodian; **transfers**
  email the home-venue manager(s); **repair close** emails the custodian +
  reporter (deduped). **Overdue** is a daily digest to the custodian only —
  never to managers/admins (PRD §6.11).
- **QR codes** are encoded at error-correction level Highest (≈30%) when a
  logo is overlaid, so the masked center stays scannable. The logo halo
  follows the logo's alpha silhouette rather than a rectangle, so the result
  blends with the QR modules. Override the embedded logo with `QR_LOGO_PATH`.
- **Scan URLs** encoded in QR codes point at `FRONTEND_BASE_URL/scan/<token>`,
  not the API — a phone-camera scan opens the user-facing page, not JSON. The
  URL format is unchanged, but the page now requires login and authorizes the
  viewer for the asset (admin, venue scope, or current custodian).

## Make targets

| Target     | What it does                                  |
|------------|-----------------------------------------------|
| `run`      | `go run ./cmd/api`                            |
| `build`    | builds `bin/api`                              |
| `test`     | `go test ./... -race -count=1`                |
| `generate` | regenerates `internal/models/models.gen.go`   |
| `tidy`     | `go mod tidy`                                 |
| `fmt`      | `go fmt ./...`                                |
| `vet` / `lint` | `go vet ./...`                            |

## Tests

```bash
make test
```

Coverage focus per PRD conventions: table-driven tests for the asset state
machine, repair state machine, overdue/digest grouping, QR scannability
(round-trip via gozxing), and the bulk-import resolver. Integration tests
against a real Mongo (Testcontainers) are an open follow-up.
