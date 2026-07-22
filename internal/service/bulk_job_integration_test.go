//go:build integration

// Integration tests for the async bulk-asset job pipeline. These require a
// MongoDB REPLICA SET (transactions are used) reachable via MONGO_URI, e.g.:
//
//	MONGO_URI='mongodb://localhost:27077/?replicaSet=rs0test' \
//	    go test -tags integration ./internal/service/ -run BulkJobIT -count=1
//
// Each test runs against a freshly-created, uniquely-named database
// (imp_integration_test_<ns>) that is DROPPED on cleanup — the real `imp`
// database is never touched.
package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"log/slog"

	"imp/internal/database"
	"imp/internal/models"
	"imp/internal/notification"
	"imp/internal/repository"
	"imp/internal/storage"
)

type itEnv struct {
	t       *testing.T
	db      *mongo.Database
	client  *mongo.Client
	assets  *repository.AssetRepository
	moves   *repository.MovementRepository
	users   *repository.UserRepository
	venues  *repository.VenueRepository
	atts    *repository.AttachmentRepository
	bulk    *repository.BulkJobRepository
	outbox  *notification.Repository
	assetS  *AssetService
	attS    *AttachmentService
	bulkS   *BulkJobService
	storage storage.FileStorage
}

func setupIT(t *testing.T, cfg BulkJobConfig) *itEnv {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set; skipping replica-set integration test")
	}
	dbName := fmt.Sprintf("imp_integration_test_%d", time.Now().UnixNano())
	ctx := context.Background()
	conn, err := database.Connect(ctx, uri, dbName)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := database.EnsureIndexes(ctx, conn.DB); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.DB.Drop(context.Background())
		_ = conn.Close(context.Background())
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fsDir := t.TempDir()
	fs, err := storage.NewLocalDisk(fsDir)
	if err != nil {
		t.Fatalf("storage: %v", err)
	}

	e := &itEnv{
		t:       t,
		db:      conn.DB,
		client:  conn.Client,
		assets:  repository.NewAssetRepository(conn.DB),
		moves:   repository.NewMovementRepository(conn.DB),
		users:   repository.NewUserRepository(conn.DB),
		venues:  repository.NewVenueRepository(conn.DB),
		atts:    repository.NewAttachmentRepository(conn.DB),
		bulk:    repository.NewBulkJobRepository(conn.DB),
		outbox:  notification.NewRepository(conn.DB),
		storage: fs,
	}
	triggers := notification.NewTriggers(e.outbox, e.users, e.venues, logger, "http://frontend.test")
	e.attS = NewAttachmentService(e.atts, e.assets, fs, AttachmentConfig{MaxBytes: 1 << 20, MaxPerRequest: 5})
	e.assetS = NewAssetService(e.assets, e.moves, e.venues,
		repository.NewCategoryRepository(conn.DB), e.users,
		repository.NewDepartmentRepository(conn.DB),
		repository.NewCounterRepository(conn.DB),
		triggers, conn.Client, "http://frontend.test", nil, e.attS, 0)
	e.bulkS = NewBulkJobService(e.assetS, e.bulk, fs, cfg, logger)
	return e
}

var seq int64

func (e *itEnv) uniq(prefix string) string {
	seq++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), seq)
}

func (e *itEnv) seedVenue(active bool) bson.ObjectID {
	id := bson.NewObjectID()
	_, err := e.db.Collection("venues").InsertOne(context.Background(), models.Venue{
		ID: id, Name: e.uniq("Venue"), Code: e.uniq("V"), IsActive: active,
		Type: "office", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		e.t.Fatalf("seed venue: %v", err)
	}
	return id
}

func (e *itEnv) seedUser(role models.Role, venue bson.ObjectID, active, notify bool) bson.ObjectID {
	id := bson.NewObjectID()
	_, err := e.db.Collection("users").InsertOne(context.Background(), models.User{
		ID: id, Name: e.uniq("User"), Email: openapi_types.Email(e.uniq("u") + "@test.local"),
		Role: role, VenueIDs: []bson.ObjectID{venue}, IsActive: active, NotifyByEmail: notify,
		Position: "Staff", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		e.t.Fatalf("seed user: %v", err)
	}
	return id
}

func (e *itEnv) seedAsset(home, current bson.ObjectID, status models.AssetStatus, cond models.AssetCondition, custodian *bson.ObjectID) bson.ObjectID {
	id := bson.NewObjectID()
	_, err := e.db.Collection("assets").InsertOne(context.Background(), models.Asset{
		ID: id, AssetTag: e.uniq("TAG"), QrToken: e.uniq("qr"), Name: e.uniq("Asset"),
		CategoryID: bson.NewObjectID(), HomeVenueID: home, CurrentVenueID: current,
		Status: status, Condition: cond, ResponsibleUserID: custodian, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		e.t.Fatalf("seed asset: %v", err)
	}
	return id
}

// deleteAsset hard-deletes an asset document, simulating "deleted since
// planning" for TOCTOU tests: the row is present at enqueue time (included in
// job.AssetIDs) but gone by the time the worker's applyRow reloads it.
func (e *itEnv) deleteAsset(id bson.ObjectID) {
	e.t.Helper()
	if err := e.assets.Delete(context.Background(), id); err != nil {
		e.t.Fatalf("delete asset: %v", err)
	}
}

// adminPrincipal authorizes everything, keeping tests focused on job mechanics.
func adminPrincipal() Principal { return Principal{IsAdmin: true, UserID: bson.NewObjectID()} }

func (e *itEnv) runToCompletion(job *models.BulkJob) *repository.BulkJobDoc {
	e.t.Helper()
	ctx := context.Background()
	for i := 0; i < 500; i++ {
		claimed, err := e.bulkS.ClaimAndRunOnce(ctx, "test-worker")
		if err != nil {
			e.t.Fatalf("run: %v", err)
		}
		if !claimed {
			break
		}
	}
	doc, err := e.bulk.FindByID(ctx, job.ID)
	if err != nil {
		e.t.Fatalf("reload job: %v", err)
	}
	return doc
}

func (e *itEnv) movementCount(assetID bson.ObjectID) int {
	n, err := e.db.Collection("movements").CountDocuments(context.Background(), bson.M{"assetId": assetID})
	if err != nil {
		e.t.Fatalf("count movements: %v", err)
	}
	return int(n)
}

func (e *itEnv) outboxCount(filter bson.M) int {
	n, err := e.db.Collection("notifications").CountDocuments(context.Background(), filter)
	if err != nil {
		e.t.Fatalf("count outbox: %v", err)
	}
	return int(n)
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------

func TestBulkJobIT_TransferEndToEnd(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)
	dest := e.seedVenue(true)
	// A home-venue manager who should receive exactly one transfer digest.
	e.seedUser(models.VenueManager, home, true, true)

	var ids []bson.ObjectID
	for i := 0; i < 5; i++ {
		ids = append(ids, e.seedAsset(home, home, models.InUse, models.Good, nil))
	}

	job, err := e.bulkS.EnqueueTransfer(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkTransferRequest{AssetIDs: ids, ToVenueID: dest})
	if err != nil || job == nil {
		t.Fatalf("enqueue: job=%v err=%v", job, err)
	}
	if job.Status != models.BulkJobStatusQueued {
		t.Fatalf("status=%s want queued", job.Status)
	}

	doc := e.runToCompletion(job)
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("terminal status=%s want completed (errors=%v)", doc.Status, doc.Errors)
	}
	if doc.Counts.Succeeded != 5 || doc.Counts.Failed != 0 {
		t.Fatalf("counts=%+v want 5 succeeded", doc.Counts)
	}
	// 5 assets over batchSize 2 → 3 batches.
	if doc.Progress.BatchesDone != 3 {
		t.Fatalf("batchesDone=%d want 3", doc.Progress.BatchesDone)
	}
	for _, id := range ids {
		if e.movementCount(id) != 1 {
			t.Fatalf("asset %s has %d movements, want exactly 1", id.Hex(), e.movementCount(id))
		}
		a, _ := e.assets.FindByID(context.Background(), id)
		if a.CurrentVenueID != dest {
			t.Fatalf("asset not moved to dest")
		}
	}
	// Exactly one transfer digest to the one home-venue manager.
	if got := e.outboxCount(bson.M{"type": string(models.Transfer)}); got != 1 {
		t.Fatalf("transfer outbox rows=%d want exactly 1 (multi-batch digest-once)", got)
	}
}

// TestBulkJobIT_TransferSameVenueAndDeletedAreSkips covers the TOCTOU paths in
// applyRow: planBulkTransfer already pre-counts same-venue/not-found rows as
// skips at enqueue time, so to exercise the worker's own skip handling both
// rows must be valid AT ENQUEUE and only become a no-op / disappear before the
// worker executes them — exactly like the status TOCTOU test above.
func TestBulkJobIT_TransferSameVenueAndDeletedAreSkips(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)
	dest := e.seedVenue(true)

	moved := e.seedAsset(home, home, models.InUse, models.Good, nil)   // valid transfer at enqueue
	deleted := e.seedAsset(home, home, models.InUse, models.Good, nil) // valid at enqueue, gone at execution

	job, err := e.bulkS.EnqueueTransfer(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkTransferRequest{AssetIDs: []bson.ObjectID{moved, deleted}, ToVenueID: dest})
	if err != nil || job == nil {
		t.Fatalf("enqueue: job=%v err=%v", job, err)
	}
	if job.Counts.Skipped != 0 {
		t.Fatalf("skipped at enqueue=%d want 0 (both destined for a different venue and present)", job.Counts.Skipped)
	}

	// TOCTOU, after enqueue but before the worker runs: `moved` is mutated to
	// already be at dest (same-venue no-op at execution time); `deleted` is
	// hard-deleted (not-found at execution time).
	if _, err := e.assets.Update(context.Background(), moved, bson.M{"currentVenueId": dest}); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	e.deleteAsset(deleted)

	doc := e.runToCompletion(job)
	if doc.Counts.Failed != 0 {
		t.Fatalf("failed=%d want 0 (both should be skips)", doc.Counts.Failed)
	}
	if doc.Counts.Skipped != 2 {
		t.Fatalf("skipped=%d want 2", doc.Counts.Skipped)
	}
	if e.movementCount(moved) != 0 {
		t.Fatalf("same-venue no-op wrote a movement (must not)")
	}
	// No notifications: nothing actually moved.
	if e.outboxCount(bson.M{"type": string(models.Transfer)}) != 0 {
		t.Fatal("expected zero transfer digests (nothing moved)")
	}
}

func TestBulkJobIT_AssignSkipsAndDigestOnce(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)
	custodian := e.seedUser(models.Staff, home, true, true)

	// 6 assets: 2 already assigned to the target (no-op skips), 4 to reassign.
	var ids []bson.ObjectID
	for i := 0; i < 2; i++ {
		ids = append(ids, e.seedAsset(home, home, models.InUse, models.Good, &custodian))
	}
	var toAssign []bson.ObjectID
	for i := 0; i < 4; i++ {
		id := e.seedAsset(home, home, models.InUse, models.Good, nil)
		ids = append(ids, id)
		toAssign = append(toAssign, id)
	}

	job, err := e.bulkS.EnqueueAssign(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkAssignRequest{AssetIDs: ids, ResponsibleUserID: custodian})
	if err != nil || job == nil {
		t.Fatalf("enqueue: job=%v err=%v", job, err)
	}

	doc := e.runToCompletion(job)
	if doc.Counts.Succeeded != 4 {
		t.Fatalf("succeeded=%d want 4", doc.Counts.Succeeded)
	}
	if doc.Counts.Skipped != 2 {
		t.Fatalf("skipped=%d want 2 (pre-counted no-ops)", doc.Counts.Skipped)
	}
	// No movement for the already-assigned no-op rows.
	for i := 0; i < 2; i++ {
		if e.movementCount(ids[i]) != 0 {
			t.Fatalf("no-op asset wrote a movement (must not)")
		}
	}
	for _, id := range toAssign {
		if e.movementCount(id) != 1 {
			t.Fatalf("reassigned asset has %d movements, want 1", e.movementCount(id))
		}
	}
	// Exactly one custody digest across all batches.
	if got := e.outboxCount(bson.M{"type": string(models.CustodyChange)}); got != 1 {
		t.Fatalf("custody outbox rows=%d want exactly 1", got)
	}
}

// TestBulkJobIT_AssignDeletedAssetIsSkip covers the TOCTOU path in applyRow's
// BulkJobTypeAssign case: the asset is present and assignable at enqueue but
// hard-deleted before the worker executes the row, which must count as a
// skip, not a failure — mirroring
// TestBulkJobIT_TransferSameVenueAndDeletedAreSkips.
func TestBulkJobIT_AssignDeletedAssetIsSkip(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)
	custodian := e.seedUser(models.Staff, home, true, true)
	id := e.seedAsset(home, home, models.InUse, models.Good, nil) // valid at enqueue, gone at execution

	job, err := e.bulkS.EnqueueAssign(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkAssignRequest{AssetIDs: []bson.ObjectID{id}, ResponsibleUserID: custodian})
	if err != nil || job == nil {
		t.Fatalf("enqueue: job=%v err=%v", job, err)
	}
	if job.Counts.Skipped != 0 {
		t.Fatalf("skipped at enqueue=%d want 0 (asset present and assignable)", job.Counts.Skipped)
	}

	// TOCTOU, after enqueue but before the worker runs: hard-delete the asset.
	e.deleteAsset(id)

	doc := e.runToCompletion(job)
	if doc.Counts.Failed != 0 {
		t.Fatalf("failed=%d want 0 (deleted-at-execution asset must be a skip, not an error)", doc.Counts.Failed)
	}
	if doc.Counts.Skipped != 1 {
		t.Fatalf("skipped=%d want 1", doc.Counts.Skipped)
	}
}

func TestBulkJobIT_AssignUnknownUserIs400AtEnqueue(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2})
	home := e.seedVenue(true)
	id := e.seedAsset(home, home, models.InUse, models.Good, nil)
	_, err := e.bulkS.EnqueueAssign(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkAssignRequest{AssetIDs: []bson.ObjectID{id}, ResponsibleUserID: bson.NewObjectID()})
	if err == nil {
		t.Fatal("expected 400 for unknown responsibleUserId")
	}
	// Nothing enqueued.
	n, _ := e.db.Collection("bulk_jobs").CountDocuments(context.Background(), bson.M{})
	if n != 0 {
		t.Fatalf("job was enqueued despite unknown user (%d jobs)", n)
	}
}

// TestBulkJobIT_StatusInvalidTransitionBecomesRowError covers both an
// invalid-at-enqueue transition and a TOCTOU one (valid at enqueue,
// invalidated before execution): planBulkStatus does not reject either at
// enqueue time (unlike a no-op, an illegal-but-non-no-op transition is not a
// skip either) — both flow through to applyRow and surface as row errors,
// exactly like an illegal condition change does for the reference endpoint.
func TestBulkJobIT_StatusInvalidTransitionBecomesRowError(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)

	retired := e.seedAsset(home, home, models.Retired, models.Good, nil) // retired→available not allowed
	a := e.seedAsset(home, home, models.InUse, models.Good, nil)         // in_use→available valid
	b := e.seedAsset(home, home, models.InUse, models.Good, nil)

	job, err := e.bulkS.EnqueueStatus(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkStatusRequest{AssetIDs: []bson.ObjectID{retired, a, b}, Status: models.Available})
	if err != nil || job == nil {
		t.Fatalf("enqueue: job=%v err=%v", job, err)
	}
	// TOCTOU: mutate `a` to Retired after enqueue so its transition is now
	// invalid too — same not-a-skip, row-error-at-execution outcome as `retired`.
	if _, err := e.assets.Update(context.Background(), a, bson.M{"status": models.Retired}); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	doc := e.runToCompletion(job)
	if doc.Status != models.BulkJobStatusCompletedWithErrors {
		t.Fatalf("status=%s want completed_with_errors", doc.Status)
	}
	if doc.Counts.Succeeded != 1 || doc.Counts.Failed != 2 {
		t.Fatalf("counts=%+v want 1 succ / 2 fail", doc.Counts)
	}
	errIDs := map[bson.ObjectID]bool{}
	for _, e := range doc.Errors {
		errIDs[e.AssetID] = true
	}
	if !errIDs[retired] || !errIDs[a] {
		t.Fatalf("expected row errors for retired and a, got %+v", doc.Errors)
	}
	// No notifications for status jobs.
	if e.outboxCount(bson.M{}) != 0 {
		t.Fatal("status job must enqueue zero notifications")
	}
}

func TestBulkJobIT_ConditionBestEffortNoNotifications(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)
	change := e.seedAsset(home, home, models.InUse, models.Good, nil)
	same := e.seedAsset(home, home, models.InUse, models.Poor, nil) // already Poor → unchanged skip

	job, err := e.bulkS.EnqueueCondition(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkConditionUpdate{AssetIDs: []bson.ObjectID{change, same}, Condition: models.Poor})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	doc := e.runToCompletion(job)
	if doc.Counts.Succeeded != 1 {
		t.Fatalf("succeeded=%d want 1", doc.Counts.Succeeded)
	}
	if doc.Counts.Skipped != 1 {
		t.Fatalf("skipped=%d want 1 (unchanged pre-counted at enqueue)", doc.Counts.Skipped)
	}
	if e.outboxCount(bson.M{}) != 0 {
		t.Fatal("condition job must enqueue zero notifications")
	}
}

func TestBulkJobIT_CursorResumeNoDoubleApply(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)
	dest := e.seedVenue(true)
	var ids []bson.ObjectID
	for i := 0; i < 4; i++ {
		ids = append(ids, e.seedAsset(home, home, models.InUse, models.Good, nil))
	}
	job, err := e.bulkS.EnqueueTransfer(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkTransferRequest{AssetIDs: ids, ToVenueID: dest})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	ctx := context.Background()
	// Worker A claims and processes ONLY the first batch, then "crashes".
	claimed, err := e.bulk.Claim(ctx, "A", time.Now().UTC(), time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim A: %v", err)
	}
	batch0 := claimed.AssetIDs[0:2]
	if done, err := e.bulkS.processBatchWithRetry(ctx, claimed, "A", 0, batch0); err != nil || !done {
		t.Fatalf("process batch0: done=%v err=%v", done, err)
	}
	// Simulate crash: force the lease to be expired so B can reclaim.
	if _, err := e.db.Collection("bulk_jobs").UpdateByID(ctx, job.ID,
		bson.M{"$set": bson.M{"leaseExpiresAt": time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	// Worker B reclaims and finishes.
	doc := e.runToCompletion(job)
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("status=%s want completed", doc.Status)
	}
	if doc.Counts.Succeeded != 4 {
		t.Fatalf("succeeded=%d want 4 (resume must not skip or repeat)", doc.Counts.Succeeded)
	}
	for _, id := range ids {
		if e.movementCount(id) != 1 {
			t.Fatalf("asset %s has %d movements, want exactly 1 (no double-apply)", id.Hex(), e.movementCount(id))
		}
	}
}

func TestBulkJobIT_TwoWorkersOneJob(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 1, MaxAttempts: 3, ErrorCap: 100, Lease: time.Minute})
	home := e.seedVenue(true)
	dest := e.seedVenue(true)
	var ids []bson.ObjectID
	for i := 0; i < 6; i++ {
		ids = append(ids, e.seedAsset(home, home, models.InUse, models.Good, nil))
	}
	job, err := e.bulkS.EnqueueTransfer(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkTransferRequest{AssetIDs: ids, ToVenueID: dest})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				claimed, err := e.bulkS.ClaimAndRunOnce(context.Background(), id)
				if err != nil || !claimed {
					if !claimed {
						return
					}
				}
			}
		}(fmt.Sprintf("worker-%d", w))
	}
	wg.Wait()

	doc, _ := e.bulk.FindByID(context.Background(), job.ID)
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("status=%s want completed", doc.Status)
	}
	if doc.Counts.Succeeded != 6 {
		t.Fatalf("succeeded=%d want 6", doc.Counts.Succeeded)
	}
	for _, id := range ids {
		if e.movementCount(id) != 1 {
			t.Fatalf("asset %s has %d movements, want exactly 1 under contention", id.Hex(), e.movementCount(id))
		}
	}
}

func TestBulkJobIT_LeaseExpiryReclaim(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, Lease: time.Minute})
	home := e.seedVenue(true)
	id := e.seedAsset(home, home, models.InUse, models.Good, nil)
	job, err := e.bulkS.EnqueueTransfer(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkTransferRequest{AssetIDs: []bson.ObjectID{id}, ToVenueID: e.seedVenue(true)})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	ctx := context.Background()
	// Worker A claims, then vanishes with an already-expired lease.
	a, err := e.bulk.Claim(ctx, "A", time.Now().Add(-2*time.Hour).UTC(), time.Minute)
	if err != nil || a == nil {
		t.Fatalf("claim A: %v", err)
	}
	// Worker B should reclaim it (lease expired).
	b, err := e.bulk.Claim(ctx, "B", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if b == nil || b.ID != job.ID {
		t.Fatal("worker B failed to reclaim the expired-lease job")
	}
	if b.ClaimedBy != "B" {
		t.Fatalf("claimedBy=%s want B", b.ClaimedBy)
	}
}

func TestBulkJobIT_ErrorCapTruncation(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 10, MaxAttempts: 3, ErrorCap: 3, Lease: time.Minute})
	home := e.seedVenue(true)
	dest := e.seedVenue(true)
	valid := e.seedAsset(home, home, models.InUse, models.Good, nil)
	// 5 non-existent ids → 5 not_found row errors, cap 3.
	ids := []bson.ObjectID{valid}
	for i := 0; i < 5; i++ {
		ids = append(ids, bson.NewObjectID())
	}
	job, err := e.bulkS.EnqueueTransfer(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkTransferRequest{AssetIDs: ids, ToVenueID: dest})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	doc, _ := e.bulk.FindByID(context.Background(), job.ID)
	if !doc.ErrorsTruncated {
		t.Fatal("expected errorsTruncated=true")
	}
	if len(doc.Errors) != 3 {
		t.Fatalf("retained errors=%d want 3 (capped)", len(doc.Errors))
	}
	if doc.Counts.Failed != 5 {
		t.Fatalf("counts.failed=%d want 5 (true total, uncapped)", doc.Counts.Failed)
	}
}

func TestBulkJobIT_DedupeAtEnqueue(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 10, Lease: time.Minute})
	home := e.seedVenue(true)
	dest := e.seedVenue(true)
	id := e.seedAsset(home, home, models.InUse, models.Good, nil)
	job, err := e.bulkS.EnqueueTransfer(context.Background(), adminPrincipal().UserID, adminPrincipal(),
		models.BulkTransferRequest{AssetIDs: []bson.ObjectID{id, id, id}, ToVenueID: dest})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if job.Counts.Total != 1 {
		t.Fatalf("total=%d want 1 (deduped)", job.Counts.Total)
	}
}

func TestBulkJobIT_AttachmentReservationSurvivesSweep(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, Lease: time.Minute})
	home := e.seedVenue(true)
	dest := e.seedVenue(true)
	uploader := adminPrincipal().UserID
	id := e.seedAsset(home, home, models.InUse, models.Good, nil)

	// An OLD unlinked attachment (created 48h ago) that would normally be swept.
	attID := bson.NewObjectID()
	key, _ := storage.NewKey()
	_, err := e.db.Collection("attachments").InsertOne(context.Background(), models.Attachment{
		ID: attID, Filename: "f.pdf", ContentType: "application/pdf", Size: 1,
		StorageKey: &key, UploadedBy: uploader, Linked: false,
		CreatedAt: time.Now().Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	job, err := e.bulkS.EnqueueTransfer(context.Background(), uploader, Principal{IsAdmin: true, UserID: uploader},
		models.BulkTransferRequest{AssetIDs: []bson.ObjectID{id}, ToVenueID: dest, AttachmentIDs: &[]bson.ObjectID{attID}})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Run the orphan sweep while the job is still QUEUED — reserved doc must survive.
	if err := e.attS.OrphanSweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := e.atts.GetByID(context.Background(), attID); err != nil {
		t.Fatalf("reserved attachment was swept while job queued: %v", err)
	}

	// Complete the job: attachment linked to the succeeded row, reservation released.
	doc := e.runToCompletion(job)
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("status=%s want completed", doc.Status)
	}
	att, err := e.atts.GetByID(context.Background(), attID)
	if err != nil {
		t.Fatalf("get attachment: %v", err)
	}
	if !att.Linked {
		t.Fatal("attachment should be linked to the succeeded row")
	}
	// Reservation cleared on terminal state.
	var raw bson.M
	_ = e.db.Collection("attachments").FindOne(context.Background(), bson.M{"_id": attID}).Decode(&raw)
	if _, still := raw["reservedByJobId"]; still {
		t.Fatal("reservation should be released on terminal state")
	}
}

func TestBulkJobIT_QRResultLifecycle(t *testing.T) {
	e := setupIT(t, BulkJobConfig{BatchSize: 2, Lease: time.Minute, ResultTTL: time.Hour})
	home := e.seedVenue(true)
	p := adminPrincipal()
	id := e.seedAsset(home, home, models.InUse, models.Good, nil)

	job, err := e.bulkS.EnqueueQR(context.Background(), p.UserID, p, models.BulkQrRequest{AssetIDs: []bson.ObjectID{id}})
	if err != nil {
		t.Fatalf("enqueue qr: %v", err)
	}
	// Before completion: no result.
	pre, _ := e.bulk.FindByID(context.Background(), job.ID)
	if pre.ResultStorageKey != "" {
		t.Fatal("result key set before run")
	}

	doc := e.runToCompletion(job)
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("status=%s want completed", doc.Status)
	}
	if doc.ResultStorageKey == "" {
		t.Fatal("expected a result storage key after completion")
	}
	rc, err := e.bulkS.OpenResult(context.Background(), doc)
	if err != nil {
		t.Fatalf("open result: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if len(b) < 4 || string(b[:4]) != "%PDF" {
		t.Fatalf("result is not a PDF (len=%d)", len(b))
	}

	// Retention cleanup deletes bytes and clears the key → subsequent 410.
	// Force the completedAt into the past so it's beyond ResultTTL.
	if _, err := e.db.Collection("bulk_jobs").UpdateByID(context.Background(), job.ID,
		bson.M{"$set": bson.M{"completedAt": time.Now().Add(-2 * time.Hour)}}); err != nil {
		t.Fatalf("age result: %v", err)
	}
	n, err := e.bulkS.CleanupExpiredResults(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("cleanup: n=%d err=%v", n, err)
	}
	after, _ := e.bulk.FindByID(context.Background(), job.ID)
	if after.ResultStorageKey != "" {
		t.Fatal("result key should be cleared after retention cleanup")
	}
}
