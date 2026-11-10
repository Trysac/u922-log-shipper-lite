package logshipperlite

import (
	"errors"
	"strings"
)

// ErrBlank is returned when the input is empty or contains only whitespace.
var ErrBlank = errors.New("logshipperlite: input is blank")

// Normalize trims surrounding whitespace and lower-cases text.
//
// Blank input is rejected with [ErrBlank] instead of returning an empty string.
func Normalize(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", ErrBlank
	}
	return strings.ToLower(trimmed), nil
}
