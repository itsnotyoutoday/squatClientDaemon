// Package fips140tls is a local stand-in for crypto/tls/internal/fips140tls.
// FIPS-only mode is never required for the vendored copy. LOCAL PATCH.
package fips140tls

// Required reports whether FIPS-approved settings are required (never).
func Required() bool { return false }
