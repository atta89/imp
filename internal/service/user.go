package service

import (
	"context"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"
	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/crypto/bcrypt"

	"imp/internal/apperror"
	"imp/internal/models"
	"imp/internal/repository"
)

type UserService struct {
	users  *repository.UserRepository
	assets *repository.AssetRepository
	pos    *repository.PurchaseOrderRepository
}

func NewUserService(users *repository.UserRepository, assets *repository.AssetRepository, pos *repository.PurchaseOrderRepository) *UserService {
	return &UserService{users: users, assets: assets, pos: pos}
}

type CreateUserInput struct {
	Name          string
	Email         string
	Password      string
	Role          models.Role
	Position      string
	VenueIDs      []bson.ObjectID
	Phone         string
	NotifyByEmail bool
}

func (s *UserService) Create(ctx context.Context, in CreateUserInput) (*models.User, error) {
	if !validRole(in.Role) {
		return nil, apperror.BadRequest("invalid role")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, apperror.Internal("hash password", err)
	}
	u := &models.User{
		Name:          strings.TrimSpace(in.Name),
		Email:         openapi_types.Email(strings.ToLower(strings.TrimSpace(in.Email))),
		PasswordHash:  string(hash),
		Role:          in.Role,
		Position:      strings.TrimSpace(in.Position),
		VenueIDs:      in.VenueIDs,
		NotifyByEmail: in.NotifyByEmail,
		IsActive:      true,
	}
	if phone := strings.TrimSpace(in.Phone); phone != "" {
		u.Phone = &phone
	}
	if u.VenueIDs == nil {
		u.VenueIDs = []bson.ObjectID{}
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// SeedAdmin creates the initial admin if NO admin exists yet. Idempotent.
func (s *UserService) SeedAdmin(ctx context.Context, name, email, password string) (*models.User, bool, error) {
	exists, err := s.users.ExistsByRole(ctx, models.Admin)
	if err != nil {
		return nil, false, err
	}
	if exists {
		return nil, false, nil
	}
	u, err := s.Create(ctx, CreateUserInput{
		Name:          name,
		Email:         email,
		Password:      password,
		Role:          models.Admin,
		Position:      "System Administrator",
		NotifyByEmail: true,
	})
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}

func validRole(r models.Role) bool {
	switch r {
	case models.Admin, models.VenueManager, models.Staff:
		return true
	}
	return false
}

func (s *UserService) Get(ctx context.Context, id bson.ObjectID) (*models.User, error) {
	return s.users.FindByID(ctx, id)
}

func (s *UserService) List(ctx context.Context, page, limit int) ([]models.User, int64, error) {
	return s.users.List(ctx, nil, page, limit)
}

// CreateFromRequest is the admin-facing create flow used by POST /users.
func (s *UserService) CreateFromRequest(ctx context.Context, in models.CreateUserRequest) (*models.User, error) {
	venueIDs := []bson.ObjectID{}
	if in.VenueIDs != nil {
		venueIDs = *in.VenueIDs
	}
	notify := true
	if in.NotifyByEmail != nil {
		notify = *in.NotifyByEmail
	}
	phone := ""
	if in.Phone != nil {
		phone = *in.Phone
	}
	if len(in.Password) < 8 {
		return nil, apperror.BadRequest("password must be at least 8 characters")
	}
	if len(in.Password) > 72 {
		return nil, apperror.BadRequest("password must be at most 72 characters (bcrypt limit)")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, apperror.BadRequest("name is required")
	}
	if strings.TrimSpace(in.Position) == "" {
		return nil, apperror.BadRequest("position is required")
	}
	return s.Create(ctx, CreateUserInput{
		Name:          in.Name,
		Email:         string(in.Email),
		Password:      in.Password,
		Role:          in.Role,
		Position:      in.Position,
		VenueIDs:      venueIDs,
		Phone:         phone,
		NotifyByEmail: notify,
	})
}

// Update is the admin patch. Password, if present, is hashed and stored.
func (s *UserService) Update(ctx context.Context, id bson.ObjectID, in models.UpdateUserRequest) (*models.User, error) {
	set := bson.M{}
	if in.Name != nil {
		if strings.TrimSpace(*in.Name) == "" {
			return nil, apperror.BadRequest("name cannot be empty")
		}
		set["name"] = strings.TrimSpace(*in.Name)
	}
	if in.Email != nil {
		set["email"] = strings.ToLower(strings.TrimSpace(string(*in.Email)))
	}
	if in.Password != nil {
		if len(*in.Password) < 8 || len(*in.Password) > 72 {
			return nil, apperror.BadRequest("password must be 8-72 characters")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, apperror.Internal("hash password", err)
		}
		set["passwordHash"] = string(hash)
	}
	if in.Role != nil {
		if !validRole(*in.Role) {
			return nil, apperror.BadRequest("invalid role")
		}
		set["role"] = *in.Role
	}
	if in.Position != nil {
		if strings.TrimSpace(*in.Position) == "" {
			return nil, apperror.BadRequest("position cannot be empty")
		}
		set["position"] = strings.TrimSpace(*in.Position)
	}
	if in.VenueIDs != nil {
		set["venueIds"] = *in.VenueIDs
	}
	if in.NotifyByEmail != nil {
		set["notifyByEmail"] = *in.NotifyByEmail
	}
	if in.Phone != nil {
		set["phone"] = *in.Phone
	}
	if in.IsActive != nil {
		set["isActive"] = *in.IsActive
	}
	if len(set) == 0 {
		return s.users.FindByID(ctx, id)
	}
	return s.users.Update(ctx, id, set)
}

// Delete soft-blocks if the user is still the responsible person on any asset
// or PO. The caller's recourse is to reassign first, or to deactivate via
// PUT /users/:id with isActive=false (which keeps history intact).
func (s *UserService) Delete(ctx context.Context, id bson.ObjectID) error {
	if _, err := s.users.FindByID(ctx, id); err != nil {
		return err
	}
	if n, err := s.assets.CountByResponsibleUser(ctx, id); err != nil {
		return err
	} else if n > 0 {
		return apperror.Conflict("user is still the custodian on assets; reassign or deactivate instead")
	}
	if n, err := s.pos.CountByResponsibleUser(ctx, id); err != nil {
		return err
	} else if n > 0 {
		return apperror.Conflict("user is still the responsible person on purchase orders; reassign or deactivate instead")
	}
	return s.users.Delete(ctx, id)
}

// UpdateNotificationPreferences is the self-service knob behind
// PUT /me/notification-preferences. Kept tight on purpose — does not allow
// any other field updates.
func (s *UserService) UpdateNotificationPreferences(ctx context.Context, id bson.ObjectID, notify bool) (*models.User, error) {
	return s.users.Update(ctx, id, bson.M{"notifyByEmail": notify})
}

// ChangePassword backs POST /me/password. Requires the caller's current
// password to re-authenticate the change. Returns Unauthorized (not BadRequest)
// when the current password is wrong so failed attempts are indistinguishable
// from missing-account from a client's perspective.
func (s *UserService) ChangePassword(ctx context.Context, id bson.ObjectID, current, next string) error {
	u, err := s.users.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)); err != nil {
		return apperror.Unauthorized("current password is incorrect")
	}
	if len(next) < 8 || len(next) > 72 {
		return apperror.BadRequest("new password must be 8-72 characters")
	}
	if next == current {
		return apperror.BadRequest("new password must differ from current")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return apperror.Internal("hash password", err)
	}
	if _, err := s.users.Update(ctx, id, bson.M{"passwordHash": string(hash)}); err != nil {
		return err
	}
	return nil
}
