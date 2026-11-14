// Package godebug is a local stand-in for the standard library's internal/godebug.
// It never reads GODEBUG: every setting reports its default (empty) value, so the
// vendored crypto/tls always behaves per Go defaults. LOCAL PATCH (see ../../../NOTICE).
package godebug

// Setting is a named GODEBUG setting.
type Setting struct{ name string }

// New returns a Setting with the given name.
func New(name string) *Setting { return &Setting{name: name} }

// Name returns the setting name.
func (s *Setting) Name() string { return s.name }

// String returns the setting as name=value.
func (s *Setting) String() string { return s.name + "=" }

// Value always returns "" (the default).
func (s *Setting) Value() string { return "" }

// IncNonDefault is a no-op.
func (s *Setting) IncNonDefault() {}
