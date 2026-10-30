package crm

import (
	"context"

	"github.com/google/uuid"
)

// ImportInput is a batch of records to create or update in one call. It is
// processed in dependency order (companies, contacts, opportunities,
// interactions), so later items may reference earlier ones by external_id.
type ImportInput struct {
	Companies     []CompanyInput     `json:"companies,omitempty"`
	Contacts      []ContactInput     `json:"contacts,omitempty"`
	Opportunities []OpportunityInput `json:"opportunities,omitempty"`
	Interactions  []InteractionInput `json:"interactions,omitempty"`
	DryRun        bool               `json:"dry_run,omitempty" jsonschema:"validate everything (references between items included) and report what would happen, without saving anything"`
}

// ImportItem is the outcome of one record of the batch.
type ImportItem struct {
	Type    string     `json:"type"`  // company | contact | opportunity | interaction
	Index   int        `json:"index"` // position in its list
	OK      bool       `json:"ok"`
	ID      *uuid.UUID `json:"id,omitempty"`
	Created bool       `json:"created,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// ImportResult summarises a batch.
type ImportResult struct {
	DryRun bool         `json:"dry_run"`
	OK     int          `json:"ok"`
	Failed int          `json:"failed"`
	Items  []ImportItem `json:"items"`
}

// MaxImport is the most records one batch may carry.
const MaxImport = 200

func (r *ImportResult) add(kind string, i int, id uuid.UUID, created bool, err error) {
	it := ImportItem{Type: kind, Index: i, OK: err == nil, Created: created && err == nil}
	if err != nil {
		it.Error = err.Error()
		r.Failed++
	} else {
		it.ID = &id
		r.OK++
	}
	r.Items = append(r.Items, it)
}

// Import creates or updates a batch of records in one transaction. Each record
// succeeds or fails on its own (a failure does not undo the others); with
// DryRun nothing is saved. Every record goes through the same upsert, rules
// and audit as a single call.
func (s *Service) Import(ctx context.Context, a Actor, in ImportInput) (ImportResult, error) {
	res := ImportResult{DryRun: in.DryRun, Items: []ImportItem{}}
	if err := a.require(PermWrite); err != nil {
		return res, err
	}
	n := len(in.Companies) + len(in.Contacts) + len(in.Opportunities) + len(in.Interactions)
	if n == 0 || n > MaxImport {
		return res, invalid("batch", "must carry between 1 and %d records", MaxImport)
	}
	err := s.db.Batch(ctx, a.WorkspaceID, !in.DryRun, func(ctx context.Context) error {
		for i, c := range in.Companies {
			v, created, err := s.UpsertCompany(ctx, a, c)
			res.add("company", i, v.ID, created, err)
		}
		for i, c := range in.Contacts {
			v, created, err := s.UpsertContact(ctx, a, c)
			res.add("contact", i, v.ID, created, err)
		}
		for i, o := range in.Opportunities {
			v, created, err := s.UpsertOpportunity(ctx, a, o)
			res.add("opportunity", i, v.ID, created, err)
		}
		for i, it := range in.Interactions {
			v, err := s.LogInteraction(ctx, a, it)
			res.add("interaction", i, v.ID, !v.Duplicate, err)
		}
		return nil
	})
	return res, mapErr(err)
}
