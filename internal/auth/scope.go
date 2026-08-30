package auth

import (
	"fmt"
	"strings"
)

const googleOAuthScopePrefix = "https://www.googleapis.com/auth/"

// SendScopeWarning reports a non-blocking warning when a send credential has
// granted OAuth scopes beyond gmail.send. An absent scope is unknown, not broad.
func SendScopeWarning(scope, configKey string) string {
	for _, granted := range strings.Fields(scope) {
		if strings.TrimPrefix(granted, googleOAuthScopePrefix) != "gmail.send" {
			return fmt.Sprintf("granted scope exceeds gmail.send; de-scope the credential behind %s when ready", safeForTerminal(configKey))
		}
	}
	return ""
}
