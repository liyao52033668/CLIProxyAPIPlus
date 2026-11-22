package executor

// antigravitySessionHashSeparator separates the fields of an Antigravity
// synthesized session-ID hash input. It must not appear in credential IDs or
// prompt text, so a NUL byte is used.
const antigravitySessionHashSeparator = "\x00"
