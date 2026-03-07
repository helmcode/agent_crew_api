package auth

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
)

const (
	defaultOrgID   = "00000000-0000-0000-0000-000000000000"
	defaultOrgName = "Default"
	defaultOrgSlug = "default"
	defaultUserID  = "00000000-0000-0000-0000-000000000001"
)

// NoopProvider is a pass-through auth provider for self-hosted deployments
// without authentication. It auto-creates a default org and user on startup,
// and backfills org_id on all existing records.
type NoopProvider struct {
	db      *gorm.DB
	orgID   string
	userID  string
	email   string
	name    string
}

// NewNoopProvider initializes the noop provider, ensuring a default org and
// user exist and all existing records have an org_id set.
func NewNoopProvider(db *gorm.DB) (*NoopProvider, error) {
	p := &NoopProvider{
		db:     db,
		orgID:  defaultOrgID,
		userID: defaultUserID,
		email:  "admin@agentcrew.local",
		name:   "Admin",
	}

	if err := p.ensureDefaults(); err != nil {
		return nil, fmt.Errorf("noop provider init: %w", err)
	}

	return p, nil
}

// ensureDefaults creates the default org and user if they don't exist,
// then backfills org_id on all existing records that lack one.
func (p *NoopProvider) ensureDefaults() error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		// Create default organization if not exists.
		var org models.Organization
		result := tx.Where("id = ?", p.orgID).First(&org)
		if result.Error != nil {
			if result.Error == gorm.ErrRecordNotFound {
				org = models.Organization{
					ID:   p.orgID,
					Name: defaultOrgName,
					Slug: defaultOrgSlug,
				}
				if err := tx.Create(&org).Error; err != nil {
					return fmt.Errorf("creating default organization: %w", err)
				}
				slog.Info("created default organization", "id", p.orgID)
			} else {
				return fmt.Errorf("querying default organization: %w", result.Error)
			}
		}

		// Create default user if not exists.
		var user models.User
		result = tx.Where("id = ?", p.userID).First(&user)
		if result.Error != nil {
			if result.Error == gorm.ErrRecordNotFound {
				user = models.User{
					ID:      p.userID,
					OrgID:   p.orgID,
					Email:   p.email,
					Name:    p.name,
					IsOwner: true,
					Role:    models.UserRoleAdmin,
				}
				if err := tx.Create(&user).Error; err != nil {
					return fmt.Errorf("creating default user: %w", err)
				}
				slog.Info("created default user", "id", p.userID)
			} else {
				return fmt.Errorf("querying default user: %w", result.Error)
			}
		}

		// Backfill org_id on all existing records that have an empty org_id.
		if err := p.backfillOrgID(tx); err != nil {
			return fmt.Errorf("backfilling org_id: %w", err)
		}

		// Backfill roles: owners get admin, others get member.
		if err := p.backfillRoles(tx); err != nil {
			return fmt.Errorf("backfilling roles: %w", err)
		}

		return nil
	})
}

// backfillOrgID sets org_id = defaultOrgID on all existing records that
// have an empty or null org_id. This is idempotent.
func (p *NoopProvider) backfillOrgID(tx *gorm.DB) error {
	tables := []struct {
		name  string
		model interface{}
	}{
		{"teams", &models.Team{}},
		{"agents", &models.Agent{}},
		{"settings", &models.Settings{}},
		{"schedules", &models.Schedule{}},
		{"webhooks", &models.Webhook{}},
		{"post_actions", &models.PostAction{}},
	}

	for _, t := range tables {
		result := tx.Model(t.model).Where("org_id IS NULL OR org_id = ''").Update("org_id", p.orgID)
		if result.Error != nil {
			return fmt.Errorf("backfilling %s: %w", t.name, result.Error)
		}
		if result.RowsAffected > 0 {
			slog.Info("backfilled org_id", "table", t.name, "rows", result.RowsAffected)
		}
	}

	return nil
}

// backfillRoles sets role=admin for owners and role=member for others
// where the role is empty (pre-migration records).
func (p *NoopProvider) backfillRoles(tx *gorm.DB) error {
	result := tx.Model(&models.User{}).Where("role = '' OR role IS NULL").Where("is_owner = ?", true).Update("role", models.UserRoleAdmin)
	if result.Error != nil {
		return fmt.Errorf("backfilling admin roles: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		slog.Info("backfilled admin roles", "rows", result.RowsAffected)
	}

	result = tx.Model(&models.User{}).Where("role = '' OR role IS NULL").Update("role", models.UserRoleMember)
	if result.Error != nil {
		return fmt.Errorf("backfilling member roles: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		slog.Info("backfilled member roles", "rows", result.RowsAffected)
	}

	return nil
}

// Authenticate always succeeds for noop, returning dummy tokens.
func (p *NoopProvider) Authenticate(_ context.Context, _ Credentials) (*TokenPair, error) {
	return &TokenPair{
		AccessToken:  "noop-access-token",
		RefreshToken: "noop-refresh-token",
		ExpiresIn:    86400,
	}, nil
}

// ValidateToken always returns the default user claims without validation.
func (p *NoopProvider) ValidateToken(_ context.Context, _ string) (*Claims, error) {
	return &Claims{
		UserID: p.userID,
		OrgID:  p.orgID,
		Email:  p.email,
		Name:   p.name,
		Role:   models.UserRoleAdmin,
	}, nil
}

// Register creates a new user in the default org (noop mode only has one org).
func (p *NoopProvider) Register(_ context.Context, input RegisterInput) (*TokenPair, error) {
	user := models.User{
		ID:    uuid.New().String(),
		OrgID: p.orgID,
		Email: input.Email,
		Name:  input.Name,
	}
	if err := p.db.Create(&user).Error; err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("email already registered")
		}
		return nil, fmt.Errorf("creating user: %w", err)
	}

	return &TokenPair{
		AccessToken:  "noop-access-token",
		RefreshToken: "noop-refresh-token",
		ExpiresIn:    86400,
	}, nil
}

// RefreshToken always succeeds for noop, returning fresh dummy tokens.
func (p *NoopProvider) RefreshToken(_ context.Context, _ string) (*TokenPair, error) {
	return &TokenPair{
		AccessToken:  "noop-access-token",
		RefreshToken: "noop-refresh-token",
		ExpiresIn:    86400,
	}, nil
}

// ProviderName returns "noop".
func (p *NoopProvider) ProviderName() string {
	return "noop"
}

// DefaultOrgID returns the well-known default organization ID used by noop.
func DefaultOrgID() string {
	return defaultOrgID
}

// DefaultUserID returns the well-known default user ID used by noop.
func DefaultUserID() string {
	return defaultUserID
}

// Ensure NoopProvider implements AuthProvider at compile time.
var _ AuthProvider = (*NoopProvider)(nil)
