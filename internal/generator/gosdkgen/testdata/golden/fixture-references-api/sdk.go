package sdk

import (
	"example.com/schemas/sdk/go/fixture-references-api/namespaces"
	"fmt"
)

// FixtureReferencesApiSDK is the root client for fixture-references-api endpoints.
type FixtureReferencesApiSDK struct {
	client *HTTPClient
	// NoteNamespace exposes note namespace operations.
	//
	// Endpoints:
	// - AddReference: Makes the row point at another row, of any table it may point at, with a role (none unless given), once per target and role; answers the reference, the one there when it was there. Requires authentication.
	// - Attachments: Lists the row's ready files, oldest first. Requires authentication.
	// - Backlinks: Lists the rows that point at the row, across every table, through a reference or a ref block of a message, each one the caller can read, most recent first. Only the owner of a row whose audience is not everyone hears how many rows it cannot see point at it. Requires authentication.
	// - BeginUpload: Starts an upload of a file to the row: a pending file, held to the table's bounds with the row's other files and uploads, and the URL the browser puts the file's bytes to, straight to the bucket, before it expires. Requires authentication.
	// - CompleteUpload: Checks an upload's bytes in the bucket against the size and SHA-256 its begin declared, and makes the file ready; a file whose bytes differ is removed. Requires authentication.
	// - Download: Signs a URL that downloads one of the row's files until it expires, and answers the file, whose preview this first view asks. Requires authentication.
	// - FileAttachment: Files one of the row's files onto another row, which may be of another table, with no second upload: a copy that names the same object, or with move the file itself. When the table keeps references that may point at the other row, the row then points at it with the role (filed unless given), so the other row lists it among its backlinks. Requires authentication.
	// - References: Lists the references the row holds, oldest first, each to a row the caller can read. Requires authentication.
	// - RemoveReference: Stops the row pointing at another row with the role, or with any role when none is given; answers how many references it removed. Requires authentication.
	NoteNamespace *namespaces.NoteNamespace
	// TicketNamespace exposes ticket namespace operations.
	//
	// Endpoints:
	// - AddReference: Makes the row point at another row, of any table it may point at, with a role (none unless given), once per target and role; answers the reference, the one there when it was there. Requires authentication.
	// - Attachments: Lists the row's ready files, oldest first. Requires authentication.
	// - Backlinks: Lists the rows that point at the row, across every table, through a reference or a ref block of a message, each one the caller can read, most recent first. Only the owner of a row whose audience is not everyone hears how many rows it cannot see point at it. Requires authentication.
	// - BeginUpload: Starts an upload of a file to the row: a pending file, held to the table's bounds with the row's other files and uploads, and the URL the browser puts the file's bytes to, straight to the bucket, before it expires. Requires authentication.
	// - CompleteUpload: Checks an upload's bytes in the bucket against the size and SHA-256 its begin declared, and makes the file ready; a file whose bytes differ is removed. Requires authentication.
	// - Download: Signs a URL that downloads one of the row's files until it expires, and answers the file, whose preview this first view asks. Requires authentication.
	// - FileAttachment: Files one of the row's files onto another row, which may be of another table, with no second upload: a copy that names the same object, or with move the file itself. When the table keeps references that may point at the other row, the row then points at it with the role (filed unless given), so the other row lists it among its backlinks. Requires authentication.
	// - GetTicket: GetTicket endpoint. Requires authentication.
	// - References: Lists the references the row holds, oldest first, each to a row the caller can read. Requires authentication.
	// - RemoveAttachment: Removes one of the row's files; its object is released once no file names it. Requires authentication.
	// - RemoveReference: Stops the row pointing at another row with the role, or with any role when none is given; answers how many references it removed. Requires authentication.
	TicketNamespace *namespaces.TicketNamespace
}

// New creates a new FixtureReferencesApiSDK.
func New(config SDKConfig) (*FixtureReferencesApiSDK, error) {
	if config.BaseURL == "" {
		return nil, fmt.Errorf("base URL is required")
	}

	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}
	sdk := &FixtureReferencesApiSDK{
		client:          client,
		NoteNamespace:   namespaces.NewNoteNamespace(client),
		TicketNamespace: namespaces.NewTicketNamespace(client),
	}
	return sdk, nil
}

// SetToken sets the static auth token used by the SDK client.
func (s *FixtureReferencesApiSDK) SetToken(token string) {
	s.client.SetToken(token)
}

// ClearToken removes the static auth token used by the SDK client.
func (s *FixtureReferencesApiSDK) ClearToken() {
	s.client.ClearToken()
}

// SetPublicEncryptionKey sets the default key for encrypted requests.
func (s *FixtureReferencesApiSDK) SetPublicEncryptionKey(key *PublicEncryptionKey) {
	s.client.SetPublicEncryptionKey(key)
}
