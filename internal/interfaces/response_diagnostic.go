package interfaces

// ResponseDiagnostic marks an error whose text describes a local failure
// (read, dial, or stream error on our side) rather than an upstream response.
// The request logger renders these in the downstream RESPONSE section instead
// of inside the captured upstream API RESPONSE payload.
type ResponseDiagnostic string

// Error implements the error interface.
func (d ResponseDiagnostic) Error() string {
	return string(d)
}

// IsResponseDiagnostic reports whether err carries a local diagnostic payload.
func IsResponseDiagnostic(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	diagnostic, ok := err.(ResponseDiagnostic)
	if !ok {
		return "", false
	}
	return string(diagnostic), true
}
