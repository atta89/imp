package database

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// EnsureIndexes creates all indexes required by the PRD (§8). Idempotent —
// safe to call on every startup; Mongo skips ones that already exist.
func EnsureIndexes(ctx context.Context, db *mongo.Database) error {
	groups := []struct {
		coll    string
		indexes []mongo.IndexModel
	}{
		{"users", []mongo.IndexModel{
			{Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true)},
			{Keys: bson.D{{Key: "venueIds", Value: 1}}},
			{Keys: bson.D{{Key: "role", Value: 1}}},
		}},
		{"venues", []mongo.IndexModel{
			{Keys: bson.D{{Key: "code", Value: 1}}, Options: options.Index().SetUnique(true)},
		}},
		{"categories", []mongo.IndexModel{
			{Keys: bson.D{{Key: "slug", Value: 1}}, Options: options.Index().SetUnique(true)},
		}},
		{"departments", []mongo.IndexModel{
			{
				Keys:    bson.D{{Key: "venueId", Value: 1}, {Key: "code", Value: 1}},
				Options: options.Index().SetUnique(true).SetName("departments_venueId_code_unique"),
			},
			{Keys: bson.D{{Key: "venueId", Value: 1}, {Key: "isActive", Value: 1}}},
		}},
		{"assets", []mongo.IndexModel{
			{Keys: bson.D{{Key: "assetTag", Value: 1}}, Options: options.Index().SetUnique(true)},
			{Keys: bson.D{{Key: "qrToken", Value: 1}}, Options: options.Index().SetUnique(true)},
			{Keys: bson.D{{Key: "homeVenueId", Value: 1}}},
			{Keys: bson.D{{Key: "currentVenueId", Value: 1}}},
			{Keys: bson.D{{Key: "status", Value: 1}}},
			{Keys: bson.D{{Key: "responsibleUserId", Value: 1}}},
			{Keys: bson.D{{Key: "categoryId", Value: 1}}},
			{Keys: bson.D{{Key: "purchaseOrderId", Value: 1}}},
			{Keys: bson.D{{Key: "departmentId", Value: 1}}},
			{Keys: bson.D{{Key: "importJobId", Value: 1}}},
			{Keys: bson.D{{Key: "isOverdue", Value: 1}}},
			// Backs the GET /assets list sort (createdAt desc) and the ids-export
			// (createdAt desc, _id desc) keyset scan.
			{Keys: bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}},
			{
				Keys: bson.D{
					{Key: "name", Value: "text"},
					{Key: "serialNumber", Value: "text"},
					{Key: "assetTag", Value: "text"},
				},
				Options: options.Index().SetName("assets_text_search"),
			},
		}},
		{"purchase_orders", []mongo.IndexModel{
			{Keys: bson.D{{Key: "poNumber", Value: 1}}, Options: options.Index().SetUnique(true)},
			{Keys: bson.D{{Key: "responsibleUserId", Value: 1}}},
			{Keys: bson.D{{Key: "status", Value: 1}}},
			{Keys: bson.D{{Key: "importJobId", Value: 1}}},
		}},
		{"import_jobs", []mongo.IndexModel{
			{Keys: bson.D{{Key: "uploadedBy", Value: 1}, {Key: "createdAt", Value: -1}}},
			{Keys: bson.D{{Key: "status", Value: 1}}},
		}},
		{"bulk_jobs", []mongo.IndexModel{
			// Claim/list scans by status, oldest first.
			{Keys: bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: 1}}},
			// Requester's job history + RBAC ownership lookups.
			{Keys: bson.D{{Key: "requestedBy", Value: 1}, {Key: "createdAt", Value: -1}}},
			// Expired-lease reclaim scan.
			{Keys: bson.D{{Key: "leaseExpiresAt", Value: 1}}},
		}},
		{"movements", []mongo.IndexModel{
			{Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "performedAt", Value: -1}}},
			{Keys: bson.D{{Key: "type", Value: 1}}},
			// Completion-step aggregation of a job's successful movements.
			{Keys: bson.D{{Key: "bulkJobId", Value: 1}}},
		}},
		{"repairs", []mongo.IndexModel{
			{Keys: bson.D{{Key: "assetId", Value: 1}}},
			{Keys: bson.D{{Key: "status", Value: 1}}},
			// Backs the in-repair report keyset scan (createdAt desc, _id desc).
			{Keys: bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}},
		}},
		{"notifications", []mongo.IndexModel{
			{Keys: bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: 1}}},
			{Keys: bson.D{{Key: "recipientUserId", Value: 1}}},
		}},
		{"audit_logs", []mongo.IndexModel{
			{Keys: bson.D{{Key: "entityType", Value: 1}, {Key: "entityId", Value: 1}}},
			{Keys: bson.D{{Key: "performedBy", Value: 1}}},
			{Keys: bson.D{{Key: "performedAt", Value: -1}}},
		}},
		{"attachments", []mongo.IndexModel{
			{Keys: bson.D{{Key: "linked", Value: 1}, {Key: "createdAt", Value: 1}}},
			{Keys: bson.D{{Key: "uploadedBy", Value: 1}}},
			{Keys: bson.D{{Key: "assetIds", Value: 1}}},
		}},
	}

	for _, g := range groups {
		if _, err := db.Collection(g.coll).Indexes().CreateMany(ctx, g.indexes); err != nil {
			return fmt.Errorf("create indexes on %s: %w", g.coll, err)
		}
	}
	return nil
}
