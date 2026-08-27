// Package auth resolves Gmail credentials for mailbox, mirroring the gws shim
// doctrine: fail loud, no fallbacks, no silent identity switches.
package auth

import (
	"fmt"
	"os"
)

type Account string

const (
	AccountWork     Account = "work"
	AccountPersonal Account = "personal"
)

// ResolveAccount picks the account: flag value, else GWS_ACCOUNT, else work.
func ResolveAccount(flagValue string) (Account, error) {
	if flagValue != "" {
		if a := Account(flagValue); a == AccountWork || a == AccountPersonal {
			return a, nil
		}
		return "", fmt.Errorf("--account must be 'work' or 'personal', got '%s'", flagValue)
	}
	if env := os.Getenv("GWS_ACCOUNT"); env != "" {
		if a := Account(env); a == AccountWork || a == AccountPersonal {
			return a, nil
		}
		return "", fmt.Errorf("GWS_ACCOUNT must be 'work' or 'personal', got '%s'", env)
	}
	return AccountWork, nil
}
