package accounts

import "strings"

type FolderRole string

const (
	FolderUnknown FolderRole = "unknown"
	FolderInbox   FolderRole = "inbox"
	FolderSent    FolderRole = "sent"
	FolderDrafts  FolderRole = "drafts"
	FolderTrash   FolderRole = "trash"
	FolderJunk    FolderRole = "junk"
	FolderArchive FolderRole = "archive"
	FolderAll     FolderRole = "all"
)

type FolderRoleSource string

const (
	RoleFromSpecialUse FolderRoleSource = "special_use"
	RoleFromPreset     FolderRoleSource = "provider_preset"
	RoleNeedsUser      FolderRoleSource = "needs_user"
	RoleFromUser       FolderRoleSource = "user"
)

func InferFolderRole(provider Provider, name string, attributes []string) (FolderRole, FolderRoleSource) {
	for _, attribute := range attributes {
		switch strings.ToLower(strings.TrimSpace(attribute)) {
		case `\inbox`:
			return FolderInbox, RoleFromSpecialUse
		case `\sent`:
			return FolderSent, RoleFromSpecialUse
		case `\drafts`:
			return FolderDrafts, RoleFromSpecialUse
		case `\trash`:
			return FolderTrash, RoleFromSpecialUse
		case `\junk`:
			return FolderJunk, RoleFromSpecialUse
		case `\archive`:
			return FolderArchive, RoleFromSpecialUse
		case `\all`:
			return FolderAll, RoleFromSpecialUse
		}
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "inbox" {
		return FolderInbox, RoleFromPreset
	}
	providerNames := map[Provider]map[string]FolderRole{
		ProviderGoogle: {
			"[gmail]/sent mail": FolderSent, "[gmail]/drafts": FolderDrafts,
			"[gmail]/trash": FolderTrash, "[gmail]/spam": FolderJunk,
			"[gmail]/all mail": FolderAll,
		},
		ProviderMicrosoft: {
			"sent items": FolderSent, "drafts": FolderDrafts, "deleted items": FolderTrash,
			"junk email": FolderJunk, "archive": FolderArchive,
		},
		ProviderQQ: {
			"sent messages": FolderSent, "drafts": FolderDrafts,
			"deleted messages": FolderTrash, "junk": FolderJunk,
		},
		ProviderNetEase: {
			"sent": FolderSent, "drafts": FolderDrafts, "trash": FolderTrash, "junk": FolderJunk,
		},
		ProviderCustom: {
			"sent": FolderSent, "sent mail": FolderSent, "sent items": FolderSent, "sent messages": FolderSent,
			"draft": FolderDrafts, "drafts": FolderDrafts,
			"trash": FolderTrash, "deleted items": FolderTrash, "deleted messages": FolderTrash,
			"junk": FolderJunk, "junk email": FolderJunk, "spam": FolderJunk,
			"archive": FolderArchive,
		},
	}
	if role, ok := providerNames[provider][normalized]; ok {
		return role, RoleFromPreset
	}
	return FolderUnknown, RoleNeedsUser
}

func ProviderSavesSentCopy(provider Provider) bool {
	return provider == ProviderGoogle || provider == ProviderMicrosoft
}
