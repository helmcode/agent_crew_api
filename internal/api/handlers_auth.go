package api

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/helmcode/agent-crew/internal/auth"
	"github.com/helmcode/agent-crew/internal/models"
)

// AuthConfigResponse is the response for GET /api/auth/config.
type AuthConfigResponse struct {
	Provider            string `json:"provider"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	MultiTenant         bool   `json:"multi_tenant"`
}

// GetAuthConfig returns the current auth provider configuration.
// This endpoint is always public (no auth required).
func (s *Server) GetAuthConfig(c *fiber.Ctx) error {
	providerName := s.authProvider.ProviderName()

	registrationEnabled := false
	if providerName != "noop" {
		if s.multiTenant {
			// Multi-tenant: registration always open.
			registrationEnabled = true
		} else {
			// Single-tenant: registration enabled only if no org exists yet.
			var count int64
			s.db.Model(&models.Organization{}).Count(&count)
			registrationEnabled = count == 0
		}
	}

	return c.JSON(AuthConfigResponse{
		Provider:            providerName,
		RegistrationEnabled: registrationEnabled,
		MultiTenant:         s.multiTenant,
	})
}

// LoginRequest is the request body for POST /api/auth/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login authenticates a user with email/password and returns tokens.
func (s *Server) Login(c *fiber.Ctx) error {
	var req LoginRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if req.Email == "" || req.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "email and password are required")
	}

	tokens, err := s.authProvider.Authenticate(c.Context(), auth.Credentials{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, err.Error())
	}

	// Fetch user for response.
	var user models.User
	if err := s.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to fetch user")
	}

	resp := fiber.Map{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"expires_in":    tokens.ExpiresIn,
		"user": fiber.Map{
			"id":       user.ID,
			"org_id":   user.OrgID,
			"email":    user.Email,
			"name":     user.Name,
			"role":     user.Role,
			"is_owner": user.IsOwner,
		},
	}
	if user.MustChangePassword {
		resp["must_change_password"] = true
	}
	return c.JSON(resp)
}

// RegisterRequest is the request body for POST /api/auth/register.
type RegisterRequest struct {
	OrgName  string `json:"org_name"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register creates a new organization and user.
func (s *Server) Register(c *fiber.Ctx) error {
	// Enforce registration gating.
	if s.authProvider.ProviderName() == "noop" {
		return fiber.NewError(fiber.StatusForbidden, "registration is not available in noop mode")
	}
	if !s.multiTenant {
		var count int64
		s.db.Model(&models.Organization{}).Count(&count)
		if count > 0 {
			return fiber.NewError(fiber.StatusForbidden, "registration is disabled — use an invite link to join")
		}
	}

	var req RegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	tokens, err := s.authProvider.Register(c.Context(), auth.RegisterInput{
		OrgName:  req.OrgName,
		Name:     req.Name,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	// Fetch user for response.
	var user models.User
	if err := s.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to fetch user")
	}

	var org models.Organization
	if err := s.db.Where("id = ?", user.OrgID).First(&org).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to fetch organization")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"expires_in":    tokens.ExpiresIn,
		"user": fiber.Map{
			"id":       user.ID,
			"org_id":   user.OrgID,
			"email":    user.Email,
			"name":     user.Name,
			"role":     user.Role,
			"is_owner": user.IsOwner,
		},
		"organization": fiber.Map{
			"id":   org.ID,
			"name": org.Name,
			"slug": org.Slug,
		},
	})
}

// InviteRegisterRequest is the request body for POST /api/auth/register/invite.
type InviteRegisterRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegisterWithInvite registers a new user using an invite token.
func (s *Server) RegisterWithInvite(c *fiber.Ctx) error {
	var req InviteRegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if req.Token == "" {
		return fiber.NewError(fiber.StatusBadRequest, "invite token is required")
	}

	tokens, err := s.authProvider.Register(c.Context(), auth.RegisterInput{
		Name:        req.Name,
		Email:       req.Email,
		Password:    req.Password,
		InviteToken: req.Token,
	})
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	// Fetch user for response.
	var user models.User
	if err := s.db.Where("email = ?", req.Email).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to fetch user")
	}

	var org models.Organization
	if err := s.db.Where("id = ?", user.OrgID).First(&org).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to fetch organization")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"access_token":  tokens.AccessToken,
		"refresh_token": tokens.RefreshToken,
		"expires_in":    tokens.ExpiresIn,
		"user": fiber.Map{
			"id":       user.ID,
			"org_id":   user.OrgID,
			"email":    user.Email,
			"name":     user.Name,
			"role":     user.Role,
			"is_owner": user.IsOwner,
		},
		"organization": fiber.Map{
			"id":   org.ID,
			"name": org.Name,
			"slug": org.Slug,
		},
	})
}

// RefreshRequest is the request body for POST /api/auth/refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// RefreshToken exchanges a refresh token for a new token pair.
func (s *Server) RefreshToken(c *fiber.Ctx) error {
	var req RefreshRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.RefreshToken == "" {
		return fiber.NewError(fiber.StatusBadRequest, "refresh_token is required")
	}

	tokens, err := s.authProvider.RefreshToken(c.Context(), req.RefreshToken)
	if err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, err.Error())
	}

	return c.JSON(tokens)
}

// GetMe returns the current authenticated user and their organization.
func (s *Server) GetMe(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)
	orgID, _ := c.Locals("org_id").(string)

	var user models.User
	if err := s.db.Where("id = ?", userID).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}

	var org models.Organization
	if err := s.db.Where("id = ?", orgID).First(&org).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "organization not found")
	}

	return c.JSON(fiber.Map{
		"user": fiber.Map{
			"id":                   user.ID,
			"org_id":               user.OrgID,
			"email":                user.Email,
			"name":                 user.Name,
			"role":                 user.Role,
			"is_owner":             user.IsOwner,
			"must_change_password": user.MustChangePassword,
			"created_at":           user.CreatedAt,
			"updated_at":           user.UpdatedAt,
		},
		"organization": fiber.Map{
			"id":         org.ID,
			"name":       org.Name,
			"slug":       org.Slug,
			"created_at": org.CreatedAt,
			"updated_at": org.UpdatedAt,
		},
	})
}

// UpdateMeRequest is the request body for PUT /api/auth/me.
type UpdateMeRequest struct {
	Name string `json:"name"`
}

// UpdateMe updates the current authenticated user's profile.
func (s *Server) UpdateMe(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

	var req UpdateMeRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}

	var user models.User
	if err := s.db.Where("id = ?", userID).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}

	user.Name = req.Name
	if err := s.db.Save(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to update user")
	}

	return c.JSON(fiber.Map{
		"id":         user.ID,
		"org_id":     user.OrgID,
		"email":      user.Email,
		"name":       user.Name,
		"role":       user.Role,
		"is_owner":   user.IsOwner,
		"created_at": user.CreatedAt,
		"updated_at": user.UpdatedAt,
	})
}

// ChangePasswordRequest is the request body for PUT /api/auth/me/password.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword changes the current authenticated user's password.
func (s *Server) ChangePassword(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

	var req ChangePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}

	if req.CurrentPassword == "" || req.NewPassword == "" {
		return fiber.NewError(fiber.StatusBadRequest, "current_password and new_password are required")
	}

	var user models.User
	if err := s.db.Where("id = ?", userID).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "user not found")
	}

	// Verify current password.
	if err := auth.VerifyPassword(user.PasswordHash, req.CurrentPassword); err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "current password is incorrect")
	}

	// Validate new password.
	if err := auth.ValidatePasswordRules(req.NewPassword); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}

	// Hash and save.
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to hash password")
	}

	if err := s.db.Model(&user).Updates(map[string]interface{}{
		"password_hash":        hash,
		"must_change_password": false,
	}).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to update password")
	}

	return c.JSON(fiber.Map{"message": "password updated successfully"})
}

// GetInviteInfo returns public info about an invite token (for the invite landing page).
func (s *Server) GetInviteInfo(c *fiber.Ctx) error {
	token := c.Params("token")
	if token == "" {
		return fiber.NewError(fiber.StatusBadRequest, "token is required")
	}

	tokenHash := auth.HashInviteTokenPublic(token)

	var invite models.Invite
	if err := s.db.Where("token = ? AND used_at IS NULL AND expires_at > ?", tokenHash, time.Now()).First(&invite).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "invalid or expired invite")
	}

	var org models.Organization
	if err := s.db.Where("id = ?", invite.OrgID).First(&org).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "organization not found")
	}

	return c.JSON(fiber.Map{
		"org_name": org.Name,
		"email":    invite.Email,
	})
}
