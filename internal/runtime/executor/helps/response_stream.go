package helps

// CanFinalizeResponseStream reports whether EOF can safely synthesize the source terminator.
func CanFinalizeResponseStream(param any) bool {
	state, okState := param.(interface{ CanFinalizeResponseStream() bool })
	return okState && state.CanFinalizeResponseStream()
}
