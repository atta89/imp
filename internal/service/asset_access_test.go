package service

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

// principalWith builds a Principal scoped to the given venues (by hex id).
func principalWith(userID bson.ObjectID, admin bool, venues ...bson.ObjectID) Principal {
	set := make(map[string]struct{}, len(venues))
	for _, v := range venues {
		set[v.Hex()] = struct{}{}
	}
	return Principal{IsAdmin: admin, UserID: userID, VenueIDs: set}
}

// TestPrincipal_CanAccessAsset exercises the shared authorization rule reused
// by the authenticated scan path and the attachment-download RBAC: admin, OR
// venue scope on the asset's home or current venue, OR current custodian.
func TestPrincipal_CanAccessAsset(t *testing.T) {
	home := bson.NewObjectID()
	current := bson.NewObjectID()
	other := bson.NewObjectID()
	custodian := bson.NewObjectID()
	stranger := bson.NewObjectID()

	assetWithCustodian := &models.Asset{
		HomeVenueID:       home,
		CurrentVenueID:    current,
		ResponsibleUserID: &custodian,
	}
	assetNoCustodian := &models.Asset{
		HomeVenueID:    home,
		CurrentVenueID: current,
	}

	tests := []struct {
		name  string
		p     Principal
		asset *models.Asset
		want  bool
	}{
		{
			name:  "admin",
			p:     principalWith(stranger, true),
			asset: assetWithCustodian,
			want:  true,
		},
		{
			name:  "manager scoped to home venue",
			p:     principalWith(stranger, false, home),
			asset: assetWithCustodian,
			want:  true,
		},
		{
			name:  "scoped to current venue only",
			p:     principalWith(stranger, false, current),
			asset: assetWithCustodian,
			want:  true,
		},
		{
			name:  "scoped to neither and not custodian",
			p:     principalWith(stranger, false, other),
			asset: assetWithCustodian,
			want:  false,
		},
		{
			name:  "current custodian with no venue scope",
			p:     principalWith(custodian, false),
			asset: assetWithCustodian,
			want:  true,
		},
		{
			name:  "no scope, no custodian on asset",
			p:     principalWith(stranger, false),
			asset: assetNoCustodian,
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.CanAccessAsset(tc.asset); got != tc.want {
				t.Errorf("CanAccessAsset() = %v, want %v", got, tc.want)
			}
		})
	}
}
