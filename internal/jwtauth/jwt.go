package jwtauth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/models"
)

type TokenType string

const (
	TokenAccess  TokenType = "access"
	TokenRefresh TokenType = "refresh"
)

type Claims struct {
	Type     TokenType   `json:"typ"`
	Role     models.Role `json:"role"`
	VenueIDs []string    `json:"venues,omitempty"`
	jwt.RegisteredClaims
}

type Issuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

func NewIssuer(secret string, accessTTL, refreshTTL time.Duration) *Issuer {
	return &Issuer{
		secret:     []byte(secret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
		now:        time.Now,
	}
}

type Pair struct {
	AccessToken  string
	RefreshToken string
	AccessExp    time.Time
	RefreshExp   time.Time
}

func (i *Issuer) IssuePair(user *models.User) (*Pair, error) {
	access, accessExp, err := i.issue(user, TokenAccess, i.accessTTL)
	if err != nil {
		return nil, err
	}
	refresh, refreshExp, err := i.issue(user, TokenRefresh, i.refreshTTL)
	if err != nil {
		return nil, err
	}
	return &Pair{
		AccessToken:  access,
		RefreshToken: refresh,
		AccessExp:    accessExp,
		RefreshExp:   refreshExp,
	}, nil
}

func (i *Issuer) issue(user *models.User, typ TokenType, ttl time.Duration) (string, time.Time, error) {
	now := i.now()
	exp := now.Add(ttl)

	venues := make([]string, 0, len(user.VenueIDs))
	for _, id := range user.VenueIDs {
		venues = append(venues, id.Hex())
	}

	claims := Claims{
		Type:     typ,
		Role:     user.Role,
		VenueIDs: venues,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID.Hex(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	}

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(i.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign token: %w", err)
	}
	return signed, exp, nil
}

// Parse verifies the signature/expiry and returns the claims. The caller must
// still check Type matches what they expect (access vs refresh).
func (i *Issuer) Parse(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return i.secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// SubjectID parses the subject into a Mongo ObjectID (driver v2).
func (c *Claims) SubjectID() (bson.ObjectID, error) {
	return bson.ObjectIDFromHex(c.Subject)
}
