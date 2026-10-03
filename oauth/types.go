package oauth

import "github.com/QuantumNous/new-api/common"

// OAuthToken represents the token received from OAuth provider
type OAuthToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	ClientID     string `json:"-"` // Expected OIDC audience from the server-owned flow.
}

// OAuthUser represents the user info from OAuth provider
type OAuthUser struct {
	GitHubGrant *common.GitHubLoginGrant
	// ProviderUserID is the unique identifier from the OAuth provider
	ProviderUserID string
	// Username is the username from the OAuth provider (e.g., GitHub login)
	Username string
	// DisplayName is the display name from the OAuth provider
	DisplayName string
	// Email is the email from the OAuth provider
	Email string
	// Extra contains any additional provider-specific data
	Extra map[string]any
}

// Texts of OAuthError that the web console translates.
const (
	msgInvalidCode   = "Invalid authorization code"
	msgTokenFailed   = "Failed to get token from {{provider}}, please check settings"
	msgUserInfoEmpty = "{{provider}} returned empty user info, please check settings"
	msgConnectFailed = "Unable to connect to {{provider}} server, please try again later"
	msgGetUserFailed = "Failed to get user information"

	msgGitHubAccessDenied      = "This GitHub account is not allowed to sign in."
	msgGitHubAccessCheckFailed = "Unable to verify GitHub access. Check organization access approval and try again."
)

// OAuthError represents a translatable OAuth error
type OAuthError struct {
	// Message is the text the web console translates
	Message *common.Message
	// RawError is the underlying error for logging purposes
	RawError string
}

func (e *OAuthError) Error() string {
	if e.RawError != "" {
		return e.RawError
	}
	return e.Message.Error()
}

// NewOAuthError creates a new OAuth error with the given web console message
func NewOAuthError(message *common.Message) *OAuthError {
	return &OAuthError{Message: message}
}

// NewOAuthErrorWithRaw creates a new OAuth error with raw error message for logging
func NewOAuthErrorWithRaw(message *common.Message, rawError string) *OAuthError {
	return &OAuthError{Message: message, RawError: rawError}
}

// GitHubAccessDeniedError reports a GitHub account the access policy rejects.
// The web console receives it as a forbidden response, unlike the generic OAuth
// errors, which keep the console's success-envelope status.
type GitHubAccessDeniedError struct {
	*OAuthError
}

// NewGitHubAccessDeniedError creates a forbidden GitHub access policy error.
func NewGitHubAccessDeniedError(message *common.Message) *GitHubAccessDeniedError {
	return &GitHubAccessDeniedError{OAuthError: NewOAuthError(message)}
}

// AccessDeniedError is a direct user-facing access denial message.
type AccessDeniedError struct {
	Message string
}

func (e *AccessDeniedError) Error() string {
	return e.Message
}
