package aicli

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// Error is the structured failure contract shared by CLI and in-process callers.
// Details should contain resource IDs and recovery guidance, never credentials.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func failure(code, message string) *Error { return &Error{Code: code, Message: message} }

func sanitizeError(err error, secrets []string) *Error {
	var cliErr *Error
	if !errors.As(err, &cliErr) {
		cliErr = failure("COMMAND_EXECUTION_FAILED", err.Error())
	}
	// Replace in the serialized object to cover nested details and map keys too.
	data, encodeErr := json.Marshal(cliErr)
	if encodeErr != nil {
		return failure("COMMAND_EXECUTION_FAILED", "Command failed; error details could not be encoded.")
	}
	text := string(data)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		quoted, _ := json.Marshal(secret)
		text = strings.ReplaceAll(text, string(quoted[1:len(quoted)-1]), "[REDACTED]")
	}
	var safe Error
	if json.Unmarshal([]byte(text), &safe) != nil {
		return failure("COMMAND_EXECUTION_FAILED", "Command failed.")
	}
	return &safe
}
