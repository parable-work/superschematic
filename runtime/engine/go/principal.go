package engine

import "encoding/json"

// Principal is who a call acts for, in the HTTP runtime's shape: the
// subject the call records as its actor, the permissions it holds, its
// verified claims, and, for a call a service makes (D37), the service.
type Principal struct {
	Subject     string            `json:"subject"`
	Permissions []string          `json:"permissions"`
	Claims      map[string]any    `json:"claims,omitempty"`
	Service     *PrincipalService `json:"service,omitempty"`
}

// MarshalJSON writes no permissions as an empty list, which the engine
// requires.
func (p Principal) MarshalJSON() ([]byte, error) {
	type plain Principal
	if p.Permissions == nil {
		p.Permissions = []string{}
	}
	return json.Marshal(plain(p))
}

// PrincipalService is the service that made a call (D37): beside the end
// user it acts for, or standing in for one.
type PrincipalService struct {
	Deployable string   `json:"deployable"`
	Serves     []string `json:"serves"`
	Subject    string   `json:"subject"`
	StandsIn   bool     `json:"standsIn"`
}

// MarshalJSON writes no services served as an empty list.
func (s PrincipalService) MarshalJSON() ([]byte, error) {
	type plain PrincipalService
	if s.Serves == nil {
		s.Serves = []string{}
	}
	return json.Marshal(plain(s))
}
