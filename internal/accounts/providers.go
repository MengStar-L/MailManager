package accounts

import "fmt"

type Preset struct {
	Provider          Provider
	IMAP              Endpoint
	SMTP              Endpoint
	DefaultAuthMethod AuthMethod
	OAuth             *OAuthProvider
}

var providerPresets = map[Provider]Preset{
	ProviderGoogle: {
		Provider:          ProviderGoogle,
		IMAP:              Endpoint{Host: "imap.gmail.com", Port: 993, TLSMode: TLSImplicit},
		SMTP:              Endpoint{Host: "smtp.gmail.com", Port: 587, TLSMode: TLSStartTLS},
		DefaultAuthMethod: AuthOAuth2,
		OAuth: &OAuthProvider{
			AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
			TokenEndpoint:         "https://oauth2.googleapis.com/token",
			Scopes:                []string{"openid", "email", "https://mail.google.com/"},
		},
	},
	ProviderMicrosoft: {
		Provider:          ProviderMicrosoft,
		IMAP:              Endpoint{Host: "outlook.office365.com", Port: 993, TLSMode: TLSImplicit},
		SMTP:              Endpoint{Host: "smtp.office365.com", Port: 587, TLSMode: TLSStartTLS},
		DefaultAuthMethod: AuthOAuth2,
		OAuth: &OAuthProvider{
			AuthorizationEndpoint: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenEndpoint:         "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			Scopes:                []string{"openid", "email", "offline_access", "https://outlook.office.com/IMAP.AccessAsUser.All", "https://outlook.office.com/SMTP.Send"},
		},
	},
	ProviderQQ: {
		Provider:          ProviderQQ,
		IMAP:              Endpoint{Host: "imap.qq.com", Port: 993, TLSMode: TLSImplicit},
		SMTP:              Endpoint{Host: "smtp.qq.com", Port: 465, TLSMode: TLSImplicit},
		DefaultAuthMethod: AuthPassword,
	},
	ProviderNetEase: {
		Provider:          ProviderNetEase,
		IMAP:              Endpoint{Host: "imap.163.com", Port: 993, TLSMode: TLSImplicit},
		SMTP:              Endpoint{Host: "smtp.163.com", Port: 465, TLSMode: TLSImplicit},
		DefaultAuthMethod: AuthPassword,
	},
}

func PresetFor(provider Provider) (Preset, error) {
	preset, ok := providerPresets[provider]
	if !ok {
		return Preset{}, fmt.Errorf("provider %q has no preset", provider)
	}
	if preset.OAuth != nil {
		copyOAuth := *preset.OAuth
		copyOAuth.Scopes = append([]string(nil), preset.OAuth.Scopes...)
		preset.OAuth = &copyOAuth
	}
	return preset, nil
}
