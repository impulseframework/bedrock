package members

import (
	"net/http"

	"github.com/impulseframework/impulse/access"
	"github.com/impulseframework/impulse/session/oidc"
	"github.com/impulseframework/impulse/session/signin"
)

// OktaRegistration is the name the okta registration goes under on the Auth: the
// segment of its login and callback routes (user/okta/login), and the connection every
// link made through the provider carries, so it is never changed once people have signed
// in. In development the simulated provider signs it in as APP_OKTA_USERNAME, or the
// shared APP_USERNAME when that is unset, with the roles APP_OKTA_ROLES or APP_ROLES
// name.
const OktaRegistration = "okta"

// OktaSettings is how the application is known to the identity provider for the okta
// registration, read from the environment. The provider is one organization's, an Okta
// or a Keycloak for instance, known by its issuer alone. Under the session library's
// skipAuth build tag the provider is simulated and only the fields the simulated sign-in
// presents are read.
type OktaSettings struct {
	// Issuer is the provider's issuer URL: its configuration is read at
	// <issuer>/.well-known/openid-configuration and every sign-in's token must carry it;
	// the registration checks its shape whether or not the provider is simulated, so the
	// simulated provider needs a well-formed placeholder.
	Issuer string
	// ClientID is the application's client id at the provider.
	ClientID string
	// ClientSecret is the client's secret at the provider.
	ClientSecret string
	// RedirectURL is the browser-facing callback the provider returns the browser to:
	// <base>/portal/api/user/okta/callback.
	RedirectURL string
}

// OktaLogin sends the browser to the identity provider to sign in.
func (h *Handlers) OktaLogin() http.HandlerFunc {
	return h.okta.Login()
}

// OktaCallback receives the browser back from the provider, resolves the identity,
// applies the account rule and starts the session. The method has no logout the provider
// starts, so there is no front-channel route: the session's own logout route ends it.
func (h *Handlers) OktaCallback() http.HandlerFunc {
	return h.okta.Callback()
}

// registerOkta registers the okta registration on the Auth, before the first
// request. The provider is a standard OpenID Connect issuer: its configuration is read at
// <Issuer>/.well-known/openid-configuration, and every sign-in's token must carry the
// issuer. The configuration names the client's secret, which is also how impulse reads
// this registration as an identity provider's and not the persona method's, a public
// client of the same library method.
// The application is the authority for role membership (signin.DisableRoleSync): the
// provider proves who someone is and the application decides what they may do, so a
// sign-in neither reads nor changes the person's roles.
// The account rule for an identity the provider verifies for the first time is the
// library's standard one, members of the directory: everyone the provider signs in is a
// member of its organization and becomes a user under the name the provider presents (the
// preferred username, or the email when it sends none), with no password, linked by the
// provider's subject so a renamed account stays the same person.
// A sign-in whose token says a second factor was used (its amr claim names mfa) carries
// that proof; any other is asked for the sign-in code the Auth mails.
func registerOkta(authSession *Session, accessClient *access.Client, settings *Settings) *oidc.Method {
	_ = accessClient

	return oidc.SignIn(authSession, OktaRegistration, nil, &oidc.Config{
		Issuer:       settings.Okta.Issuer,
		ClientID:     settings.Okta.ClientID,
		ClientSecret: settings.Okta.ClientSecret,
		RedirectURL:  settings.Okta.RedirectURL,
		Connection:   OktaRegistration,
		RoleSync:     signin.DisableRoleSync(),
		Accounts:     signin.MembersOfDirectory,
		LoginURL:     settings.LoginURL,
	})
}
