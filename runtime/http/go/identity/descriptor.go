package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// DescriptorVersion is the identity descriptor format the store reads.
const DescriptorVersion = 1

// Descriptor is a schema's identity descriptor: the tables that hold its
// users, sessions, credentials, API keys, roles and role grants, and which
// column of each plays which part. The DB build writes it as
// identity/<schema>.json and as the Go types' IdentityDescriptor constant;
// internal/generator/identitydesc/README.md is its contract.
type Descriptor struct {
	Version    int                  `json:"version"`
	User       DescriptorUser       `json:"user"`
	Session    DescriptorSession    `json:"session"`
	Credential DescriptorCredential `json:"credential"`
	// APIKey is nil in a descriptor written before the UserApiKey table
	// (D114): its store has no API keys.
	APIKey *DescriptorAPIKey `json:"apiKey,omitempty"`
	// OAuth is nil in a descriptor written before the OAuth tables (#695).
	// The Go server serves no OAuth yet; it reads the member so a
	// descriptor that has it parses.
	OAuth *DescriptorOAuth `json:"oauth,omitempty"`
	// Role and RoleGrant are nil when the schema has no UserRole table.
	Role      *DescriptorRole      `json:"role,omitempty"`
	RoleGrant *DescriptorRoleGrant `json:"roleGrant,omitempty"`
}

// DescriptorUser is the table with the User trait.
type DescriptorUser struct {
	Type    string `json:"type"`
	Table   string `json:"table"`
	Columns struct {
		Key   string `json:"key"`
		Login string `json:"login"`
		Name  string `json:"name"`
	} `json:"columns"`
	KeyScalar   string `json:"keyScalar"`
	LoginScalar string `json:"loginScalar"`
	NameScalar  string `json:"nameScalar"`
}

// DescriptorSession is the Session table the loader adds.
type DescriptorSession struct {
	Table   string `json:"table"`
	Columns struct {
		ID         string `json:"id"`
		User       string `json:"user"`
		TokenHash  string `json:"tokenHash"`
		CreatedAt  string `json:"createdAt"`
		ExpiresAt  string `json:"expiresAt"`
		LastSeenAt string `json:"lastSeenAt"`
		RevokedAt  string `json:"revokedAt"`
	} `json:"columns"`
}

// DescriptorCredential is the UserCredential table the loader adds.
type DescriptorCredential struct {
	Table   string `json:"table"`
	Columns struct {
		ID                string `json:"id"`
		User              string `json:"user"`
		PasswordHash      string `json:"passwordHash"`
		PasswordChangedAt string `json:"passwordChangedAt"`
		DisabledAt        string `json:"disabledAt"`
		PendingAt         string `json:"pendingAt"`
	} `json:"columns"`
}

// DescriptorAPIKey is the UserApiKey table the loader adds (D114).
type DescriptorAPIKey struct {
	Table   string `json:"table"`
	Columns struct {
		ID          string `json:"id"`
		User        string `json:"user"`
		Name        string `json:"name"`
		TokenHash   string `json:"tokenHash"`
		Permissions string `json:"permissions"`
		CreatedAt   string `json:"createdAt"`
		ExpiresAt   string `json:"expiresAt"`
		LastUsedAt  string `json:"lastUsedAt"`
		RevokedAt   string `json:"revokedAt"`
	} `json:"columns"`
}

// DescriptorOAuth is the four tables of an OAuth authorization server
// (#695).
type DescriptorOAuth struct {
	Client struct {
		Table   string `json:"table"`
		Columns struct {
			ID           string `json:"id"`
			ClientID     string `json:"clientId"`
			Kind         string `json:"kind"`
			Name         string `json:"name"`
			URI          string `json:"uri"`
			RedirectURIs string `json:"redirectUris"`
			CreatedAt    string `json:"createdAt"`
			RefreshedAt  string `json:"refreshedAt"`
			FreshUntil   string `json:"freshUntil"`
			LastUsedAt   string `json:"lastUsedAt"`
		} `json:"columns"`
	} `json:"client"`
	Grant struct {
		Table   string `json:"table"`
		Columns struct {
			ID          string `json:"id"`
			User        string `json:"user"`
			Client      string `json:"client"`
			Permissions string `json:"permissions"`
			Resource    string `json:"resource"`
			CreatedAt   string `json:"createdAt"`
			ExpiresAt   string `json:"expiresAt"`
			LastUsedAt  string `json:"lastUsedAt"`
			RevokedAt   string `json:"revokedAt"`
		} `json:"columns"`
	} `json:"grant"`
	Code struct {
		Table   string `json:"table"`
		Columns struct {
			ID            string `json:"id"`
			Client        string `json:"client"`
			User          string `json:"user"`
			Grant         string `json:"grant"`
			RedirectURI   string `json:"redirectUri"`
			CodeChallenge string `json:"codeChallenge"`
			State         string `json:"state"`
			Scope         string `json:"scope"`
			Resource      string `json:"resource"`
			Nonce         string `json:"nonce"`
			CodeHash      string `json:"codeHash"`
			CreatedAt     string `json:"createdAt"`
			ExpiresAt     string `json:"expiresAt"`
			UsedAt        string `json:"usedAt"`
		} `json:"columns"`
	} `json:"code"`
	Token struct {
		Table   string `json:"table"`
		Columns struct {
			ID        string `json:"id"`
			Grant     string `json:"grant"`
			Kind      string `json:"kind"`
			TokenHash string `json:"tokenHash"`
			CreatedAt string `json:"createdAt"`
			ExpiresAt string `json:"expiresAt"`
			UsedAt    string `json:"usedAt"`
			RevokedAt string `json:"revokedAt"`
		} `json:"columns"`
	} `json:"token"`
}

// names adds the member path of each of o's names to names.
func (o *DescriptorOAuth) names(names map[string]string) {
	add := func(prefix string, table string, columns any) {
		names[prefix+".table"] = table
		v := reflect.ValueOf(columns)
		for i := 0; i < v.NumField(); i++ {
			tag := strings.Split(v.Type().Field(i).Tag.Get("json"), ",")[0]
			names[prefix+".columns."+tag] = v.Field(i).String()
		}
	}
	add("oauth.client", o.Client.Table, o.Client.Columns)
	add("oauth.grant", o.Grant.Table, o.Grant.Columns)
	add("oauth.code", o.Code.Table, o.Code.Columns)
	add("oauth.token", o.Token.Table, o.Token.Columns)
}

// DescriptorRole is the table with the UserRole trait.
type DescriptorRole struct {
	Type    string `json:"type"`
	Table   string `json:"table"`
	Columns struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Permissions string `json:"permissions"`
	} `json:"columns"`
	KeyScalar string `json:"keyScalar"`
}

// DescriptorRoleGrant is the UserRoleGrant table the loader adds beside a
// UserRole table.
type DescriptorRoleGrant struct {
	Table   string `json:"table"`
	Columns struct {
		ID        string `json:"id"`
		User      string `json:"user"`
		Role      string `json:"role"`
		GrantedAt string `json:"grantedAt"`
	} `json:"columns"`
}

// ParseDescriptor reads an identity descriptor. It refuses any version but
// DescriptorVersion, unknown members, an empty name, a role table without
// its grant table or the other way round, and a name holding a NUL, which
// no quoting can carry. A descriptor without apiKey is one written before
// D114, and one without oauth one written before #695; each is read as it
// was.
func ParseDescriptor(data []byte) (Descriptor, error) {
	var d Descriptor
	if err := checkMemberNames(data, reflect.TypeOf(d)); err != nil {
		return Descriptor{}, fmt.Errorf("identity: descriptor: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Descriptor{}, fmt.Errorf("identity: descriptor: %w", err)
	}
	if d.Version != DescriptorVersion {
		return Descriptor{}, fmt.Errorf("identity: descriptor: version %d, want %d", d.Version, DescriptorVersion)
	}
	if (d.Role == nil) != (d.RoleGrant == nil) {
		return Descriptor{}, errors.New("identity: descriptor: role and roleGrant are present together or not at all")
	}
	names := map[string]string{
		"user.type": d.User.Type, "user.table": d.User.Table,
		"user.columns.key": d.User.Columns.Key, "user.columns.login": d.User.Columns.Login, "user.columns.name": d.User.Columns.Name,
		"user.keyScalar": d.User.KeyScalar, "user.loginScalar": d.User.LoginScalar, "user.nameScalar": d.User.NameScalar,
		"session.table": d.Session.Table, "session.columns.id": d.Session.Columns.ID, "session.columns.user": d.Session.Columns.User,
		"session.columns.tokenHash": d.Session.Columns.TokenHash, "session.columns.createdAt": d.Session.Columns.CreatedAt,
		"session.columns.expiresAt": d.Session.Columns.ExpiresAt, "session.columns.lastSeenAt": d.Session.Columns.LastSeenAt,
		"session.columns.revokedAt": d.Session.Columns.RevokedAt,
		"credential.table":          d.Credential.Table, "credential.columns.id": d.Credential.Columns.ID,
		"credential.columns.user": d.Credential.Columns.User, "credential.columns.passwordHash": d.Credential.Columns.PasswordHash,
		"credential.columns.passwordChangedAt": d.Credential.Columns.PasswordChangedAt, "credential.columns.disabledAt": d.Credential.Columns.DisabledAt,
		"credential.columns.pendingAt": d.Credential.Columns.PendingAt,
	}
	if k := d.APIKey; k != nil {
		names["apiKey.table"], names["apiKey.columns.id"], names["apiKey.columns.user"] = k.Table, k.Columns.ID, k.Columns.User
		names["apiKey.columns.name"], names["apiKey.columns.tokenHash"], names["apiKey.columns.permissions"] = k.Columns.Name, k.Columns.TokenHash, k.Columns.Permissions
		names["apiKey.columns.createdAt"], names["apiKey.columns.expiresAt"] = k.Columns.CreatedAt, k.Columns.ExpiresAt
		names["apiKey.columns.lastUsedAt"], names["apiKey.columns.revokedAt"] = k.Columns.LastUsedAt, k.Columns.RevokedAt
	}
	if o := d.OAuth; o != nil {
		o.names(names)
	}
	if r := d.Role; r != nil {
		names["role.type"], names["role.table"], names["role.keyScalar"] = r.Type, r.Table, r.KeyScalar
		names["role.columns.key"], names["role.columns.name"], names["role.columns.permissions"] = r.Columns.Key, r.Columns.Name, r.Columns.Permissions
		g := d.RoleGrant
		names["roleGrant.table"], names["roleGrant.columns.id"] = g.Table, g.Columns.ID
		names["roleGrant.columns.user"], names["roleGrant.columns.role"], names["roleGrant.columns.grantedAt"] = g.Columns.User, g.Columns.Role, g.Columns.GrantedAt
	}
	for _, member := range slices.Sorted(maps.Keys(names)) {
		name := names[member]
		if name == "" {
			return Descriptor{}, fmt.Errorf("identity: descriptor: %s is empty", member)
		}
		if strings.ContainsRune(name, 0) {
			return Descriptor{}, fmt.Errorf("identity: descriptor: %s holds a NUL", member)
		}
	}
	return d, nil
}
