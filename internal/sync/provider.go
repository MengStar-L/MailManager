package sync

import (
	"errors"
	"fmt"

	"mailmanager/internal/accounts"
)

// CanonicalMessageKey collapses Gmail copies in multiple folders by
// X-GM-MSGID. Other providers retain the stable IMAP location identity.
func CanonicalMessageKey(provider accounts.Provider, location Location, gmailMessageID uint64) (string, error) {
	if provider == accounts.ProviderGoogle {
		if location.AccountID == "" {
			return "", errors.New("account ID is required")
		}
		if gmailMessageID == 0 {
			return "", errors.New("Gmail message key requires X-GM-MSGID")
		}
		return fmt.Sprintf("gmail\x00%s\x00%d", location.AccountID, gmailMessageID), nil
	}
	return location.StableKey()
}
