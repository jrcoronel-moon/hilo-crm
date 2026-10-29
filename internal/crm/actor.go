// Package crm is the single home of CRM business rules. The MCP tools, the
// web UI and the public lead forms all call this package; none of them talks
// to the CRM tables directly, so every rule (validation, permissions, pipeline
// semantics, audit) is implemented and tested exactly once.
package crm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Permission is what an operation requires.
type Permission string

// Permissions, from weakest to strongest.
const (
	PermRead  Permission = "crm:read"
	PermWrite Permission = "crm:write"
	PermAdmin Permission = "crm:admin"
)

// AllScopes is the full OAuth/PAT scope string.
const AllScopes = "crm:read crm:write crm:admin"

// rolePerms maps a workspace role to what it may do. A token's scopes can only
// narrow this, never widen it.
var rolePerms = map[string][]Permission{
	"owner":  {PermRead, PermWrite, PermAdmin},
	"admin":  {PermRead, PermWrite, PermAdmin},
	"member": {PermRead, PermWrite},
	"viewer": {PermRead},
}

// ValidRole reports whether r is a workspace role.
func ValidRole(r string) bool { _, ok := rolePerms[r]; return ok }

// Actor is who is calling: the user, the workspace they act in, and through
// what (web session, an OAuth client such as Claude, a PAT, a public form).
type Actor struct {
	UserID      uuid.UUID // uuid.Nil for public forms
	WorkspaceID uuid.UUID
	Role        string
	Kind        string // session | oauth | pat | form | system
	Client      string // OAuth client or PAT name, for the audit log
	perms       map[Permission]bool
}

// NewActor computes the effective permissions: role ∩ scopes. An empty scope
// string means "whatever the role allows" (web sessions).
func NewActor(userID, workspaceID uuid.UUID, role, kind, client, scopes string) Actor {
	a := Actor{UserID: userID, WorkspaceID: workspaceID, Role: role, Kind: kind, Client: client, perms: map[Permission]bool{}}
	granted := map[Permission]bool{}
	for _, s := range strings.Fields(scopes) {
		granted[Permission(s)] = true
	}
	for _, p := range rolePerms[role] {
		if scopes == "" || granted[p] {
			a.perms[p] = true
		}
	}
	return a
}

// FormActor is the actor for anonymous lead-form submissions: it may only
// create leads, which the forms package does through CaptureLead.
func FormActor(workspaceID uuid.UUID, formName string) Actor {
	return Actor{WorkspaceID: workspaceID, Kind: "form", Client: formName, perms: map[Permission]bool{}}
}

// Can reports whether the actor holds p.
func (a Actor) Can(p Permission) bool { return a.perms[p] }

func (a Actor) require(p Permission) error {
	if a.WorkspaceID == uuid.Nil || !a.perms[p] {
		return fmt.Errorf("%w: requires %s", ErrForbidden, p)
	}
	return nil
}

func (a Actor) userPtr() *uuid.UUID {
	if a.UserID == uuid.Nil {
		return nil
	}
	id := a.UserID
	return &id
}

// Errors returned by the service. Callers map them to HTTP statuses / MCP
// tool errors; their messages are safe to show to the caller.
var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
	ErrConflict  = errors.New("conflict")
)

// ValidationError describes one invalid input field.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }

func invalid(field, format string, args ...any) error {
	return &ValidationError{Field: field, Msg: fmt.Sprintf(format, args...)}
}
