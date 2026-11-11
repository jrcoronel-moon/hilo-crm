package crm

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/safehttp"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Service implements every CRM operation. Methods take the Actor first and
// check its permission before touching data.
type Service struct {
	db  *db.DB
	log *slog.Logger
	now func() time.Time

	master []byte       // SESSION_SECRET, for secrets sealed in the database
	img    *http.Client // fetches record pictures (safehttp: public https only)
}

// New builds the service.
func New(d *db.DB, log *slog.Logger) *Service {
	return &Service{db: d, log: log, now: time.Now, img: safehttp.Client(avatarTimeout, 3)}
}

// tx runs fn in the actor's workspace after checking permission p.
func (s *Service) tx(ctx context.Context, a Actor, p Permission, fn func(q *store.Queries) error) error {
	if err := a.require(p); err != nil {
		return err
	}
	return mapErr(s.db.InWorkspace(ctx, a.WorkspaceID, fn))
}

// audit appends one entry to the audit log inside the current transaction.
func (s *Service) audit(ctx context.Context, q *store.Queries, a Actor, action, entity string, id uuid.UUID, details map[string]any) error {
	b, err := json.Marshal(details)
	if err != nil || details == nil {
		b = []byte(`{}`)
	}
	var idPtr *uuid.UUID
	if id != uuid.Nil {
		idPtr = &id
	}
	ws := a.WorkspaceID
	return q.InsertAudit(ctx, store.InsertAuditParams{
		WorkspaceID: &ws, ActorUserID: a.userPtr(), ActorKind: a.Kind, ActorClient: truncate(a.Client, 200),
		Action: action, EntityType: entity, EntityID: idPtr, Details: b,
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// mapErr turns database errors into service errors whose messages are safe
// to return to callers. Anything unexpected becomes a generic error (the
// caller logs the original).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505": // unique_violation
			return conflictFor(pg.ConstraintName)
		case "23503": // foreign_key_violation
			return &ValidationError{Field: "reference", Msg: "points to a record that does not exist in this workspace"}
		case "23514": // check_violation
			if pg.ConstraintName == "pipeline_shape" {
				return &ValidationError{Field: "stages", Msg: "a pipeline needs exactly 1 initial stage and at least 1 in_progress, 1 won and 1 lost stage"}
			}
			return &ValidationError{Field: "input", Msg: "violates a data constraint (" + pg.ConstraintName + ")"}
		case "22001": // string_data_right_truncation
			return &ValidationError{Field: "input", Msg: "value too long"}
		}
	}
	return err
}

var conflictFields = map[string]string{
	"companies_domain_key":       "a company with this domain already exists",
	"contacts_email_key":         "a contact with this email already exists",
	"companies_external_key":     "a company with this external_id already exists",
	"contacts_external_key":      "a contact with this external_id already exists",
	"opportunities_external_key": "an opportunity with this external_id already exists",
	"interactions_external_key":  "an interaction with this external_id already exists",
	"pipelines_name_key":         "a pipeline with this name already exists",
	"pipeline_stages_name_key":   "stage names must be unique within a pipeline",
	"channels_name_key":          "a channel with this name already exists",
}

func conflictFor(constraint string) error {
	if msg, ok := conflictFields[constraint]; ok {
		return &conflictError{msg: msg}
	}
	return ErrConflict
}

type conflictError struct{ msg string }

func (e *conflictError) Error() string { return e.msg }
func (e *conflictError) Unwrap() error { return ErrConflict }

// notFound converts pgx.ErrNoRows into a named not-found error.
func notFound(what string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return &notFoundError{what: what}
	}
	return err
}

type notFoundError struct{ what string }

func (e *notFoundError) Error() string { return e.what + " not found" }
func (e *notFoundError) Unwrap() error { return ErrNotFound }
