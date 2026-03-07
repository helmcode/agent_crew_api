package api

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/helmcode/agent-crew/internal/auth"
	"github.com/helmcode/agent-crew/internal/crypto"
	"github.com/helmcode/agent-crew/internal/models"
)

// GetOrg returns the current user's organization.
func (s *Server) GetOrg(c *fiber.Ctx) error {
	orgID := GetOrgID(c)
	var org models.Organization
	if err := s.db.First(&org, "id = ?", orgID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "organization not found")
	}
	return c.JSON(org)
}

// UpdateOrgRequest is the request body for PUT /api/org.
type UpdateOrgRequest struct {
	Name string `json:"name"`
}

// UpdateOrg updates the organization name (admin only).
func (s *Server) UpdateOrg(c *fiber.Ctx) error {
	orgID := GetOrgID(c)

	if !IsAdmin(c) {
		return fiber.NewError(fiber.StatusForbidden, "only admins can update the organization")
	}

	var req UpdateOrgRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}

	var org models.Organization
	if err := s.db.First(&org, "id = ?", orgID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "organization not found")
	}

	if err := s.db.Model(&org).Update("name", req.Name).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to update organization")
	}

	s.db.First(&org, "id = ?", orgID)
	return c.JSON(org)
}

// ListMembers returns all users in the current organization.
func (s *Server) ListMembers(c *fiber.Ctx) error {
	orgID := GetOrgID(c)
	var users []models.User
	if err := s.db.Where("org_id = ?", orgID).Find(&users).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list members")
	}

	// Build response without sensitive fields.
	type memberResponse struct {
		ID        string    `json:"id"`
		Email     string    `json:"email"`
		Name      string    `json:"name"`
		Role      string    `json:"role"`
		IsOwner   bool      `json:"is_owner"`
		CreatedAt time.Time `json:"created_at"`
	}

	resp := make([]memberResponse, len(users))
	for i, u := range users {
		resp[i] = memberResponse{
			ID:        u.ID,
			Email:     u.Email,
			Name:      u.Name,
			Role:      u.Role,
			IsOwner:   u.IsOwner,
			CreatedAt: u.CreatedAt,
		}
	}
	return c.JSON(resp)
}

// RemoveMember removes a user from the organization (admin only).
func (s *Server) RemoveMember(c *fiber.Ctx) error {
	orgID := GetOrgID(c)
	userID := GetUserID(c)
	targetID := c.Params("id")

	if !IsAdmin(c) {
		return fiber.NewError(fiber.StatusForbidden, "only admins can remove members")
	}

	if targetID == userID {
		return fiber.NewError(fiber.StatusBadRequest, "cannot remove yourself")
	}

	var target models.User
	if err := s.db.Where("id = ? AND org_id = ?", targetID, orgID).First(&target).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "member not found")
	}

	if target.IsOwner {
		return fiber.NewError(fiber.StatusForbidden, "cannot remove the organization owner")
	}

	if err := s.db.Delete(&target).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to remove member")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// CreateInviteRequest is the request body for POST /api/org/invites.
type CreateInviteRequest struct {
	Email string `json:"email,omitempty"`
}

// CreateInvite creates a new invite for the organization (admin only).
func (s *Server) CreateInvite(c *fiber.Ctx) error {
	if !IsAdmin(c) {
		return fiber.NewError(fiber.StatusForbidden, "only admins can create invites")
	}

	orgID := GetOrgID(c)

	var req CreateInviteRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	// Validate email is not already in use.
	if req.Email != "" {
		// Check if already a member of this org.
		var existingUser models.User
		if err := s.db.Where("email = ? AND org_id = ?", req.Email, orgID).First(&existingUser).Error; err == nil {
			return fiber.NewError(fiber.StatusConflict, "user with this email is already a member of this organization")
		}

		// Check if registered in another org.
		if err := s.db.Where("email = ?", req.Email).First(&existingUser).Error; err == nil {
			return fiber.NewError(fiber.StatusConflict, "this email is already registered in another organization")
		}

		// Check for an active pending invite in this org.
		var existingInvite models.Invite
		if err := s.db.Where("email = ? AND org_id = ? AND used_at IS NULL AND expires_at > ?", req.Email, orgID, time.Now()).First(&existingInvite).Error; err == nil {
			return fiber.NewError(fiber.StatusConflict, "an active invite already exists for this email")
		}
	}

	rawToken, tokenHash, err := auth.GenerateInviteToken()
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate invite token")
	}

	encToken, err := crypto.Encrypt(rawToken)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to encrypt invite token")
	}

	invite := models.Invite{
		ID:             uuid.New().String(),
		OrgID:          orgID,
		Token:          tokenHash,
		EncryptedToken: encToken,
		Email:          req.Email,
		ExpiresAt:      time.Now().Add(7 * 24 * time.Hour),
	}

	if err := s.db.Create(&invite).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to create invite")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":         invite.ID,
		"token":      rawToken,
		"email":      invite.Email,
		"expires_at": invite.ExpiresAt,
		"created_at": invite.CreatedAt,
	})
}

// ListInvites returns all invites for the organization (admin only).
func (s *Server) ListInvites(c *fiber.Ctx) error {
	if !IsAdmin(c) {
		return fiber.NewError(fiber.StatusForbidden, "only admins can list invites")
	}

	orgID := GetOrgID(c)
	var invites []models.Invite
	if err := s.db.Where("org_id = ?", orgID).Find(&invites).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to list invites")
	}

	type inviteResponse struct {
		ID        string     `json:"id"`
		OrgID     string     `json:"org_id"`
		Email     string     `json:"email,omitempty"`
		Token     string     `json:"token,omitempty"`
		ExpiresAt time.Time  `json:"expires_at"`
		UsedAt    *time.Time `json:"used_at,omitempty"`
		CreatedAt time.Time  `json:"created_at"`
	}

	resp := make([]inviteResponse, len(invites))
	for i, inv := range invites {
		resp[i] = inviteResponse{
			ID:        inv.ID,
			OrgID:     inv.OrgID,
			Email:     inv.Email,
			ExpiresAt: inv.ExpiresAt,
			UsedAt:    inv.UsedAt,
			CreatedAt: inv.CreatedAt,
		}
		// Decrypt the raw token so admins can copy the invite link.
		if inv.EncryptedToken != "" {
			if raw, err := crypto.Decrypt(inv.EncryptedToken); err == nil {
				resp[i].Token = raw
			}
		}
	}
	return c.JSON(resp)
}

// DeleteInvite removes an invite (admin only).
func (s *Server) DeleteInvite(c *fiber.Ctx) error {
	if !IsAdmin(c) {
		return fiber.NewError(fiber.StatusForbidden, "only admins can delete invites")
	}

	orgID := GetOrgID(c)
	inviteID := c.Params("id")

	var invite models.Invite
	if err := s.db.Where("id = ? AND org_id = ?", inviteID, orgID).First(&invite).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "invite not found")
	}

	if err := s.db.Delete(&invite).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to delete invite")
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// UpdateMemberRoleRequest is the request body for PUT /api/org/members/:id/role.
type UpdateMemberRoleRequest struct {
	Role string `json:"role"`
}

// UpdateMemberRole changes a member's role (admin only).
func (s *Server) UpdateMemberRole(c *fiber.Ctx) error {
	orgID := GetOrgID(c)
	targetID := c.Params("id")

	if !IsAdmin(c) {
		return fiber.NewError(fiber.StatusForbidden, "only admins can change member roles")
	}

	var req UpdateMemberRoleRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid request body")
	}
	if req.Role != models.UserRoleAdmin && req.Role != models.UserRoleMember {
		return fiber.NewError(fiber.StatusBadRequest, "role must be 'admin' or 'member'")
	}

	var target models.User
	if err := s.db.Where("id = ? AND org_id = ?", targetID, orgID).First(&target).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "member not found")
	}

	// Cannot downgrade the owner.
	if target.IsOwner && req.Role == models.UserRoleMember {
		return fiber.NewError(fiber.StatusForbidden, "cannot change the owner's role")
	}

	// Cannot downgrade if this is the last admin.
	if target.Role == models.UserRoleAdmin && req.Role == models.UserRoleMember {
		var adminCount int64
		s.db.Model(&models.User{}).Where("org_id = ? AND role = ?", orgID, models.UserRoleAdmin).Count(&adminCount)
		if adminCount <= 1 {
			return fiber.NewError(fiber.StatusForbidden, "cannot remove the last admin")
		}
	}

	if err := s.db.Model(&target).Update("role", req.Role).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to update role")
	}

	s.db.Where("id = ?", targetID).First(&target)
	return c.JSON(fiber.Map{
		"id":       target.ID,
		"email":    target.Email,
		"name":     target.Name,
		"role":     target.Role,
		"is_owner": target.IsOwner,
	})
}

// ResetMemberPassword generates a temporary password for a member (admin only).
func (s *Server) ResetMemberPassword(c *fiber.Ctx) error {
	orgID := GetOrgID(c)
	userID := GetUserID(c)
	targetID := c.Params("id")

	if !IsAdmin(c) {
		return fiber.NewError(fiber.StatusForbidden, "only admins can reset passwords")
	}

	if targetID == userID {
		return fiber.NewError(fiber.StatusBadRequest, "cannot reset your own password — use /api/auth/me/password")
	}

	var target models.User
	if err := s.db.Where("id = ? AND org_id = ?", targetID, orgID).First(&target).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "member not found")
	}

	// Generate a 16-byte random temporary password.
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to generate temporary password")
	}
	tempPassword := base64.RawURLEncoding.EncodeToString(b)

	hash, err := auth.HashPassword(tempPassword)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to hash password")
	}

	if err := s.db.Model(&target).Updates(map[string]interface{}{
		"password_hash":        hash,
		"must_change_password": true,
	}).Error; err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to reset password")
	}

	return c.JSON(fiber.Map{
		"temporary_password": tempPassword,
	})
}
