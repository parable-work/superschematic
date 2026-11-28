package namespaces

import (
	"context"
	"fmt"

	types "example.com/schemas/types/go/fixture-references-api"
)

// TicketNamespace contains API methods for the ticket namespace.
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
type TicketNamespace struct {
	client jsonClient
}

// NewTicketNamespace creates a TicketNamespace.
func NewTicketNamespace(
	client jsonClient,
) *TicketNamespace {
	return &TicketNamespace{
		client: client,
	}
}

// AddReference Makes the row point at another row, of any table it may point at, with a role (none unless given), once per target and role; answers the reference, the one there when it was there.
// Requires authentication.
func (n *TicketNamespace) AddReference(
	ctx context.Context,
	Id string,
	input types.RowReferenceInput,
) (types.RowReference, error) {
	path := fmt.Sprintf("/api/ticket/%v/references", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.RowReference
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.RowReference
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RowReference
		return zero, err
	}
	return out, nil
}

// Attachments Lists the row's ready files, oldest first.
// Requires authentication.
func (n *TicketNamespace) Attachments(
	ctx context.Context,
	Id string,
) ([]types.RowAttachment, error) {
	path := fmt.Sprintf("/api/ticket/%v/files", pathSegment(Id))

	var requestBody any
	var out []types.RowAttachment
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero []types.RowAttachment
		return zero, err
	}
	return out, nil
}

// Backlinks Lists the rows that point at the row, across every table, through a reference or a ref block of a message, each one the caller can read, most recent first. Only the owner of a row whose audience is not everyone hears how many rows it cannot see point at it.
// Requires authentication.
func (n *TicketNamespace) Backlinks(
	ctx context.Context,
	Id string,
) (types.RowBacklinks, error) {
	path := fmt.Sprintf("/api/ticket/%v/backlinks", pathSegment(Id))

	var requestBody any
	var out types.RowBacklinks
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RowBacklinks
		return zero, err
	}
	return out, nil
}

// BeginUpload Starts an upload of a file to the row: a pending file, held to the table's bounds with the row's other files and uploads, and the URL the browser puts the file's bytes to, straight to the bucket, before it expires.
// Requires authentication.
func (n *TicketNamespace) BeginUpload(
	ctx context.Context,
	Id string,
	input types.RowAttachmentBeginInput,
) (types.RowAttachmentBegun, error) {
	path := fmt.Sprintf("/api/ticket/%v/files", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.RowAttachmentBegun
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.RowAttachmentBegun
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RowAttachmentBegun
		return zero, err
	}
	return out, nil
}

// CompleteUpload Checks an upload's bytes in the bucket against the size and SHA-256 its begin declared, and makes the file ready; a file whose bytes differ is removed.
// Requires authentication.
func (n *TicketNamespace) CompleteUpload(
	ctx context.Context,
	Id string,
	File string,
) (types.RowAttachment, error) {
	path := fmt.Sprintf("/api/ticket/%v/files/%v/complete", pathSegment(Id), pathSegment(File))

	var requestBody any
	var out types.RowAttachment
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RowAttachment
		return zero, err
	}
	return out, nil
}

// Download Signs a URL that downloads one of the row's files until it expires, and answers the file, whose preview this first view asks.
// Requires authentication.
func (n *TicketNamespace) Download(
	ctx context.Context,
	Id string,
	File string,
) (types.RowAttachmentDownload, error) {
	path := fmt.Sprintf("/api/ticket/%v/files/%v", pathSegment(Id), pathSegment(File))

	var requestBody any
	var out types.RowAttachmentDownload
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RowAttachmentDownload
		return zero, err
	}
	return out, nil
}

// FileAttachment Files one of the row's files onto another row, which may be of another table, with no second upload: a copy that names the same object, or with move the file itself. When the table keeps references that may point at the other row, the row then points at it with the role (filed unless given), so the other row lists it among its backlinks.
// Requires authentication.
func (n *TicketNamespace) FileAttachment(
	ctx context.Context,
	Id string,
	File string,
	input types.RowAttachmentFileInput,
) (types.RowAttachment, error) {
	path := fmt.Sprintf("/api/ticket/%v/files/%v/file", pathSegment(Id), pathSegment(File))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.RowAttachment
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.RowAttachment
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RowAttachment
		return zero, err
	}
	return out, nil
}

// GetTicket calls the GET /api/tickets/%v endpoint.
// Requires authentication.
func (n *TicketNamespace) GetTicket(
	ctx context.Context,
	Id string,
) (types.Ticket, error) {
	path := fmt.Sprintf("/api/tickets/%v", pathSegment(Id))

	var requestBody any
	var out types.Ticket
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.Ticket
		return zero, err
	}
	return out, nil
}

// References Lists the references the row holds, oldest first, each to a row the caller can read.
// Requires authentication.
func (n *TicketNamespace) References(
	ctx context.Context,
	Id string,
) ([]types.RowReference, error) {
	path := fmt.Sprintf("/api/ticket/%v/references", pathSegment(Id))

	var requestBody any
	var out []types.RowReference
	if err := n.client.DoJSON(
		ctx,
		"GET",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero []types.RowReference
		return zero, err
	}
	return out, nil
}

// RemoveAttachment Removes one of the row's files; its object is released once no file names it.
// Requires authentication.
func (n *TicketNamespace) RemoveAttachment(
	ctx context.Context,
	Id string,
	File string,
) (bool, error) {
	path := fmt.Sprintf("/api/ticket/%v/files/%v", pathSegment(Id), pathSegment(File))

	var requestBody any
	var out bool
	if err := n.client.DoJSON(
		ctx,
		"DELETE",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero bool
		return zero, err
	}
	return out, nil
}

// RemoveReference Stops the row pointing at another row with the role, or with any role when none is given; answers how many references it removed.
// Requires authentication.
func (n *TicketNamespace) RemoveReference(
	ctx context.Context,
	Id string,
	input types.RowReferenceInput,
) (types.RowReferencesRemoved, error) {
	path := fmt.Sprintf("/api/ticket/%v/references/remove", pathSegment(Id))
	validationErrors := types.NewValidationErrors()
	appendInputValidationErrors(validationErrors, any(&input))
	if validationErrors.HasErrors() {
		var zero types.RowReferencesRemoved
		return zero, NewValidationError(validationErrors)
	}

	var requestBody any
	requestBody = input
	var out types.RowReferencesRemoved
	if err := n.client.DoJSON(
		ctx,
		"POST",
		path,
		nil,
		requestBody,
		&out,
	); err != nil {
		var zero types.RowReferencesRemoved
		return zero, err
	}
	return out, nil
}
