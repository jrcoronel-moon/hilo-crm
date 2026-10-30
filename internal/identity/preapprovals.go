package identity

import (
	"context"
	"net/url"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// IsClientIDURL reports whether a client_id is a Client ID Metadata Document
// URL: https with a path, on the default port, without credentials, query or
// fragment. Such a client's identity is proven by its domain.
func IsClientIDURL(clientID string) bool {
	u, err := url.Parse(clientID)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Port() == "" && u.User == nil &&
		u.Path != "" && u.Path != "/" && u.RawQuery == "" && u.Fragment == "" && len(clientID) <= 2000
}

// Preapprove lets an agent skip the consent screen in the caller's workspace
// (admin). Only clients identified by URL qualify, and the grant is at most
// read + write: crm:admin always needs the user's consent.
func (s *Service) Preapprove(ctx context.Context, c Caller, clientID string, readOnly bool) error {
	if err := c.requireAdmin(); err != nil {
		return err
	}
	if !IsClientIDURL(clientID) {
		return &crm.ValidationError{Field: "client_id", Msg: "must be the agent's https client metadata URL, as shown in Settings → Agents once it has connected"}
	}
	scope := string(crm.PermRead) + " " + string(crm.PermWrite)
	if readOnly {
		scope = string(crm.PermRead)
	}
	return s.db.Platform().Preapprove(ctx, store.PreapproveParams{WorkspaceID: c.WorkspaceID, ClientID: clientID, Scope: scope})
}

// Unpreapprove removes a preapproval (admin). Existing connections stay; revoke
// them separately.
func (s *Service) Unpreapprove(ctx context.Context, c Caller, clientID string) error {
	if err := c.requireAdmin(); err != nil {
		return err
	}
	n, err := s.db.Platform().Unpreapprove(ctx, store.UnpreapproveParams{WorkspaceID: c.WorkspaceID, ClientID: clientID})
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

// Preapprovals lists the agents trusted in the caller's workspace (admin).
func (s *Service) Preapprovals(ctx context.Context, c Caller) ([]store.ListPreapprovalsRow, error) {
	if err := c.requireAdmin(); err != nil {
		return nil, err
	}
	return s.db.Platform().ListPreapprovals(ctx, c.WorkspaceID)
}
