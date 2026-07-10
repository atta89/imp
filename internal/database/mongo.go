package database

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type Mongo struct {
	Client *mongo.Client
	DB     *mongo.Database
}

// Connect dials Mongo and verifies the connection with a Ping. Driver v2's
// mongo.Connect no longer takes a context — connectivity is verified via Ping.
func Connect(ctx context.Context, uri, dbName string) (*Mongo, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongo ping: %w", err)
	}

	return &Mongo{Client: client, DB: client.Database(dbName)}, nil
}

func (m *Mongo) Close(ctx context.Context) error {
	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return m.Client.Disconnect(closeCtx)
}
