package service

import (
	"context"
	"errors"

	"golang.org/x/crypto/bcrypt"

	"imp/internal/apperror"
	"imp/internal/jwtauth"
	"imp/internal/models"
	"imp/internal/repository"
)

type AuthService struct {
	users  *repository.UserRepository
	issuer *jwtauth.Issuer
}

func NewAuthService(users *repository.UserRepository, issuer *jwtauth.Issuer) *AuthService {
	return &AuthService{users: users, issuer: issuer}
}

type LoginResult struct {
	User   *models.User
	Tokens *jwtauth.Pair
}

func (s *AuthService) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	u, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		// Don't leak whether the email exists.
		if appErr, ok := apperror.As(err); ok && appErr.Kind == apperror.KindNotFound {
			return nil, apperror.Unauthorized("invalid email or password")
		}
		return nil, err
	}
	if !u.IsActive {
		return nil, apperror.Unauthorized("account is disabled")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return nil, apperror.Unauthorized("invalid email or password")
		}
		return nil, apperror.Internal("compare password", err)
	}
	tokens, err := s.issuer.IssuePair(u)
	if err != nil {
		return nil, apperror.Internal("issue tokens", err)
	}
	return &LoginResult{User: u, Tokens: tokens}, nil
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*LoginResult, error) {
	claims, err := s.issuer.Parse(refreshToken)
	if err != nil {
		return nil, apperror.Unauthorized("invalid or expired refresh token")
	}
	if claims.Type != jwtauth.TokenRefresh {
		return nil, apperror.Unauthorized("not a refresh token")
	}
	uid, err := claims.SubjectID()
	if err != nil {
		return nil, apperror.Unauthorized("malformed token subject")
	}
	u, err := s.users.FindByID(ctx, uid)
	if err != nil {
		return nil, apperror.Unauthorized("user no longer exists")
	}
	if !u.IsActive {
		return nil, apperror.Unauthorized("account is disabled")
	}
	tokens, err := s.issuer.IssuePair(u)
	if err != nil {
		return nil, apperror.Internal("issue tokens", err)
	}
	return &LoginResult{User: u, Tokens: tokens}, nil
}
