package accountsecret

import (
	"encoding/json"
	"strings"

	"golang.org/x/oauth2"

	"mailmanager/internal/accounts"
)

type Credential struct {
	Username    string           `json:"username"`
	Secret      string           `json:"secret"`
	IMAPTLSMode accounts.TLSMode `json:"imap_tls_mode"`
	SMTPTLSMode accounts.TLSMode `json:"smtp_tls_mode"`
}

type OAuthCredential struct {
	Username    string           `json:"username"`
	Token       *oauth2.Token    `json:"token"`
	IMAPTLSMode accounts.TLSMode `json:"imap_tls_mode"`
	SMTPTLSMode accounts.TLSMode `json:"smtp_tls_mode"`
}

func Purpose(provider, email string) string {
	return "account/" + strings.ToLower(strings.TrimSpace(provider)) + "/" + strings.ToLower(strings.TrimSpace(email)) + "/credential"
}

func Marshal(credential Credential) ([]byte, error) {
	return json.Marshal(credential)
}

func Unmarshal(value []byte) (Credential, error) {
	var credential Credential
	err := json.Unmarshal(value, &credential)
	return credential, err
}

func MarshalOAuth(credential OAuthCredential) ([]byte, error) {
	return json.Marshal(credential)
}

func UnmarshalOAuth(value []byte) (OAuthCredential, error) {
	var credential OAuthCredential
	err := json.Unmarshal(value, &credential)
	return credential, err
}
