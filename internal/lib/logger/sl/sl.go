package sl

import (
	"log/slog"
)

// Err returns a slog attribute named "error" with err's message.
func Err(err error) slog.Attr {
	return slog.Attr{
		Key:   "error",
		Value: slog.StringValue(err.Error()),
	}
}
