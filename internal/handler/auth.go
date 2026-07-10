package handler

import (
	"github.com/gofiber/fiber/v2"
	"go.mongodb.org/mongo-driver/v2/bson"

	"imp/internal/apperror"
	"imp/internal/middleware"
	"imp/internal/models"
	"imp/internal/repository"
	"imp/internal/service"
	"imp/internal/validate"
	"imp/pkg/response"
)

type AuthHandler struct {
	auth      *service.AuthService
	users     *repository.UserRepository
	validator *validate.Validator
}

func NewAuthHandler(auth *service.AuthService, users *repository.UserRepository, v *validate.Validator) *AuthHandler {
	return &AuthHandler{auth: auth, users: users, validator: v}
}

type loginRequest struct {
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required,min=1"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken" validate:"required"`
}

func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	if err := h.validator.Struct(req); err != nil {
		return err
	}
	res, err := h.auth.Login(c.Context(), req.Email, req.Password)
	if err != nil {
		return err
	}
	return response.OK(c, authResponse(res))
}

func (h *AuthHandler) Refresh(c *fiber.Ctx) error {
	var req refreshRequest
	if err := c.BodyParser(&req); err != nil {
		return apperror.BadRequest("invalid JSON body")
	}
	if err := h.validator.Struct(req); err != nil {
		return err
	}
	res, err := h.auth.Refresh(c.Context(), req.RefreshToken)
	if err != nil {
		return err
	}
	return response.OK(c, authResponse(res))
}

// Me returns the currently authenticated user, loaded fresh from the DB.
func (h *AuthHandler) Me(c *fiber.Ctx) error {
	uid, ok := c.Locals(middleware.LocalUserID).(bson.ObjectID)
	if !ok {
		return apperror.Unauthorized("no authenticated user")
	}
	u, err := h.users.FindByID(c.Context(), uid)
	if err != nil {
		return err
	}
	return response.OK(c, u)
}

// authResponse maps the service result into the generated wire DTO.
func authResponse(res *service.LoginResult) models.AuthResponse {
	return models.AuthResponse{
		User: *res.User,
		Tokens: models.TokenPair{
			AccessToken:  res.Tokens.AccessToken,
			RefreshToken: res.Tokens.RefreshToken,
			AccessExp:    res.Tokens.AccessExp.Unix(),
			RefreshExp:   res.Tokens.RefreshExp.Unix(),
		},
	}
}
