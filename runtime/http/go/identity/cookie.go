package identity

import (
	"net/http"
	"time"
)

// SessionCookie is the cookie a cookie login sets: the config's name, the
// token, Path=/, the Domain the config names, Max-Age the whole seconds
// left in the session, HttpOnly, Secure unless the config turns it off,
// and the config's SameSite. Go writes it as
//
//	__Host-session=<token>; Path=/; Max-Age=1209600; HttpOnly; Secure; SameSite=Lax
//
// with Domain=<domain> after Path when the config names one.
func (c Config) SessionCookie(token string, maxAge time.Duration) *http.Cookie {
	cookie := c.cookie(token)
	cookie.MaxAge = int(maxAge / time.Second)
	if cookie.MaxAge <= 0 {
		cookie.MaxAge = -1
	}
	return cookie
}

// ClearCookie is the cookie logout sets: SessionCookie with no value and
// Max-Age=0, which a browser takes to delete it.
func (c Config) ClearCookie() *http.Cookie {
	cookie := c.cookie("")
	cookie.MaxAge = -1
	return cookie
}

func (c Config) cookie(value string) *http.Cookie {
	cookie := &http.Cookie{
		Name:     c.CookieName(),
		Value:    value,
		Path:     "/",
		Domain:   c.Cookie.Domain,
		HttpOnly: true,
		Secure:   c.cookieSecure(),
	}
	switch c.cookieSameSite() {
	case "Strict":
		cookie.SameSite = http.SameSiteStrictMode
	case "None":
		cookie.SameSite = http.SameSiteNoneMode
	default:
		cookie.SameSite = http.SameSiteLaxMode
	}
	return cookie
}
