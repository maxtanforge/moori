// Package agenttoken defines the bearer-token format shared by the control service and Agent.
package agenttoken

import (
	"encoding/base64"
	"strings"
)

// Valid accepts a 32-byte token in canonical unpadded base64url.
// Previously issued credentials retain their prefix as part of their identity.
func Valid(token string) bool {
	const legacyPrefix = "moori_agent_"
	const encodedSize = 43
	if len(token) == len(legacyPrefix)+encodedSize {
		token = strings.TrimPrefix(token, legacyPrefix)
	}
	if len(token) != encodedSize {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	return err == nil && len(raw) == 32
}
