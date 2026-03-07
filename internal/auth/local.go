package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/helmcode/agent-crew/internal/models"
)

const (
	bcryptCost          = 12
	defaultAccessExpiry = 24 * time.Hour
	defaultRefreshExpiry = 7 * 24 * time.Hour
	minPasswordLength   = 8
)

var slugRegex = regexp.MustCompile(`[^a-z0-9]+`)

// LocalProvider implements email/password authentication with JWT tokens.
type LocalProvider struct {
	db            *gorm.DB
	jwtSecret     []byte
	accessExpiry  time.Duration
	refreshExpiry time.Duration
	multiTenant   bool
}

// NewLocalProvider creates a new local auth provider.
func NewLocalProvider(db *gorm.DB, config Config) (*LocalProvider, error) {
	if config.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required for local auth provider")
	}
	if len(config.JWTSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters for adequate security")
	}

	accessExpiry := parseDuration(config.JWTAccessExpiration, defaultAccessExpiry)
	refreshExpiry := parseDuration(config.JWTRefreshExpiration, defaultRefreshExpiry)

	return &LocalProvider{
		db:            db,
		jwtSecret:     []byte(config.JWTSecret),
		accessExpiry:  accessExpiry,
		refreshExpiry: refreshExpiry,
		multiTenant:   config.MultiTenant,
	}, nil
}

// Authenticate validates email/password and returns JWT tokens.
func (p *LocalProvider) Authenticate(_ context.Context, creds Credentials) (*TokenPair, error) {
	email := strings.ToLower(strings.TrimSpace(creds.Email))
	var user models.User
	if err := p.db.Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("invalid email or password")
		}
		return nil, fmt.Errorf("querying user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(creds.Password)); err != nil {
		return nil, fmt.Errorf("invalid email or password")
	}

	return p.generateTokenPair(user)
}

// ValidateToken validates a JWT access token and returns the user claims.
func (p *LocalProvider) ValidateToken(_ context.Context, tokenString string) (*Claims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return p.jwtSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	tokenType, _ := claims["type"].(string)
	if tokenType != "access" {
		return nil, fmt.Errorf("invalid token type")
	}

	return &Claims{
		UserID: claimString(claims, "sub"),
		OrgID:  claimString(claims, "org_id"),
		Email:  claimString(claims, "email"),
		Name:   claimString(claims, "name"),
		Role:   claimString(claims, "role"),
	}, nil
}

// Register creates a new organization and user, returning JWT tokens.
func (p *LocalProvider) Register(_ context.Context, input RegisterInput) (*TokenPair, error) {
	if err := validatePassword(input.Password); err != nil {
		return nil, err
	}

	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Name = strings.TrimSpace(input.Name)

	if input.Email == "" || input.Name == "" {
		return nil, fmt.Errorf("email and name are required")
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	var user models.User

	if input.InviteToken != "" {
		// Register via invite — join existing org.
		user, err = p.registerWithInvite(input, string(passwordHash))
		if err != nil {
			return nil, err
		}
	} else {
		// Register new org + owner.
		if input.OrgName == "" {
			return nil, fmt.Errorf("organization name is required")
		}
		user, err = p.registerNewOrg(input, string(passwordHash))
		if err != nil {
			return nil, err
		}
	}

	return p.generateTokenPair(user)
}

// registerNewOrg creates a new organization and its first user (owner).
func (p *LocalProvider) registerNewOrg(input RegisterInput, passwordHash string) (models.User, error) {
	var user models.User

	err := p.db.Transaction(func(tx *gorm.DB) error {
		// Check for duplicate email.
		var count int64
		if err := tx.Model(&models.User{}).Where("email = ?", input.Email).Count(&count).Error; err != nil {
			return fmt.Errorf("checking email: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("email already registered")
		}

		org := models.Organization{
			ID:   uuid.New().String(),
			Name: input.OrgName,
			Slug: generateSlug(input.OrgName),
		}
		if err := tx.Create(&org).Error; err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("organization name already taken")
			}
			return fmt.Errorf("creating organization: %w", err)
		}

		user = models.User{
			ID:           uuid.New().String(),
			OrgID:        org.ID,
			Email:        input.Email,
			Name:         input.Name,
			PasswordHash: passwordHash,
			IsOwner:      true,
			Role:         models.UserRoleAdmin,
		}
		if err := tx.Create(&user).Error; err != nil {
			return fmt.Errorf("creating user: %w", err)
		}

		return nil
	})

	return user, err
}

// registerWithInvite registers a user using an invite token to join an existing org.
func (p *LocalProvider) registerWithInvite(input RegisterInput, passwordHash string) (models.User, error) {
	var user models.User

	tokenHash := hashInviteToken(input.InviteToken)

	err := p.db.Transaction(func(tx *gorm.DB) error {
		var invite models.Invite
		if err := tx.Where("token = ? AND used_at IS NULL", tokenHash).First(&invite).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("invalid or expired invite token")
			}
			return fmt.Errorf("querying invite: %w", err)
		}

		if time.Now().After(invite.ExpiresAt) {
			return fmt.Errorf("invite token has expired")
		}

		if invite.Email != "" && strings.ToLower(strings.TrimSpace(invite.Email)) != input.Email {
			return fmt.Errorf("email does not match invite")
		}

		// Check for duplicate email.
		var count int64
		if err := tx.Model(&models.User{}).Where("email = ?", input.Email).Count(&count).Error; err != nil {
			return fmt.Errorf("checking email: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("email already registered")
		}

		user = models.User{
			ID:           uuid.New().String(),
			OrgID:        invite.OrgID,
			Email:        input.Email,
			Name:         input.Name,
			PasswordHash: passwordHash,
			IsOwner:      false,
			Role:         models.UserRoleMember,
		}
		if err := tx.Create(&user).Error; err != nil {
			return fmt.Errorf("creating user: %w", err)
		}

		// Mark invite as used.
		now := time.Now()
		if err := tx.Model(&invite).Update("used_at", &now).Error; err != nil {
			return fmt.Errorf("marking invite as used: %w", err)
		}

		return nil
	})

	return user, err
}

// RefreshToken exchanges a refresh token for a new token pair.
func (p *LocalProvider) RefreshToken(_ context.Context, refreshToken string) (*TokenPair, error) {
	token, err := jwt.Parse(refreshToken, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return p.jwtSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid refresh token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid refresh token claims")
	}

	tokenType, _ := claims["type"].(string)
	if tokenType != "refresh" {
		return nil, fmt.Errorf("invalid token type for refresh")
	}

	userID := claimString(claims, "sub")
	var user models.User
	if err := p.db.Where("id = ?", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("querying user: %w", err)
	}

	return p.generateTokenPair(user)
}

// ProviderName returns "local".
func (p *LocalProvider) ProviderName() string {
	return "local"
}

// generateTokenPair creates a new access + refresh JWT token pair for a user.
func (p *LocalProvider) generateTokenPair(user models.User) (*TokenPair, error) {
	now := time.Now()

	accessClaims := jwt.MapClaims{
		"sub":    user.ID,
		"org_id": user.OrgID,
		"email":  user.Email,
		"name":   user.Name,
		"role":   user.Role,
		"type":   "access",
		"iat":    now.Unix(),
		"exp":    now.Add(p.accessExpiry).Unix(),
	}
	accessToken := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims)
	accessString, err := accessToken.SignedString(p.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("signing access token: %w", err)
	}

	refreshClaims := jwt.MapClaims{
		"sub":  user.ID,
		"type": "refresh",
		"iat":  now.Unix(),
		"exp":  now.Add(p.refreshExpiry).Unix(),
	}
	refreshToken := jwt.NewWithClaims(jwt.SigningMethodHS256, refreshClaims)
	refreshString, err := refreshToken.SignedString(p.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("signing refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessString,
		RefreshToken: refreshString,
		ExpiresIn:    int64(p.accessExpiry.Seconds()),
	}, nil
}

// validatePassword checks the password meets minimum requirements.
func validatePassword(password string) error {
	if len(password) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}

	var hasUpper, hasLower, hasDigit bool
	for _, c := range password {
		switch {
		case unicode.IsUpper(c):
			hasUpper = true
		case unicode.IsLower(c):
			hasLower = true
		case unicode.IsDigit(c):
			hasDigit = true
		}
	}

	if !hasUpper {
		return fmt.Errorf("password must contain at least one uppercase letter")
	}
	if !hasLower {
		return fmt.Errorf("password must contain at least one lowercase letter")
	}
	if !hasDigit {
		return fmt.Errorf("password must contain at least one digit")
	}

	return nil
}

// generateSlug creates a URL-safe slug from a name.
func generateSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = slugRegex.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		slug = "org"
	}
	return slug
}

// GenerateInviteToken creates a cryptographically random invite token.
// Returns the raw token (to share with the user) and the hash (to store in DB).
func GenerateInviteToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generating random bytes: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	hash = hashInviteToken(raw)
	return raw, hash, nil
}

// hashInviteToken computes the SHA-256 hash of a raw invite token for storage.
func hashInviteToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", h)
}

// parseDuration parses a duration string like "24h" or "7d".
func parseDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	// Handle "Xd" format for days.
	if strings.HasSuffix(s, "d") {
		s = strings.TrimSuffix(s, "d")
		var days int
		if _, err := fmt.Sscanf(s, "%d", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour
		}
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// claimString extracts a string value from JWT map claims.
func claimString(claims jwt.MapClaims, key string) string {
	v, _ := claims[key].(string)
	return v
}

// VerifyPassword compares a bcrypt hash with a plaintext password.
func VerifyPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// HashPassword hashes a password with bcrypt.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// ValidatePasswordRules checks the password meets minimum requirements.
func ValidatePasswordRules(password string) error {
	return validatePassword(password)
}

// HashInviteTokenPublic is the public accessor for hashing invite tokens.
func HashInviteTokenPublic(raw string) string {
	return hashInviteToken(raw)
}

// Ensure LocalProvider implements AuthProvider at compile time.
var _ AuthProvider = (*LocalProvider)(nil)
