# Product Requirements Document — Multi-Venue Inventory Management Platform

| | |
|---|---|
| **Document status** | Draft v0.7 |
| **Owner** | _you_ |
| **Last updated** | 2026-07-08 |
| **Stack** | Go 1.25 (Fiber v2 · MongoDB driver v2) · MongoDB · Next.js |
| **API contract** | [`openapi.yaml`](../openapi.yaml) (OpenAPI 3.0.3) is the single source of truth for endpoints + schemas; Go model structs are generated from it via `oapi-codegen` |

> **Locked scope decisions (v0.5):** single organization (no multi-tenancy) · expected-return tracking + overdue alerts in MVP, delivered as a **daily digest only** to the **responsible person only** · per-asset custody reassignment from day one · **authenticated, per-asset-authorized** scan (admin, venue scope, or current custodian) shows full asset details **including the custodian's contact details** and **still resolves for lost/retired assets** for authorized callers (see v0.8) · **no financials** — pure location/custody tracking · email notifications for v1 via **Gmail SMTP** · **bulk PO import** (CSV/XLSX) with a validate-then-commit flow, transactional commit per PO, and notification suppression · self-service `/me/*` endpoints (password change, notification preferences) · **venue-scoped departments** (§6.14) as an optional grouping dimension on assets, propagated through import, the scan view, and reporting.

### Changelog
- **v0.10 (2026-07-14):** Added `POST /assets/bulk/ids` — an async **asset-id export** job. It collects asset `_id`s ONLY (no documents) matching the **exact same filter set as `GET /assets`** (`venue`, `currentVenue`, `category`, `department`, `status`, `responsible`, `away`, `overdue`, `q`), up to an adjustable `limit`, and delivers the result as a downloadable JSON artifact fetched from the existing `GET /assets/bulk/jobs/:jobId/result` sub-resource (now serving `application/json` for `ids` jobs alongside `application/pdf` for `qr`). Primary use case: selecting large sets by filter to feed the bulk asset actions. Filter construction AND venue scoping are **shared code with `GET /assets`** (one `service.BuildAssetListQuery` parser + the existing `buildAssetFilter`, two callers, zero drift), so a manager/staff can only ever export ids of assets they could see in the list. Enqueue validation is synchronous (400): malformed ObjectId filters reuse the list endpoint's per-field messages, and `limit` must be `1..ASSET_IDS_MAX_LIMIT` (optional, defaults to the cap). The worker is **read-only**: no transactions, notifications, attachments, or per-row errors. It counts matches cheaply with a `limit+1`-capped `CountDocuments` (sets `truncated`), then fetches ids via **keyset pagination**, newest first (`find(filter ∧ (createdAt,_id) < cursor)`, `sort {createdAt:-1,_id:-1}`, projection `{_id:1,createdAt:1}`, batch size `ASSET_IDS_BATCH_SIZE`, default 1000; backed by a new `{createdAt:-1,_id:-1}` asset index that also serves the `GET /assets` list sort), heartbeating the lease and updating progress between batches. The artifact (`{jobId, generatedAt, count, truncated, assetIds}`) is written once at completion via `FileStorage` under the `bulk-jobs/` prefix and falls under `BULK_RESULT_TTL_DAYS` retention (the cleanup filter now covers `ids` as well as `qr`). IDs are **descending by `createdAt`** (newest first, following the `GET /assets` list sort, with `_id` as a stable tiebreak — the most recently created matching assets, not name/relevance); when more assets match than `limit`, the **newest `limit`** are kept (the oldest are dropped). They reflect assets as observed during the scan (not a point-in-time snapshot). Zero matches still writes an empty array so `/result` is deterministic. On lease-expiry reclaim after a crash the scan **restarts from zero** (read-only and cheap; no cursor-resume). Terminal statuses are `completed` (including truncated) or `failed` (infra error) — never `completed_with_errors`. New env: `ASSET_IDS_MAX_LIMIT` (default 100000), `ASSET_IDS_BATCH_SIZE` (default 1000). No synchronous mode (one contract, consistent with the v0.9 bulk-jobs decision); JSON only (CSV/assetTag columns are a follow-up); `GET /assets` behavior is unchanged.
- **v0.9 (2026-07-13):** Made all five bulk asset actions (`/assets/bulk/transfer`, `/assets/bulk/status`, `/assets/bulk/assign`, `/assets/condition/bulk`, `/assets/qr/bulk`) **asynchronous and job-based**. Each endpoint now returns **`202 Accepted` + a `BulkJob` resource** and executes in the background via an in-process worker; poll `GET /assets/bulk/jobs/:jobId` for status and fetch a completed QR PDF from `GET /assets/bulk/jobs/:jobId/result` (404 before ready, 410 after the retention window; `GET /assets/bulk/jobs` lists jobs for admins). **Enqueue-time validation stays synchronous and preserves the prior 400/diagnostics contracts** (malformed requests still 400; strict per-row validation failures still return the prior per-row diagnostics shape with nothing enqueued; assign still 400s on an unknown/inactive target user). **Whole-job atomicity is relaxed to PER-BATCH atomicity:** the worker processes assets in batched Mongo transactions (`BULK_BATCH_SIZE`, default 100), and rows invalidated between enqueue and execution (TOCTOU) become row errors or skips rather than aborting the job — deliberately mirroring the §6.12 import pipeline's partial-progress contract. Every Movement is stamped `performedBy` = the enqueuing principal (authorization is evaluated **once, at enqueue**) and `performedAt` = batch execution time, plus a new `bulkJobId` provenance stamp (mirrors `importJobId` on assets/POs). The per-asset cap is raised from 500 to **`BULK_MAX_ASSETS` (default 5000)**, with `assetIds` deduped (first-occurrence order) at enqueue; an optional `validOnly` flag on the four mutating endpoints enqueues the valid rows and records invalid ones as job errors instead of failing the whole request. **Notification guarantees are unchanged and enforced once per job at completion:** a multi-batch transfer still enqueues exactly one digest per home-venue manager, and a multi-batch assign exactly one custody digest to the new custodian (aggregated from Movements stamped with `bulkJobId`); status/condition still enqueue none. The worker uses an **atomic `findOneAndUpdate` claim + lease with expiry-based reclaim** (the reference fix for the §15 "no inter-instance worker lock" gap): the batch cursor advances only inside the committed batch transaction, so a crash + reclaim can never double-apply a batch, and transient batch failures retry with backoff before their rows are errored out (the job never wedges). New `bulk_jobs` collection (indexes `{status,createdAt}`, `{requestedBy,createdAt}`, `{leaseExpiresAt}`) + a `movements.{bulkJobId}` index. Attachments referenced by a job are **reserved** at enqueue (`reservedByJobId`) and exempted from the orphan sweep until the job is terminal, closing the window where a queued job's not-yet-linked attachments could be swept. QR jobs render outside any transaction, store the PDF via `FileStorage` under a `bulk-jobs/` prefix, and are cleaned up after `BULK_RESULT_TTL_DAYS` (default 7) by a new daily cron. New env: `BULK_MAX_ASSETS`, `BULK_BATCH_SIZE`, `BULK_WORKER_CONCURRENCY`, `BULK_WORKER_POLL_INTERVAL`, `BULK_JOB_LEASE_SECONDS`, `BULK_JOB_MAX_ATTEMPTS`, `BULK_JOB_ERROR_CAP`, `BULK_RESULT_TTL_DAYS`, `BULK_RESULT_CLEANUP_CRON`. The single-asset action endpoints and their Movement shapes are unchanged (single and bulk paths share the same apply helpers); `GET /scan/:qrToken` still exposes no attachment data (regression test green).
- **v0.8 (2026-07-10):** Locked down `GET /scan/:qrToken` — it is **no longer public**. The route moved into the JWT-protected group, so a missing/invalid token now returns 401. After authentication the scan service authorizes the caller per-asset (the same rule as `GET /attachments/:id/download`, via a shared `Principal.CanAccessAsset` helper): admin, OR venue scope on the asset's home or current venue, OR the asset's current custodian; out-of-scope callers get 403, unknown tokens 404. Lost/retired assets still resolve for authorized callers. Because every viewer is now authenticated **and** authorized, the responsible person's contact details (email/phone) are **unmasked** in the scan response — the old masked `PublicUserContact`/`PublicAssetView` schemas were renamed to `ScanUserContact`/`ScanAssetView` and the contact fields added. The scan response still exposes **no attachment data** (unchanged, still locked in by regression test). The **QR URL format, qrToken generation, and printed labels are unchanged** — only the access rules changed; email deep-links keep pointing at `/scan/:qrToken` (recipients are account holders who pass through the login redirect). Rate-limiting the endpoint is a follow-up (§15).
- **v0.7 (2026-07-08):** Added file attachments on every asset action, single and bulk. New endpoints `POST /attachments` (multipart upload; server-side sniffs content type, enforces 10 MB max, produces an unlinked attachment doc) and `GET /attachments/:id/download` (streams file bytes with venue/custody RBAC — admin, or a manager/staff scoped to the asset's home or current venue, or the current custodian; unlinked docs 404 for everyone including admins). All eight asset-action endpoints (single + bulk transfer/status/assign/condition) accept an optional `attachmentIds[]` (max 5). Attachment IDs are validated upfront: whole-request 400 on any failure with a top-level `attachments` diagnostic array mirroring the bulk per-row shape. Linking happens inside the same Mongo transaction that writes the Movement(s), so aborted bulk txns leave attachments unlinked (orphan sweep collects). All four single-asset actions now run inside a Mongo transaction — closes the pre-v0.7 "rare crash window" between the asset update and its movement insert. GET `/assets/:id/history` enriches each movement with `attachments[]` metadata (id, filename, contentType, size only; no storage keys, no URLs). `POST /assets/condition/bulk` keeps its best-effort per-item semantics; the shared attachment set is validated once upfront and accumulates on the doc as items succeed. Public `/scan/:qrToken` continues to expose NO attachment data — locked in by regression test. New MongoDB collection `attachments` (indexes on `{linked,createdAt}`, `uploadedBy`, `assetIds`). New daily orphan-sweep cron (default `0 3 * * *`) deletes unlinked docs older than 24h and their bytes. Storage abstraction (`internal/storage.FileStorage`) with a local-disk MVP under `STORAGE_BASE_DIR` (default `/var/lib/imp/attachments`); S3 is a future swap without touching call sites. Content-type allow-list (sniffed server-side): `image/jpeg`, `image/png`, `image/webp`, `application/pdf`.
- **Known limitation (v0.7):** a microsecond-window race exists in `AttachmentRepository.MarkLinked` where two concurrent requests that both pass validation before either commits can cross-link the same attachment to two unrelated actions. Real-world impact is minimal (concurrent write to the same attachmentId is required). A post-MVP fix moves validation inside the transaction callback.
- **v0.6 (2026-07-07):** Added `POST /assets/bulk/assign` — reassigns the custodian across up to 500 assets in one Mongo transaction. Mirrors the bulk transfer/status all-or-nothing contract (per-row diagnostics on validation failure, no DB writes), silently skips assets already assigned to the target (counted as `skippedNoOp`), and — critically for the Gmail quota — enqueues exactly **one digest email** to the new custodian listing all newly assigned assets, not one email per asset. Extended §6.3 bulk operations list and §6.11 notification triggers accordingly. Extracted a shared `applyAssignCustody` helper so single-asset and bulk paths write the same `custody_change` Movement shape.
- **v0.5 (2026-07-07):** Documentation catch-up to the shipped implementation. Added §6.14 **Departments** (venue-scoped grouping, CRUD, soft-block, home-venue invariant) plus `departments` collection in §8 and department routes in §9. Extended §6.3 bulk operations to match what actually ships: **bulk transfer**, **bulk status**, **bulk condition**, and **bulk QR** (each capped at 500 assets); removed the aspirational "bulk create" line — no bulk-create endpoint exists. Added `/reports/by-department` to §6.9. Added optional `departmentCode` column to §6.12 template shape. Called out on §6.4 that the public scan shows department name when set. Moved §6.10 audit log to a **partially-wired** status (schema declared in `openapi.yaml`, no service/repository writes today) and added it to §15 follow-ups.
- **v0.4 (2026-06-29):** Added §6.12 Bulk PO import and §6.13 Self-service (`/me/*`). Extended §6.11 notifications outbox schema to carry rendered `subject` / `body` / `recipientEmail` so the worker doesn't re-resolve at send time. Documented that PO `/receive` takes the destination `venueId` in the body. Added `import_jobs` collection and `importJobId` traceability stamp on `assets` + `purchase_orders`. Added §10 note that `openapi.yaml` is the authoritative API + schema spec.
- **v0.3 (2026-06-25):** Locked overdue-alerts-in-MVP, daily-digest-to-custodian-only, public-scan masking, email via Gmail SMTP.

---

## 1. Overview

A platform to track physical assets (chairs, tables, laptops, etc.) across **multiple venues**. Every asset carries a **QR code** that, when scanned, shows its identity, current location, status, and who is responsible for it. The system tracks whether an asset is **in use at its home venue**, **in use at another venue** (moved/loaned out), or **in repair**, and ties responsibility for assets back to the **purchase order** they came from.

### 1.1 Problem statement
Assets are spread across venues and physically move around — borrowed by another venue, sent out for repair, reassigned to new owners. Without a single source of truth, it's hard to answer basic questions: *Where is laptop #14 right now? Whose responsibility is it? Is that batch of chairs still under the original PO owner? What's currently out for repair?* This platform makes every physical asset traceable from purchase to disposal.

### 1.2 Goals
- Single source of truth for every asset across all venues.
- QR-scan workflow that works on a phone: scan → see details → update status/location in seconds.
- Clear separation of **lifecycle status** (in use / in repair / etc.) from **location** (home vs. other venue).
- Full movement and custody history (audit trail) for every asset.
- Tie each asset to a purchase order and a **responsible person**.

### 1.3 Non-goals (for now)
- **Financials of any kind** — no purchase-value tracking, no depreciation, no accounting/ERP integration. Tracking is location + custody only.
- **Multi-tenancy** — single organization; no `orgId` namespacing.
- Procurement/approval workflows (multi-step PO approvals, budgets).
- Consumables/stock-level management (this is **fixed-asset tracking**, not warehouse stock with reorder points).
- Native mobile apps (mobile is handled via responsive web / PWA).

---

## 2. Personas & roles

| Role | Description | Core needs |
|---|---|---|
| **Admin** | Owns the whole system. | Manage venues, users, categories; full visibility; configure everything. |
| **Venue Manager** | Responsible for one or more venues. | See/manage assets at their venue(s), approve incoming/outgoing transfers, raise repairs. |
| **Staff / Operator** | On-the-ground user. | Scan QR, update status, log a transfer or repair, look up an asset. |
| **Responsible Person (Custodian)** | The accountable owner of an asset or PO batch. | See what they're accountable for; acknowledge custody transfers. *(May also be one of the roles above.)* |

> RBAC note: a user can be a Venue Manager for venue A and also a Custodian for assets located elsewhere. Permissions are scoped by role **and** by venue membership.

---

## 3. Key concepts & glossary

- **Venue** — a physical location that holds assets.
- **Department** — a venue-scoped grouping *inside* a venue (e.g., "IT" or "Housekeeping" at Venue A). Optional on an asset. An asset's department must belong to that asset's **home venue** — see §6.14.
- **Category (Asset Type)** — the kind of thing an asset is (Laptop, Chair, Table…). Defines optional custom fields (e.g., laptops have a serial number / specs).
- **Asset** — one individual physical item. Has a unique asset tag and a unique QR token.
- **Home Venue** — where an asset *belongs*.
- **Current Venue** — where an asset *physically is right now*. When `currentVenue ≠ homeVenue`, the asset is **away** (in use at another venue).
- **Status** — the lifecycle state (Available, In Use, In Repair, Retired, Lost).
- **Movement** — a logged event: transfer, status change, or custody change. The audit trail.
- **Purchase Order (PO)** — a record of a purchase. Has a **responsible person** and produces one or more assets when received.
- **Custodian / Responsible Person** — the user accountable for an asset (inherited from its PO, reassignable later).

---

## 4. Core model decision: status vs. location

The request "in use (in original venue or another venue) or in repair" mixes two independent dimensions. The platform keeps them **separate** so they can be combined cleanly:

- **Status** (lifecycle): `available` · `in_use` · `in_repair` · `retired` · `lost`
- **Location** (derived): compare `homeVenueId` vs `currentVenueId`.

The user-facing label is a combination of the two:

| Status | Location | Displayed as |
|---|---|---|
| `in_use` | home == current | **In use — home venue** |
| `in_use` | home ≠ current | **In use — at {venue}** (away) |
| `available` | home == current | **Available — home venue** |
| `available` | home ≠ current | **Available — at {venue}** |
| `in_repair` | (wherever) | **In repair** |
| `retired` | — | **Retired** |
| `lost` | — | **Lost / missing** |

This avoids an explosion of status enums and makes "where is it" and "what state is it in" each answerable on their own.

---

## 5. Asset lifecycle (state machine)

```
                 assign/deploy
   ┌─────────────┐ ─────────────▶ ┌─────────────┐
   │  AVAILABLE  │                │   IN_USE    │
   │             │ ◀───────────── │             │
   └─────┬───────┘   return       └──────┬──────┘
         │                                │
         │  report damage   report damage │
         ▼                                ▼
   ┌──────────────────────────────────────────┐
   │                IN_REPAIR                  │
   └───────┬───────────────────────┬──────────┘
           │ repaired              │ unrepairable
           ▼                        ▼
     (back to AVAILABLE        ┌──────────┐
      or IN_USE)               │ RETIRED  │
                               └──────────┘

   Any state ──mark lost──▶ LOST ──found──▶ AVAILABLE
   Any state ──dispose──────────────────▶ RETIRED
```

**Transfer** (changing `currentVenueId`) is *orthogonal* to status — an asset can be transferred while `available` or `in_use`. Every transition writes a **Movement** record.

Allowed transitions (enforced server-side):

| From | To | Trigger |
|---|---|---|
| available | in_use | Assign/deploy |
| in_use | available | Return/unassign |
| available, in_use | in_repair | Report damage / send to repair |
| in_repair | available, in_use | Repair completed |
| in_repair | retired | Marked unrepairable |
| available, in_use | retired | Dispose |
| any | lost | Mark lost |
| lost | available | Found |

---

## 6. Functional requirements by module

### 6.1 Venue management
- CRUD venues (name, code, address, type, active flag).
- Cannot delete a venue that still has assets assigned as home venue (soft-block / require reassignment).
- Each venue shows a live count of assets by status.

### 6.2 Category (asset type) management
- CRUD categories (Laptop, Chair, Table…).
- Per-category **custom fields** (e.g., laptops → serial number, CPU, RAM; chairs → material). Stored as a flexible map on the asset.

### 6.3 Asset management
- CRUD assets. On create, the system auto-generates a unique **asset tag** (e.g., `LAP-0001`) and a unique **QR token**.
- Fields: name, category, home venue, current venue, **department (optional, home-venue-scoped — see §6.14)**, status, condition (`new/good/fair/poor`), responsible user, linked PO, purchase date, serial number, specs (custom fields), photos, notes. *(No price/value fields — custody tracking only.)*
- List view with filters: venue (home/current), category, status, responsible person, "away from home", free-text search (tag/serial/name).
- Asset detail page shows current state + **full history timeline** (movements, status changes, custody changes, repairs).
- **Bulk operations (shipped):**
  - `POST /assets/bulk/transfer` — transfer up to 500 assets to a target venue in one Mongo transaction; per-asset RBAC checked; deduplicated **transfer digest** email enqueued per home venue (§6.11).
  - `POST /assets/bulk/status` — status change on up to 500 assets in one transaction, validated against the §5 state machine per item; no notifications (status changes are silent in bulk).
  - `POST /assets/bulk/assign` — reassign the custodian across up to 500 assets in one transaction; per-asset RBAC checked; assets already assigned to the target user are silently skipped as no-ops (counted in `skippedNoOp`, no Movement written); exactly **one custody digest** email is enqueued to the new custodian listing all assets they were just assigned (§6.11). Unknown / inactive `responsibleUserId` is a whole-request 400.
  - `POST /assets/condition/bulk` — condition update on up to 500 assets, **best-effort per item** (per-item transactions); silently skips unchanged / not-found / forbidden items.
  - `POST /assets/qr/bulk` — combined PDF of QR labels for a set of asset IDs (used for per-PO batch printing — §6.7).
- **Bulk create is deliberately not offered** as a separate endpoint. New assets in batches come from PO receive (§6.7) or the bulk PO import (§6.12); ad-hoc bulk create is out of scope for v1.
- **File attachments on actions (v0.7):** every asset-action endpoint — the four single-asset actions (`transfer`, `status`, `assign`, `condition`) and their four bulk counterparts — accepts an optional `attachmentIds[]` (max 5), letting the caller attach photos or documents (e.g. a signed transfer note, a condition photo) to the resulting movement. Attachments are uploaded separately first via `POST /attachments` (§9), then referenced by id on the action call; linking happens inside the same transaction as the movement write. Action attachments show up in the asset's history (`GET /assets/:id/history`) as lightweight metadata (id, filename, contentType, size) on each movement — never storage keys or raw URLs.

### 6.4 QR code system
- Each asset has a unique, **non-enumerable** QR token (random, not the raw Mongo `_id`) to prevent guessing/scraping.
- QR encodes a URL: `https://app.example.com/scan/{qrToken}` (format unchanged — printed labels keep resolving).
- Scanning opens a **mobile-optimized asset page** that shows full details (identity, category, home/current venue, **department name if set (§6.14)**, status, **responsible person's name and role**, history). The page **requires login** and authorizes the viewer for that specific asset (admin, OR venue scope on the asset's home or current venue, OR the asset's current custodian); an out-of-scope viewer gets 403. Authorized viewers see the responsible person's **full contact details (email/phone)** — there is no longer a masked public view. Mutating actions (update status, transfer, raise repair, reassign custody) require authentication as before.
- The scan **still resolves for lost and retired assets** (shows the asset with its `lost`/`retired` status) **for authorized callers**, rather than returning a dead page — useful if an authorized user finds a lost item and scans it.
- The QR token is still random/non-enumerable: a scanned code reveals the asset, but the URLs can't be guessed or scraped in bulk.
- Generate a printable label (PNG/PDF) per asset and **batch print** for a set of assets (e.g., a whole PO's worth at once).
- QR generation in Go (e.g. `github.com/skip2/go-qrcode`); scanning in the browser (e.g. `@zxing/browser` or `html5-qrcode`) using the device camera.

### 6.5 Transfers, expected return & overdue (location tracking)
- Transfer an asset from one venue to another → updates `currentVenueId`, writes a **Movement** of type `transfer`.
- **Temporary transfers** carry an `expectedReturnDate`. A scheduled job flags assets past that date as **overdue** and surfaces them on the dashboard.
- **Overdue email alerts (MVP):** a once-daily scheduled job flags assets past `expectedReturnDate` and sends a **single daily digest to the asset's responsible person only** listing everything of theirs that is overdue. No instant per-event email — digest only. *(See §6.11 Notifications.)*
- "Return home" action resets `currentVenueId = homeVenueId` and clears `expectedReturnDate`.
- Optional approval: venue manager of the receiving venue confirms receipt (configurable; off by default for MVP).

### 6.6 Repair management
- "Send to repair" → status `in_repair`, opens a **Repair ticket** (issue description, reported by, optional vendor).
- Repair ticket fields: issue, status (`open/in_progress/completed/unrepairable`), vendor, cost, sent date, returned date, resolution notes.
- On completion → asset returns to `available` (or `in_use`); on `unrepairable` → asset `retired`.
- Report of all assets currently in repair, with age of each ticket.

### 6.7 Purchase orders & responsibility
- Create a PO: PO number, supplier, order date, line items (category, name, quantity), attachments (invoice), **responsible person**. *(No prices/totals — custody tracking only.)*
- **Receiving a PO** generates the individual asset records (one asset per unit per line item), each pre-filled with category, purchase date, linked PO, and **the PO's responsible person as the initial custodian**. The receive request body carries the **destination `venueId`** — it becomes both `homeVenueId` and `currentVenueId` on every generated asset. Generation runs in a **single MongoDB transaction** (asset inserts + PO status flip + counter increments) so it's all-or-nothing. A received PO is locked from further `PUT` edits; corrections require admin tooling outside the normal API.
- **Per-asset custody reassignment is a day-one (MVP) feature:** any asset's responsible person can be changed independently (custody change → Movement of type `custody_change`). The PO records the *original* owner; each asset always carries its *current* custodian. A custody change emails the new custodian.
- Report: assets grouped by responsible person; assets still owned by their original PO custodian.

### 6.8 Users, auth & permissions
- Email/password auth with JWT (access + refresh tokens).
- Roles: Admin, Venue Manager, Staff. Venue managers/staff are scoped to assigned venues.
- Users can be selected as PO responsible person / asset custodian.
- Audit of who did what (every mutating action records `performedBy`).

### 6.9 Dashboard & reporting
- Dashboard summary: total assets, by status, by venue, count away from home, count in repair, overdue returns.
- Reports: inventory by venue, assets away from home, overdue, in repair, by responsible person, and **by department** (per-venue breakdown — see §6.14). Additional cuts (by category, by PO) are planned but not yet exposed.
- Export to CSV/Excel for any report (later phase).

### 6.10 Audit log
- Intent: an immutable log of all create/update/delete and state-changing actions, with actor, timestamp, before/after diff.
- **Current status (v0.5): partially wired.** The `audit_logs` schema is declared in `openapi.yaml` and reserved in §8, and every mutating handler already carries `performedBy`, but **no service/repository writes to `audit_logs` today** — the append-only source of truth in production is the `movements` collection (per-asset timeline) plus the `notifications` outbox (rendered emails). Filling in general-purpose audit writes is tracked in §15.

### 6.11 Notifications (email, v1)
- **Channel:** email only for v1, sent via **Gmail SMTP** (`smtp.gmail.com`, port 587 STARTTLS). The mail layer is abstracted behind an interface so a dedicated provider (Resend/SES/Postmark) can be swapped in later without touching business logic.
- **Gmail auth & limits (important):** Gmail SMTP requires an **App Password** (16-char, generated with 2-step verification on) or OAuth2 — plain password auth is blocked. Sending quota is **recipient-based on a rolling 24-hour window**: roughly **500/day on a free Gmail account (and as low as ~100/day over raw SMTP), 2,000/day on Google Workspace**. Gmail also enforces hidden behavioral throttles and is explicitly not built for high-volume transactional mail, so a Workspace account is recommended even at launch, and migration to a real ESP is the expected path as volume grows. Configure SPF (`include:_spf.google.com`) and DKIM to protect deliverability.
- **Triggers (MVP):**
  - **Overdue return** — **daily digest to the responsible person only**, listing all of their overdue assets. One email per custodian per day. Never sent to managers/admins.
  - **Custody assigned/reassigned** — new responsible person is emailed that they're now accountable for an asset. In the **bulk** path (`POST /assets/bulk/assign`, §6.3) this collapses to a single **custody digest** listing every asset assigned in the batch — never N emails — so a 500-item reassignment doesn't blow the Gmail quota.
  - **Transfer** — home-venue manager(s) emailed when their asset moves to another venue (one outbox entry per recipient). The **bulk transfer** path enqueues one **transfer digest** per home-venue manager instead of a per-asset fan-out.
  - **Repair status change** — reporter and current custodian (deduplicated) emailed when a repair **completes** or is marked **unrepairable**. No email for `in_progress` transitions.
- **Delivery model:** events are written to a notification **outbox** and sent asynchronously by a worker (keeps API requests fast); failed sends are retried with backoff up to a per-record cap before being flipped to `failed`. The low daily volume keeps it comfortably inside Gmail's quota. Each email links back to the **`/scan/:qrToken` page**; recipients are account holders (custodians/reporters), so the link passes through the frontend's login redirect and, once authenticated, they are authorized for the asset the email is about.
- **Outbox doc carries the rendered email:** `subject`, `body`, and `recipientEmail` are snapshotted at enqueue time so the worker can send without DB lookups, and the audit trail records exactly what went out. Template changes don't rewrite history. Each outbox record also stores `recipientUserId`, `assetId`, `attempts`, `status`, `sentAt`, and last `error`.
- **Bulk import suppression:** notification triggers are suppressed during `/imports/purchase-orders/commit` (§6.12) so a 1,000-row file doesn't blow past Gmail's daily quota.
- A scheduled job (cron) runs the once-daily overdue scan and enqueues the digests. Cron expression is env-configurable (`OVERDUE_CRON`, default `0 9 * * *`).

### 6.12 Bulk PO import (historical migration)
- **Goal.** A validate-first/commit-second admin pipeline that ingests POs that exist *outside* the system (spreadsheets, legacy data) and funnels them into the **same** asset-generation service the `/purchase-orders/:id/receive` endpoint uses (§6.7). No duplication of asset-creation logic.
- **Pipeline.** `POST /imports/purchase-orders/validate` (multipart CSV/XLSX → dry-run report, writes nothing); `POST /imports/purchase-orders/commit` (creates POs + assets); `GET /imports/:id` (status + counts); `GET /imports/:id/report` (downloadable result CSV with new asset tags); `GET /imports/purchase-orders/template` (downloadable template).
- **Template shape.** One row = one PO line item, denormalised (PO-level fields repeated per row, keyed on `poNumber`). Optional per-asset override columns: `assetTag`, `status`, `condition`, `currentVenueCode`, `responsibleUserEmail`, `serialNumber`, `purchaseDate`, `expectedReturnDate`, `notes`, `spec:<key>`, and **`departmentCode`** (resolved against the destination venue's departments — must belong to the asset's home venue, §6.14). Constraint: per-asset overrides require `quantity=1` (mixing N>1 with overrides is rejected).
- **Reference resolution.** Categories by slug (falling back to name); venues by code; **users by email — never auto-create**, unknown custodians reject the row; departments by (venue, code). `responsibleUserEmail` on the asset overrides the PO's owner per asset; `departmentCode` sets the asset's initial department.
- **Notification suppression.** All notifications (per-asset custody-assigned emails, etc.) are **suppressed** during import via a `SuppressNotifications` flag threaded through the receive service. A 1000-row import would blow past Gmail's daily quota otherwise (§6.11). The flag is wired and asserted in tests even though no notification currently fires from the receive path — it's a forward-protection guarantee.
- **Transaction granularity.** One Mongo transaction **per PO**, not one giant transaction (Mongo's 16MB document and lock-time limits). The job is **resumable**: a failed PO is recorded as a row-level error and the run continues with the rest. The job ends `completed` (or `failed` if zero POs landed). `importJobId` is stamped on every created PO and asset for traceability and rollback.
- **Audit trail.** Each import-created asset gets a `custody_change` Movement with `reason: "import"` (reuses the existing enum; no new MovementType).
- **Conflict policy.** Default = error the row when `poNumber` already exists. Opt-in `onConflict: skipExisting` to skip duplicates instead. Never silently update existing POs.
- **Partial-success.** `importValidOnly=true` commits the valid rows and skips errored ones rather than rejecting the whole upload. Default `false`.

### 6.13 Self-service (`/me/*`)
- Endpoints under `/me/*` operate on **the caller's own record** (identity taken from the JWT). No `:id` path parameter — so a user can never read or mutate someone else's data through these routes.
- **`GET|PUT /me/notification-preferences`** — read or toggle `notifyByEmail` for the authenticated user. PUT is tight: never accepts any other field.
- **`POST /me/password`** — change the caller's own password. Requires the current password to re-authenticate the change; new password must be 8–72 chars (bcrypt limit) and differ from the current. On success returns 204; existing access/refresh tokens stay valid until they expire (no global session revocation in v1 — see §15 follow-ups).

### 6.14 Departments (venue-scoped grouping)
- **What it is.** A **Department** is an organisational sub-unit *inside* a single venue — e.g., "IT" or "Housekeeping" at Venue A. Every department belongs to exactly one venue; there are no cross-venue departments.
- **Why it exists.** Some venues need a finer accountability cut than "venue + custodian" — a laptop can be at the Jakarta venue but owned by the IT department there. Departments give that cut without inventing a second venue hierarchy.
- **Model.** `{ venueId, name, code, description, isActive }`; unique by `(venueId, code)`. Code is short and human-picked (e.g. `IT`, `HK`).
- **Endpoints (nested under venue):**
  - `GET|POST /venues/:venueId/departments` — list / create. Read is scope-filtered (managers/staff see only their venues); create is **admin-only**.
  - `GET|PUT|DELETE /venues/:venueId/departments/:id` — read / update / delete. Delete is **soft-blocked** while any asset still references the department.
- **Asset integration.** Assets carry an optional `departmentId`. **Invariant:** the department must belong to the asset's `homeVenueId`. The invariant is enforced on asset create/update, on `POST /assets/:id/assign`, on PO `/receive`, and on bulk-import commit (§6.12). Transferring an asset to another venue does **not** clear `departmentId` (department tracks *ownership*, not physical location), but re-homing an asset requires the department to be cleared or reassigned first.
- **Where it surfaces:**
  - **Scan view** — the authenticated scan view shows the department name when set (§6.4).
  - **Bulk import** — `departmentCode` column, resolved against the destination venue's departments (§6.12).
  - **Reports** — `GET /reports/by-department` returns counts grouped by department with a per-venue breakdown (§6.9).
- **RBAC.** Reads follow the same venue scope as the parent venue (managers/staff scoped to their assigned venues). Writes (create/update/delete) are admin-only.

---

## 7. User stories (selected)

- *As a staff member,* I scan a chair's QR and instantly see it belongs to Venue A but is currently at Venue B, so I know it's on loan.
- *As a venue manager,* I transfer 10 chairs to another venue for an event and set an expected return date, so I can chase them later.
- *As a venue manager,* I report a broken laptop and send it to repair in two taps from its QR page.
- *As an admin,* I receive a PO of 20 laptops and the system creates 20 tracked assets, all assigned to the responsible person named on the PO.
- *As a custodian,* I open "my assets" and see everything I'm accountable for and where each one is.
- *As an admin,* I print QR labels for a whole PO batch at once before they're deployed.
- *As an admin,* I open any asset and see a complete timeline of every move, repair, and ownership change.

---

## 8. Data model (MongoDB collections)

> Sketches — types abbreviated. `_id` is `ObjectId`. Timestamps on every doc.

**venues**
```jsonc
{ _id, name, code, address, city, type, isActive, createdAt, updatedAt }
```

**categories**
```jsonc
{
  _id, name, slug, description,
  customFields: [ { key, label, type, required } ],  // e.g. serialNumber, cpu, ram
  isActive, createdAt, updatedAt
}
```

**departments**  *(venue-scoped grouping — see §6.14)*
```jsonc
{
  _id, venueId, name, code, description,
  isActive, createdAt, updatedAt
}
```

**assets**
```jsonc
{
  _id,
  assetTag,            // "LAP-0001", unique
  qrToken,            // random unique token used in the QR URL
  name,
  categoryId,
  homeVenueId,
  currentVenueId,
  departmentId,       // (optional) venue-scoped grouping — must belong to homeVenueId (§6.14)
  status,             // available | in_use | in_repair | retired | lost
  condition,          // new | good | fair | poor
  responsibleUserId,  // current custodian (reassignable per-asset)
  purchaseOrderId,    // origin PO (nullable for manually added assets)
  importJobId,        // (optional) bulk-import provenance stamp — see §6.12
  purchaseDate,
  serialNumber,
  specs,              // map of category custom fields
  photos: [ url ],
  expectedReturnDate, // set when temporarily away
  isOverdue,          // derived/flagged by the daily scan
  notes, isActive, createdAt, updatedAt
}
```

**purchase_orders**
```jsonc
{
  _id, poNumber,
  supplier: { name, contact },
  responsibleUserId,   // initial custodian for the batch
  orderDate, receivedDate,
  status,             // draft | ordered | received | cancelled
  lineItems: [ { categoryId, name, quantity } ],
  attachments: [ url ],
  importJobId,        // (optional) bulk-import provenance stamp — see §6.12
  notes, createdBy, createdAt, updatedAt
}
```

**movements**  *(the audit trail for an asset)*
```jsonc
{
  _id, assetId,
  type,               // transfer | status_change | custody_change | repair_in | repair_out
  fromVenueId, toVenueId,
  fromStatus, toStatus,
  fromUserId, toUserId,
  reason, notes, expectedReturnDate,
  attachmentIds,      // (optional) ids into attachments — set by the triggering action (v0.7)
  performedBy, performedAt
}
```
> `attachments` on the `GET /assets/:id/history` response is an **output-only** field — server-enriched per movement from `attachmentIds` (id, filename, contentType, size) and never persisted on the movement doc itself.

**attachments**  *(uploaded files, linked to assets/movements by an action — v0.7)*
```jsonc
{
  _id, filename, contentType, size,
  storageKey,         // internal storage handle — never returned to API callers
  uploadedBy,
  linked,             // false until an action links it to at least one asset; unlinked docs 404 on download and are swept after 24h
  assetIds,           // (optional) assets this attachment is linked to
  movementIds,        // (optional) movements this attachment is linked to
  createdAt,
  linkedAt            // (optional) set when linked=true
}
```

**repairs**
```jsonc
{
  _id, assetId, issue, reportedBy, reportedAt,
  status,             // open | in_progress | completed | unrepairable
  vendor,
  sentAt, returnedAt, resolution, notes,
  createdAt, updatedAt
}
```

**users**
```jsonc
{
  _id, name, email, passwordHash, role,   // admin | venue_manager | staff
  position,                               // job title, e.g. "IT Staff", "Event Manager"
  venueIds: [ ObjectId ],                 // scope for managers/staff
  notifyByEmail,                          // notification preference
  phone, isActive, createdAt, updatedAt
}
```

**notifications**  *(outbox/log of emails sent)*
```jsonc
{
  _id, type,          // overdue | custody_change | transfer | repair_update
  recipientUserId,
  recipientEmail,     // snapshotted at enqueue so the worker doesn't re-resolve
  subject, body,      // pre-rendered text — audit trail of what was sent
  assetId,
  channel,            // email
  status,             // queued | sent | failed
  attempts, sentAt, error,
  createdAt
}
```

**import_jobs**  *(bulk PO import jobs — see §6.12)*
```jsonc
{
  _id, filename, uploadedBy,
  status,             // validating | preview_ready | importing | completed | failed | rolled_back
  options: { onConflict, importValidOnly },
  counts: { posTotal, posCreated, assetsCreated, rowsSkipped, rowsErrored },
  errors: [ { row, field, message } ],
  parsedRows,         // internal — never serialized over the API
  createdAt, completedAt
}
```

**audit_logs**
```jsonc
{ _id, entityType, entityId, action, changes, performedBy, performedAt, ip }
```

### Suggested indexes
- `assets`: unique on `assetTag`, unique on `qrToken`; indexes on `homeVenueId`, `currentVenueId`, `departmentId`, `status`, `responsibleUserId`, `categoryId`, `purchaseOrderId`, `importJobId`, `isOverdue`; text index on `name` + `serialNumber` + `assetTag`.
- `purchase_orders`: unique on `poNumber`; indexes on `responsibleUserId`, `status`, `importJobId`.
- `import_jobs`: indexes on `uploadedBy` + `createdAt`, and on `status`.
- `movements`: compound index on `assetId` + `performedAt`; index on `type`.
- `repairs`: indexes on `assetId`, `status`.
- `notifications`: compound index on `status` + `createdAt`; index on `recipientUserId`.
- `users`: unique on `email`; indexes on `role` and `venueIds`.
- `venues`: unique on `code`.
- `categories`: unique on `slug`.
- `departments`: unique compound on `(venueId, code)`; index on `venueId`.
- `attachments`: compound index on `linked` + `createdAt` (orphan sweep); index on `uploadedBy`; index on `assetIds`.

---

## 9. API surface (REST, Fiber)

Base path `/api/v1`. **JWT bearer auth on all endpoints** — including `GET /scan/:qrToken`, which additionally enforces per-asset authorization (admin, venue scope, or current custodian) and returns a full read-only asset view.

**Auth**
```
POST   /auth/login
POST   /auth/refresh
GET    /auth/me
```

**Venues / Categories / Departments / Users**
```
GET|POST            /venues
GET|PUT|DELETE       /venues/:id
GET|POST            /venues/:venueId/departments             // §6.14 (nested under venue)
GET|PUT|DELETE       /venues/:venueId/departments/:id        // delete soft-blocked while any asset references it
GET|POST            /categories
GET|PUT|DELETE       /categories/:id
GET|POST            /users
GET|PUT|DELETE       /users/:id
```

**Assets**
```
GET    /assets                 // filters: venue, currentVenue, category, status, responsible, away, q, page
POST   /assets
GET|PUT|DELETE /assets/:id
GET    /assets/:id/history     // movement timeline; each movement carries attachments[] metadata (v0.7)
POST   /assets/:id/transfer    // { toVenueId, expectedReturnDate?, notes, attachmentIds[]? }
POST   /assets/:id/status      // { status, reason, attachmentIds[]? }
POST   /assets/:id/assign      // { responsibleUserId, attachmentIds[]? }  -> custody change
POST   /assets/:id/condition   // { condition, notes?, attachmentIds[]? }
GET    /assets/:id/qr          // PNG/PDF label

// Bulk endpoints — max 500 assets per call (§6.3)
POST   /assets/bulk/transfer   // { assetIds[], toVenueId, expectedReturnDate?, notes, attachmentIds[]? } — one transaction; dedup transfer digest per home venue
POST   /assets/bulk/status     // { assetIds[], status, reason, attachmentIds[]? } — one transaction; validates state machine per item
POST   /assets/bulk/assign     // { assetIds[], responsibleUserId, notes?, attachmentIds[]? } — one transaction; already-assigned rows counted as skippedNoOp; one custody digest to the new custodian
POST   /assets/condition/bulk  // { assetIds[], condition, notes?, attachmentIds[]? } — best-effort per item; silently skips no-op / not-found / forbidden
POST   /assets/qr/bulk         // { assetIds[] } -> combined PDF of labels

GET    /scan/:qrToken          // authed + per-asset RBAC — resolve a scanned code to full asset view; exposes no attachment data
```
> **v0.7:** all eight asset-action endpoints above (single + bulk transfer/status/assign/condition) accept an optional `attachmentIds[]` (max 5), validated upfront against `POST /attachments` uploads and linked inside the same transaction as the movement write.

**Attachments (v0.7)**
```
POST   /attachments             // multipart upload; sniffs content type, enforces 10 MB max -> unlinked attachment id
GET    /attachments/:id/download // streams file bytes; RBAC: admin, or venue-scoped manager/staff (home or current), or current custodian; unlinked docs 404 for everyone
```

**Purchase orders**
```
GET|POST            /purchase-orders
GET|PUT              /purchase-orders/:id
POST                /purchase-orders/:id/receive   // body: { venueId } — generates assets (transactional)
```

**Bulk PO import (admin only — see §6.12)**
```
GET    /imports/purchase-orders/template      // CSV template
POST   /imports/purchase-orders/validate      // multipart upload → preview
POST   /imports/purchase-orders/commit        // body: { importJobId, options? }
GET    /imports/:id                           // job status + counts + errors
GET    /imports/:id/report                    // downloadable CSV result
```

**Repairs**
```
GET|POST            /repairs
GET|PUT              /repairs/:id
```

**Reports / Dashboard**
```
GET    /dashboard/summary
GET    /reports/inventory-by-venue
GET    /reports/assets-away
GET    /reports/assets-overdue
GET    /reports/in-repair
GET    /reports/by-responsible
GET    /reports/by-department        // §6.14 — counts grouped by department with per-venue breakdown
```

**Notifications / Account (self-service — see §6.13)**
```
GET|PUT             /me/notification-preferences   // toggle email notifications for caller
POST                /me/password                   // body: { current, next } — change own password
GET                 /notifications                 // sent/queued log (admin)
```

---

## 10. System architecture

- **API contract is OpenAPI-first.** [`openapi.yaml`](../openapi.yaml) (OpenAPI 3.0.3) at the repo root is the single source of truth for every endpoint and every data shape. Go model structs are generated from it via `oapi-codegen` (`make generate` → `internal/models/models.gen.go`); the generated structs carry both `json` and `bson` tags so a single Go type serves both API I/O and Mongo persistence. Generated files are never hand-edited — to change a model, edit `openapi.yaml` and regenerate.
- **Frontend** — Next.js (App Router). Responsive, mobile-first. A dedicated `/scan/[qrToken]` route and a camera-based scanner page using a browser QR library. Server components for lists, client components for the scanner and quick-action forms. Consider PWA for fast phone access.
- **Backend** — Go 1.25 + Fiber v2 REST API. Stateless (JWT), so it scales horizontally. Generates QR images/labels server-side. Enforces the asset state machine and RBAC in a service layer.
- **Database** — MongoDB via the official **driver v2** (`go.mongodb.org/mongo-driver/v2`). Use multi-document **transactions** for PO receiving (create N assets + flip PO status atomically); the bulk import (§6.12) uses one transaction **per PO** during commit. Movements/audit are append-only. Transactions require a replica set or sharded cluster — standalone `mongod` will reject `StartSession`.
- **Scheduler (cron)** — a scheduled worker runs the **daily overdue scan** (flag assets past `expectedReturnDate`) and enqueues digest emails. Can run as a goroutine ticker in the API process for MVP, or a separate worker later.
- **Email worker** — consumes the notification outbox and sends via **Gmail SMTP** (App Password / OAuth2), with retries. Mail layer is interface-based so a dedicated ESP can replace Gmail later. Keeps email sending off the request path.
- **File storage** — asset photos, QR PDFs, PO invoices in S3-compatible object storage (or local disk for MVP).
- **Auth** — JWT access + refresh; passwords hashed with bcrypt/argon2.

```
                         ┌──────────────────────┐
[ Next.js (web+scanner) ]─HTTPS─▶[ Fiber API ]──┼──▶ [ MongoDB ]
[ Authed /scan page ]────────────▶              │
                                  [ Cron: overdue scan ]──▶ outbox
                                  [ Email worker ]──▶ [ Gmail SMTP (App Password) ]
                                  [ Object storage: photos, QR PDFs, invoices ]
```

---

## 11. Non-functional requirements

- **Mobile usability** — scanning and quick status/transfer updates must be smooth on a phone in the field.
- **Security** — RBAC enforced server-side; QR tokens random and non-enumerable; HTTPS only; rate-limit the scan endpoint (now authenticated; rate limiting remains a follow-up — see §15).
- **Auditability** — every state-changing action is logged with actor + timestamp; movements are immutable.
- **Performance** — all list endpoints paginated and index-backed; dashboard counts via aggregation, cached if needed.
- **Data integrity** — PO-receive is transactional; state transitions validated against the state machine; can't delete venues/categories still in use.
- **Reliability** — stateless API behind a load balancer; daily DB backups.
- **Email deliverability** — Gmail SMTP via App Password with SPF/DKIM configured; keep within Gmail's recipient-based daily quota (use a Workspace account, not free Gmail, for headroom). Outbox + retries absorb transient failures; design assumes migration to a dedicated ESP if volume ever grows.

---

## 12. Scope & phasing

**Phase 1 — MVP**
Auth + users/roles · venues · **venue-scoped departments (§6.14)** · categories · assets CRUD · QR generation + **authenticated scan-to-view** · status (available / in_use / in_repair / retired / lost) · venue transfers with movement history · **expected-return tracking + overdue flagging** · **per-asset custody assignment & reassignment** · **bulk asset ops** (transfer / status / condition / QR — §6.3) · **email notifications** (overdue, custody change, transfer, repair update) · daily overdue cron · dashboard with overdue/away/in-repair counts.

**Phase 2 — Purchase orders & batch tooling**
Purchase orders · receive-PO → transactional asset generation with inherited custodian · **bulk PO import (CSV/XLSX) → validate then commit; reuses the receive service; notifications suppressed; per-PO transactions; importJobId stamping for traceability (§6.12)** · bulk QR label printing (per-PO batch) · repair ticket workflow · reports (by venue / away / overdue / in repair / by responsible / by department / by PO).

**Phase 3 — Operational polish**
Receiving-venue transfer approvals · repair SLA tracking · CSV/Excel report exports · PWA offline scan · richer notification preferences (per-event toggles, digests).

**Phase 4 — Optional / later**
Additional channels (WhatsApp/in-app) · maintenance scheduling · barcode support alongside QR.

> Note: financials/depreciation are intentionally **out of scope** at every phase per the locked decisions.

---

## 13. Success metrics
- % of assets with an up-to-date location (scanned/updated in last N days).
- Time to locate an asset (target: < 30s via scan or search).
- % of assets linked to a PO and a responsible person.
- Overdue returns resolved within X days of the first alert.
- Reduction in "lost/unaccounted" assets over time.
- Repair turnaround time tracked and trending down.

---

## 14. Resolved decisions

### v0.3
1. **Tenancy** — single organization; no `orgId` / multi-tenancy.
2. **Financials** — out of scope entirely (no value, no depreciation). Location + custody only.
3. **Overdue alerts** — in MVP; **daily digest only** (no instant email), sent to the **responsible person only**.
4. **Custody** — per-asset reassignment from day one; PO sets the initial custodian.
5. **Public scan** — unauthenticated scan shows full asset details with the responsible person's **name/role visible but contact details masked**; **resolves even for lost/retired assets**; actions still require login. _(Superseded by v0.8: the scan endpoint is now authenticated with per-asset authorization, and contact details are unmasked for authorized viewers.)_
6. **Notifications** — email for v1 via **Gmail SMTP** (App Password, SPF/DKIM), outbox + worker, with a documented path to a dedicated ESP later.

### v0.4 (additions)
7. **API contract** — `openapi.yaml` (OpenAPI 3.0.3) is the source of truth; Go model structs are code-generated and serve both JSON and BSON (one struct, both tags). Hand-written models are removed.
8. **MongoDB driver** — v2 (`go.mongodb.org/mongo-driver/v2`); v1 is deprecated and not used.
9. **PO `/receive` body** — carries the destination `venueId`; it becomes both `homeVenueId` and `currentVenueId` on every generated asset.
10. **Notification outbox** — stores **rendered `subject` / `body` / `recipientEmail`** at enqueue time. The worker sends without DB lookups, and the doc is an audit record of what was actually sent.
11. **Bulk PO import (§6.12)** — admin-only, **validate-then-commit** flow; **one Mongo transaction per PO** on commit; notifications **suppressed** during import; per-asset overrides supported; every created PO and asset is stamped with `importJobId` for traceability.
12. **Self-service `/me/*`** — JWT-identified endpoints for password change and notification-preferences toggle; never accept an `:id` parameter.
13. **Soft-block delete** — venues, categories, and users refuse to delete while any asset/PO references them (consistent with §6.1 and applied uniformly).

### v0.5 (additions)
14. **Departments (§6.14)** — venue-scoped, first-class entity with nested CRUD under `/venues/:venueId/departments`. Optional on an asset, but when set the department **must** belong to the asset's `homeVenueId` (invariant enforced on create/update, assign, PO receive, and bulk-import commit). Delete is soft-blocked while any asset references the department.
15. **Bulk asset endpoints** — the shipped surface is `bulk/transfer`, `bulk/status`, `condition/bulk`, and `qr/bulk`, each capped at 500 assets per call. Transfer and status run as a single Mongo transaction; condition is best-effort per item; QR bulk is unchanged. **No ad-hoc bulk-create endpoint** — batch creation flows through PO receive (§6.7) or bulk PO import (§6.12).
16. **Reports by department** — `GET /reports/by-department` added, returning counts grouped by department with a per-venue breakdown.
17. **Audit log — partially wired** — `audit_logs` schema stays declared in `openapi.yaml` and §8, but the append-only source of truth in production today is `movements` + the `notifications` outbox. A general-purpose audit writer is deferred to §15.

## 15. Known follow-ups (post-v0.5)

These are flagged design gaps the team has acknowledged but not yet committed to:
- **General-purpose audit log writer** — §6.10 / §8 declare an `audit_logs` collection but no service writes to it yet. Every mutating handler already threads `performedBy`, so the missing piece is a shared writer + a decision on which mutations to persist beyond `movements`.
- **Global session revocation on password change** — currently access/refresh tokens stay valid until they expire after `POST /me/password`. Adding a `tokenVersion` claim + per-user counter is the standard fix.
- **Rate limiting** — `POST /auth/login`, `POST /me/password`, and `GET /scan/:qrToken` (§11) have no rate limit yet.
- **Multi-replica notification worker** — current worker has no inter-instance lock. Horizontal scaling needs a `findOneAndUpdate(queued → claimed)` step to avoid duplicate sends.
