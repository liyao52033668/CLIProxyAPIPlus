// Package auth provides authentication management, scheduling, and session handling for CLIProxyAPI.
package auth

import (
	"errors"
	"net/http"
)

const requestScopedErrorCode = "request_scoped"

// Error describes an authentication related failure in a provider agnostic format.
type Error struct {
	// Code is a short machine readable identifier.
	Code string `json:"code,omitempty"`
	// Message is a human readable description of the failure.
	Message string `json:"message"`
	// Retryable indicates whether a retry might fix the issue automatically.
	Retryable bool `json:"retryable"`
	// HTTPStatus optionally records an HTTP-like status code for the error.
	HTTPStatus int `json:"http_status,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// StatusCode implements optional status accessor for manager decision making.
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// IsRequestScoped reports whether the failure is tied to the current request
// rather than the selected credential.
func (e *Error) IsRequestScoped() bool {
	return e != nil && e.Code == requestScopedErrorCode
}

// IsTerminalAuthError checks if err or any error in its chain represents a permanent upstream auth failure.
func IsTerminalAuthError(err error) bool {
	if err == nil {
		return false
	}
	type terminalAuthProvider interface {
		IsTerminalAuth() bool
	}
	var tap terminalAuthProvider
	if errors.As(err, &tap) && tap != nil {
		return tap.IsTerminalAuth()
	}
	return false
}

// terminalAuthError marks an auth error as a permanent upstream authentication
// failure while keeping the wrapped *Error and its cause reachable through
// errors.As and errors.Unwrap.
type terminalAuthError struct {
	err   *Error
	cause error
}

func (e *terminalAuthError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *terminalAuthError) IsTerminalAuth() bool { return true }

func (e *terminalAuthError) StatusCode() int {
	if e == nil || e.err == nil {
		return 0
	}
	return e.err.StatusCode()
}

func (e *terminalAuthError) IsRequestScoped() bool {
	return e != nil && e.err.IsRequestScoped()
}

func (e *terminalAuthError) Unwrap() error { return e.cause }

// As exposes the wrapped *Error to errors.As without unwrapping the cause.
func (e *terminalAuthError) As(target any) bool {
	if t, ok := target.(**Error); ok {
		*t = e.err
		return true
	}
	return false
}

// NewTerminalAuthError wraps an *Error and cause as a terminal upstream authentication failure.
func NewTerminalAuthError(err *Error, cause error) error {
	if err == nil {
		return nil
	}
	return &terminalAuthError{err: err, cause: cause}
}

// newTerminalAuthUnavailableError builds the terminal error returned when every
// candidate credential is stuck in an unauthorized failure state.
func newTerminalAuthUnavailableError(cause error) error {
	return NewTerminalAuthError(&Error{
		Code:       "auth_unavailable",
		Message:    "no auth available",
		Retryable:  false,
		HTTPStatus: http.StatusServiceUnavailable,
	}, cause)
}
