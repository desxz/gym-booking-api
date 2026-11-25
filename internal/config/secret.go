package config

import "log/slog"

// Secret wraps sensitive strings so they are redacted by every common
// formatting and logging path. The raw value is available only through Expose.
type Secret string

const redacted = "[REDACTED]"

func (s Secret) String() string { return redacted }

func (s Secret) GoString() string { return redacted }

func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// Expose returns the raw secret. Call it only where the secret is actually
// consumed: the database pool and the JWT signer.
func (s Secret) Expose() string { return string(s) }
