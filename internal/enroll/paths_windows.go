package enroll

import "os"

// DefaultDir is where the agent keeps its key and certificate. ProgramData
// is machine-wide and survives user profile changes; the service runs as
// SYSTEM and the installer restricts the directory ACL to SYSTEM and
// Administrators. DPAPI wrapping of the key is a follow-up (design doc 4.2).
var DefaultDir = func() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return pd + `\FOG\agent`
	}
	return `C:\ProgramData\FOG\agent`
}()
