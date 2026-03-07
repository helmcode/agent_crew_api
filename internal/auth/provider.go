// Package auth defines the authentication provider abstraction for AgentCrew.
package auth

import "context"

// Credentials represents login credentials (email/password for local provider).
type Credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegisterInput represents the data needed to register a new user/org.
type RegisterInput struct {
	OrgName  string `json:"org_name"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
	// InviteToken is set when registering via invite link.
	InviteToken string `json:"invite_token,omitempty"`
}

// Claims represents the authenticated user identity extracted from a token.
type Claims struct {
	UserID string
	OrgID  string
	Email  string
	Name   string
	Role   string
}

// TokenPair holds access and refresh tokens returned after authentication.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// AuthProvider defines the interface for pluggable auth backends.
type AuthProvider interface {
	// Authenticate validates credentials and returns tokens.
	Authenticate(ctx context.Context, credentials Credentials) (*TokenPair, error)

	// ValidateToken validates an access token and returns the user claims.
	ValidateToken(ctx context.Context, token string) (*Claims, error)

	// Register creates a new organization and user, returning tokens.
	Register(ctx context.Context, input RegisterInput) (*TokenPair, error)

	// RefreshToken exchanges a refresh token for a new token pair.
	RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error)

	// ProviderName returns the provider identifier (e.g. "noop", "local").
	ProviderName() string
}
