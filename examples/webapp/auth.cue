package webapp

import "time"

auth: {
	// Session lifetime, e.g. "24h".
	session_lifetime: time.Duration | *"24h"

	// Minimum password length.
	min_password_length: int & >=8 | *12

	// OAuth2 / OIDC providers, by name.
	oauth2: [Name=string]: {
		client_id!:     string
		client_secret!: string @menu(secret)
		// OIDC discovery URL.
		discovery_url?: =~"^https://"
		scopes:         [...string] | *["openid", "email"]
	}

	// Initial admin users.
	admins: [...string]
}
