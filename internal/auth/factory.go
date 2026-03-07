package auth

import (
	"fmt"

	"gorm.io/gorm"
)

// Config holds configuration for auth providers that need it (e.g. local, OIDC).
type Config struct {
	JWTSecret           string
	JWTAccessExpiration string
	JWTRefreshExpiration string
	MultiTenant         bool
}

// NewProvider creates the appropriate AuthProvider based on the provider type.
func NewProvider(providerType string, db *gorm.DB, config Config) (AuthProvider, error) {
	switch providerType {
	case "noop", "":
		return NewNoopProvider(db)
	case "local":
		return NewLocalProvider(db, config)
	default:
		return nil, fmt.Errorf("unknown auth provider: %s", providerType)
	}
}
