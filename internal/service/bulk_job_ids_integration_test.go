//go:build integration

// Integration tests for the ids-export bulk job (Task 9). These reuse the
// setupIT/itEnv harness and seed helpers from bulk_job_integration_test.go
// (same package) — see that file for the replica-set requirement and
// MONGO_URI setup. Run:
//
//	MONGO_URI='mongodb://localhost:27077/?replicaSet=rs0test' \
//	    go test -tags integration ./internal/service/ -run BulkJobIT_IDs -count=1
package service

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
	"imp/internal/repository"
)

// ---------------------------------------------------------------------------
// Seed helpers — thin wrappers over the real db writes, modeled on the
// existing itEnv.seedVenue/seedUser/seedAsset helpers in
// bulk_job_integration_test.go (which insert directly via env.db, since
// VenueRepository/UserRepository/AssetRepository expose no test-friendly
// Create()). seedAsset there doesn't parameterize category or name/tag, so
// assetSeed/seedAssetWith below is a small local superset used only by the
// ids-export tests.
// ---------------------------------------------------------------------------

type assetSeed struct {
	home, current bson.ObjectID
	category      bson.ObjectID
	status        models.AssetStatus
	cond          models.AssetCondition
	custodian     *bson.ObjectID
	name          string
	tag           string
}

func (e *itEnv) seedAssetWith(s assetSeed) bson.ObjectID {
	e.t.Helper()
	id := bson.NewObjectID()
	name := s.name
	if name == "" {
		name = e.uniq("Asset")
	}
	tag := s.tag
	if tag == "" {
		tag = e.uniq("TAG")
	}
	cat := s.category
	if cat.IsZero() {
		cat = bson.NewObjectID()
	}
	cond := s.cond
	if cond == "" {
		cond = models.Good
	}
	status := s.status
	if status == "" {
		status = models.Available
	}
	_, err := e.db.Collection("assets").InsertOne(context.Background(), models.Asset{
		ID: id, AssetTag: tag, QrToken: e.uniq("qr"), Name: name,
		CategoryID: cat, HomeVenueID: s.home, CurrentVenueID: s.current,
		Status: status, Condition: cond, ResponsibleUserID: s.custodian, IsActive: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	if err != nil {
		e.t.Fatalf("seed asset: %v", err)
	}
	return id
}

func statusp(s models.AssetStatus) *models.AssetStatus { return &s }

// assortedSeed carries the admin principal plus the venue/category/custodian
// ids seedAssorted used, so filter-parity cases can target real seed data
// (venue/currentVenue/category/responsible combinations), not just admin.
type assortedSeed struct {
	admin          Principal
	venueA, venueB bson.ObjectID
	catA, catB     bson.ObjectID
	custodian      bson.ObjectID
}

// seedAssorted seeds assets across 2 venues, 2 categories, several statuses
// and a custodian, including "away" assets (home != current) and a couple
// whose name/assetTag contain "sparky" for the free-text q test.
func seedAssorted(t *testing.T, env *itEnv) assortedSeed {
	t.Helper()
	venueA := env.seedVenue(true)
	venueB := env.seedVenue(true)
	catA := bson.NewObjectID()
	catB := bson.NewObjectID()
	custodian := env.seedUser(models.Staff, venueA, true, false)

	// Baseline mix across venues/categories/statuses/custodians.
	env.seedAssetWith(assetSeed{home: venueA, current: venueA, category: catA, status: models.Available, cond: models.Good})
	env.seedAssetWith(assetSeed{home: venueA, current: venueA, category: catB, status: models.InUse, cond: models.Good, custodian: &custodian})
	env.seedAssetWith(assetSeed{home: venueB, current: venueB, category: catA, status: models.Available, cond: models.Fair})
	env.seedAssetWith(assetSeed{home: venueB, current: venueB, category: catB, status: models.Retired, cond: models.Poor})
	// Away: home != current.
	env.seedAssetWith(assetSeed{home: venueA, current: venueB, category: catA, status: models.InUse, cond: models.Good})
	env.seedAssetWith(assetSeed{home: venueB, current: venueA, category: catB, status: models.Available, cond: models.Good})
	// Free-text "sparky" matches (name and assetTag).
	env.seedAssetWith(assetSeed{home: venueA, current: venueA, category: catA, status: models.Available, cond: models.Good, name: "Sparky Drill"})
	env.seedAssetWith(assetSeed{home: venueB, current: venueB, category: catB, status: models.InUse, cond: models.Good, tag: env.uniq("SPARKY-TAG")})

	return assortedSeed{admin: adminPrincipal(), venueA: venueA, venueB: venueB, catA: catA, catB: catB, custodian: custodian}
}

// seedTwoVenues seeds a clean 4-vs-3 asset split across two venues (no cross
// contamination) for the RBAC-scoping test.
func seedTwoVenues(t *testing.T, env *itEnv) (admin Principal, venueA, venueB bson.ObjectID) {
	t.Helper()
	venueA = env.seedVenue(true)
	venueB = env.seedVenue(true)
	for i := 0; i < 4; i++ {
		env.seedAssetWith(assetSeed{home: venueA, current: venueA, status: models.Available, cond: models.Good})
	}
	for i := 0; i < 3; i++ {
		env.seedAssetWith(assetSeed{home: venueB, current: venueB, status: models.Available, cond: models.Good})
	}
	return adminPrincipal(), venueA, venueB
}

// seedN seeds n plain assets in a single venue, returning an admin principal.
func seedN(t *testing.T, env *itEnv, n int) Principal {
	t.Helper()
	venue := env.seedVenue(true)
	for i := 0; i < n; i++ {
		env.seedAssetWith(assetSeed{home: venue, current: venue, status: models.Available, cond: models.Good})
	}
	return adminPrincipal()
}

// ---------------------------------------------------------------------------
// Artifact helpers
// ---------------------------------------------------------------------------

// runReadArtifact opens (via OpenResult) and decodes a completed ids job's
// JSON artifact.
func runReadArtifact(t *testing.T, env *itEnv, doc *repository.BulkJobDoc) models.AssetIdsResult {
	t.Helper()
	ctx := context.Background()
	rc, err := env.bulkS.OpenResult(ctx, doc)
	if err != nil {
		t.Fatalf("OpenResult: %v", err)
	}
	defer rc.Close()
	blob, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	var res models.AssetIdsResult
	if err := json.Unmarshal(blob, &res); err != nil {
		t.Fatalf("unmarshal artifact: %v", err)
	}
	return res
}

// runIDsToCompletion enqueues an ids job for the given principal/filters/limit,
// runs the worker to a terminal state, and returns the decoded artifact.
func runIDsToCompletion(t *testing.T, env *itEnv, p Principal, filters *models.AssetListFilters, limit *int) models.AssetIdsResult {
	t.Helper()
	ctx := context.Background()
	job, err := env.bulkS.EnqueueIDs(ctx, p.UserID, p, models.BulkIdsRequest{Filters: filters, Limit: limit})
	if err != nil {
		t.Fatalf("EnqueueIDs: %v", err)
	}
	for {
		claimed, err := env.bulkS.ClaimAndRunOnce(ctx, "w1")
		if err != nil {
			t.Fatalf("ClaimAndRunOnce: %v", err)
		}
		if !claimed {
			break
		}
	}
	doc, err := env.bulkS.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("status = %s, want completed (lastError=%q)", doc.Status, doc.LastError)
	}
	return runReadArtifact(t, env, doc)
}

// truthIDs fully paginates the list repo with the same filter to get the
// ground-truth id set (order-independent comparison).
func truthIDs(t *testing.T, env *itEnv, q AssetListQuery) map[bson.ObjectID]struct{} {
	t.Helper()
	ctx := context.Background()
	filter := buildAssetFilter(q)
	out := map[bson.ObjectID]struct{}{}
	page := 1
	for {
		assets, _, err := env.assets.List(ctx, filter, page, 100)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(assets) == 0 {
			break
		}
		for i := range assets {
			out[assets[i].ID] = struct{}{}
		}
		page++
	}
	return out
}

// ---------------------------------------------------------------------------
// Step 1: filter parity (the critical test)
// ---------------------------------------------------------------------------

func TestBulkJobIT_IDs_FilterParity(t *testing.T) {
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 100000, IDsBatchSize: 100, Lease: 60 * time.Second})
	seed := seedAssorted(t, env)

	cases := []struct {
		name string
		f    *models.AssetListFilters
	}{
		{"empty/all", &models.AssetListFilters{}},
		{"status", &models.AssetListFilters{Status: statusp(models.Available)}},
		{"away", &models.AssetListFilters{Away: boolp(true)}},
		{"q", &models.AssetListFilters{Q: strp("sparky")}},
		{"venue", &models.AssetListFilters{Venue: strp(seed.venueA.Hex())}},
		{"currentVenue", &models.AssetListFilters{CurrentVenue: strp(seed.venueB.Hex())}},
		{"category", &models.AssetListFilters{Category: strp(seed.catA.Hex())}},
		{"responsible", &models.AssetListFilters{Responsible: strp(seed.custodian.Hex())}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := BuildAssetListQuery(tc.f)
			if err != nil {
				t.Fatalf("BuildAssetListQuery: %v", err)
			}
			want := truthIDs(t, env, q) // admin: no scope
			res := runIDsToCompletion(t, env, seed.admin, tc.f, nil)
			got := map[bson.ObjectID]struct{}{}
			for _, id := range res.AssetIDs {
				got[id] = struct{}{}
			}
			if len(got) != len(want) {
				t.Fatalf("filter %+v: got %d ids, want %d", tc.f, len(got), len(want))
			}
			for id := range want {
				if _, ok := got[id]; !ok {
					t.Fatalf("filter %+v: missing id %s", tc.f, id.Hex())
				}
			}
			if res.Count != len(res.AssetIDs) || res.Truncated {
				t.Fatalf("filter %+v: count=%d len=%d truncated=%v (unexpected under generous limit)", tc.f, res.Count, len(res.AssetIDs), res.Truncated)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Step 2: RBAC scoping
// ---------------------------------------------------------------------------

func TestBulkJobIT_IDs_RBACScoping(t *testing.T) {
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 100000, IDsBatchSize: 100, Lease: 60 * time.Second})
	admin, venueA, _ := seedTwoVenues(t, env)

	// Manager scoped to venueA only.
	mgr := Principal{IsAdmin: false, UserID: bson.NewObjectID(), VenueIDs: map[string]struct{}{venueA.Hex(): {}}}

	adminRes := runIDsToCompletion(t, env, admin, &models.AssetListFilters{}, nil)
	mgrRes := runIDsToCompletion(t, env, mgr, &models.AssetListFilters{}, nil)

	if len(mgrRes.AssetIDs) == 0 {
		t.Fatal("manager should see venueA assets")
	}
	if len(mgrRes.AssetIDs) >= len(adminRes.AssetIDs) {
		t.Fatalf("manager (%d) should see fewer ids than admin (%d)", len(mgrRes.AssetIDs), len(adminRes.AssetIDs))
	}
	// Every manager id must be in scope (home or current venueA).
	got := map[bson.ObjectID]struct{}{}
	for _, id := range mgrRes.AssetIDs {
		got[id] = struct{}{}
	}
	scoped := truthIDs(t, env, AssetListQuery{Scope: []bson.ObjectID{venueA}})
	for id := range got {
		if _, ok := scoped[id]; !ok {
			t.Fatalf("manager export leaked out-of-scope id %s", id.Hex())
		}
	}
	// And it must be the FULL scoped set, not a partial one (parity for the
	// scoped case too).
	if len(got) != len(scoped) {
		t.Fatalf("manager export: got %d ids, want %d (full venueA scope)", len(got), len(scoped))
	}
}

// ---------------------------------------------------------------------------
// Step 3: keyset batching (no dupes/gaps/misorder across batch boundaries)
// ---------------------------------------------------------------------------

func TestBulkJobIT_IDs_KeysetBatching(t *testing.T) {
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 100000, IDsBatchSize: 100, Lease: 60 * time.Second})
	admin := seedN(t, env, 250) // > 2x batch size
	res := runIDsToCompletion(t, env, admin, &models.AssetListFilters{}, nil)
	if len(res.AssetIDs) != 250 {
		t.Fatalf("got %d ids, want 250", len(res.AssetIDs))
	}
	seen := map[bson.ObjectID]struct{}{}
	for i, id := range res.AssetIDs {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %s", id.Hex())
		}
		seen[id] = struct{}{}
		if i > 0 && !(res.AssetIDs[i-1].Hex() > id.Hex()) {
			t.Fatalf("not descending at %d: %s then %s", i, res.AssetIDs[i-1].Hex(), id.Hex())
		}
	}
}

// ---------------------------------------------------------------------------
// Step 4: limit/truncation + empty match
// ---------------------------------------------------------------------------

func TestBulkJobIT_IDs_LimitTruncation(t *testing.T) {
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 100000, IDsBatchSize: 100, Lease: 60 * time.Second})
	admin := seedN(t, env, 250)

	// Full export (no limit): all 250, newest _id first.
	full := runIDsToCompletion(t, env, admin, &models.AssetListFilters{}, nil)
	if len(full.AssetIDs) != 250 {
		t.Fatalf("full export got %d ids, want 250", len(full.AssetIDs))
	}

	res := runIDsToCompletion(t, env, admin, &models.AssetListFilters{}, intp(100))
	if len(res.AssetIDs) != 100 || res.Count != 100 {
		t.Fatalf("got count=%d len=%d, want 100", res.Count, len(res.AssetIDs))
	}
	if !res.Truncated {
		t.Fatal("truncated should be true when matched > limit")
	}
	// Truncation keeps the NEWEST 100 (highest _id), in the same descending
	// order — i.e. exactly the first 100 of the full newest-first export.
	for i := 0; i < 100; i++ {
		if res.AssetIDs[i] != full.AssetIDs[i] {
			t.Fatalf("truncated result must be the newest 100 (full[:100]); mismatch at %d: %s vs %s",
				i, res.AssetIDs[i].Hex(), full.AssetIDs[i].Hex())
		}
	}
}

func TestBulkJobIT_IDs_EmptyMatch(t *testing.T) {
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 100000, IDsBatchSize: 100, Lease: 60 * time.Second})
	admin := seedN(t, env, 5)
	res := runIDsToCompletion(t, env, admin, &models.AssetListFilters{Status: statusp(models.AssetStatus("no_such_status"))}, nil)
	if res.Count != 0 || len(res.AssetIDs) != 0 || res.Truncated {
		t.Fatalf("empty match: count=%d len=%d truncated=%v", res.Count, len(res.AssetIDs), res.Truncated)
	}
}

// ---------------------------------------------------------------------------
// Step 5: enqueue validation → 400
// ---------------------------------------------------------------------------

func TestBulkJobIT_IDs_EnqueueValidation(t *testing.T) {
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 50, IDsBatchSize: 10, Lease: 60 * time.Second})
	admin := seedN(t, env, 1)
	ctx := context.Background()
	if _, err := env.bulkS.EnqueueIDs(ctx, admin.UserID, admin, models.BulkIdsRequest{Limit: intp(51)}); err == nil {
		t.Fatal("limit above cap should 400")
	}
	bad := "nothex"
	if _, err := env.bulkS.EnqueueIDs(ctx, admin.UserID, admin, models.BulkIdsRequest{Filters: &models.AssetListFilters{Venue: &bad}}); err == nil {
		t.Fatal("malformed venue should 400")
	}
}

// ---------------------------------------------------------------------------
// Step 6: result lifecycle + retention (also proves the widened
// FindExpiredResults covers ids artifacts, not just qr).
// ---------------------------------------------------------------------------

func TestBulkJobIT_IDs_ResultLifecycleAndRetention(t *testing.T) {
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 100000, IDsBatchSize: 100, Lease: 60 * time.Second, ResultTTL: time.Hour})
	admin := seedN(t, env, 3)
	ctx := context.Background()

	job, err := env.bulkS.EnqueueIDs(ctx, admin.UserID, admin, models.BulkIdsRequest{})
	if err != nil {
		t.Fatalf("EnqueueIDs: %v", err)
	}
	// Before completion: no artifact yet, and OpenResult must refuse.
	doc, _ := env.bulkS.Get(ctx, job.ID)
	if doc.Status == models.BulkJobStatusCompleted {
		t.Fatal("should not be completed before the worker runs")
	}
	if doc.ResultStorageKey != "" {
		t.Fatal("result key should not be set before the worker runs")
	}
	if _, err := env.bulkS.OpenResult(ctx, doc); err == nil {
		t.Fatal("OpenResult before completion should fail (no result key)")
	}

	// Run to completion; artifact readable.
	for {
		claimed, err := env.bulkS.ClaimAndRunOnce(ctx, "w1")
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if !claimed {
			break
		}
	}
	doc, _ = env.bulkS.Get(ctx, job.ID)
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("status=%s want completed", doc.Status)
	}
	res := runReadArtifact(t, env, doc)
	if res.Count != 3 || len(res.AssetIDs) != 3 {
		t.Fatalf("count=%d len=%d, want 3", res.Count, len(res.AssetIDs))
	}

	// Force retention: backdate completedAt beyond TTL, then run cleanup.
	if _, err := env.db.Collection("bulk_jobs").UpdateByID(ctx, job.ID,
		bson.M{"$set": bson.M{"completedAt": time.Now().UTC().Add(-2 * time.Hour)}}); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	n, err := env.bulkS.CleanupExpiredResults(ctx)
	if err != nil || n != 1 {
		t.Fatalf("CleanupExpiredResults: n=%d err=%v (ids artifact must be swept)", n, err)
	}
	doc, _ = env.bulkS.Get(ctx, job.ID)
	if doc.ResultStorageKey != "" {
		t.Fatal("resultStorageKey should be cleared after retention")
	}
	if _, err := env.bulkS.OpenResult(ctx, doc); err == nil {
		t.Fatal("OpenResult after retention should be Gone")
	}
}

// ---------------------------------------------------------------------------
// Step 7: crash/reclaim
// ---------------------------------------------------------------------------

func TestBulkJobIT_IDs_ReclaimRestartsCleanly(t *testing.T) {
	// Lease is short enough that a manually-claimed, already-expired lease
	// triggers Claim's reclaim path (status:running AND leaseExpiresAt < now).
	env := setupIT(t, BulkJobConfig{IDsMaxLimit: 100000, IDsBatchSize: 100, Lease: time.Millisecond})
	admin := seedN(t, env, 250)
	ctx := context.Background()

	job, err := env.bulkS.EnqueueIDs(ctx, admin.UserID, admin, models.BulkIdsRequest{})
	if err != nil {
		t.Fatalf("EnqueueIDs: %v", err)
	}
	// Worker 1 claims but "crashes": simulate by claiming at the repo level
	// with an already-expired lease, plus stale partial scan progress.
	claimed, err := env.bulk.Claim(ctx, "w1-crash", time.Now().UTC().Add(-time.Minute), time.Millisecond)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim: expected to claim job %s, got %+v", job.ID.Hex(), claimed)
	}
	if err := env.bulk.AdvanceIDsScan(ctx, job.ID, "w1-crash", 37, time.Now().UTC().Add(-time.Minute), time.Millisecond); err != nil {
		t.Fatalf("advance (stale partial progress): %v", err)
	}

	// Worker 2 reclaims (lease expired) and completes.
	for {
		claimed, err := env.bulkS.ClaimAndRunOnce(ctx, "w2")
		if err != nil {
			t.Fatalf("reclaim run: %v", err)
		}
		if !claimed {
			break
		}
	}
	doc, _ := env.bulkS.Get(ctx, job.ID)
	if doc.Status != models.BulkJobStatusCompleted {
		t.Fatalf("status = %s, want completed (lastError=%q)", doc.Status, doc.LastError)
	}
	res := runReadArtifact(t, env, doc)
	seen := map[bson.ObjectID]struct{}{}
	for _, id := range res.AssetIDs {
		if _, dup := seen[id]; dup {
			t.Fatalf("reclaim produced duplicate id %s", id.Hex())
		}
		seen[id] = struct{}{}
	}
	if len(res.AssetIDs) != 250 {
		t.Fatalf("reclaim result = %d ids, want 250 (clean restart)", len(res.AssetIDs))
	}
}
