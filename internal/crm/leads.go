package crm

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// LeadSubmission is what a public form posts. Everything in it is untrusted.
type LeadSubmission struct {
	Email     string
	FirstName string
	LastName  string
	Company   string
	Phone     string
	Message   string
	Extra     map[string]string // any other form fields
}

// FormInfo is what the public endpoint needs to know about a form.
type FormInfo struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	Name           string
	AllowedOrigins []string
	RedirectURL    string
	RequireCaptcha bool
	CaptchaSecret  string // opened Turnstile secret when RequireCaptcha ("" if unavailable)
}

// LookupForm finds an active form by its public key.
func (s *Service) LookupForm(ctx context.Context, publicKey string) (FormInfo, error) {
	var f FormInfo
	err := s.db.InFormLookup(ctx, publicKey, func(q *store.Queries) error {
		row, err := q.LookupLeadForm(ctx, publicKey)
		if err != nil {
			return notFound("form", err)
		}
		f = FormInfo{ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name, AllowedOrigins: row.AllowedOrigins, RedirectURL: row.RedirectUrl,
			RequireCaptcha: row.RequireCaptcha}
		if row.RequireCaptcha {
			if secret, err := s.formCaptchaSecret(ctx, q, row.WorkspaceID); err == nil {
				f.CaptchaSecret = secret
			} else {
				s.log.Error("lead form requires a captcha but the workspace keys are unavailable", "form_id", row.ID, "err", err)
			}
		}
		return nil
	})
	return f, err
}

// freemail domains never become companies.
var freemail = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "outlook.com": true, "hotmail.com": true, "live.com": true,
	"yahoo.com": true, "icloud.com": true, "me.com": true, "proton.me": true, "protonmail.com": true,
	"aol.com": true, "gmx.com": true, "yandex.com": true, "mail.com": true,
}

// CaptureLead turns a form submission into contact + opportunity + inbound
// interaction. An anonymous submitter can never change existing data: an
// existing contact or company is only linked (and only empty fields filled),
// never overwritten.
func (s *Service) CaptureLead(ctx context.Context, f FormInfo, sub LeadSubmission) (uuid.UUID, error) {
	email, err := NormalizeEmail("email", sub.Email)
	if err != nil || email == "" {
		return uuid.Nil, invalid("email", "a valid email is required")
	}
	first, err1 := cleanLine("first_name", sub.FirstName, 100)
	last, err2 := cleanLine("last_name", sub.LastName, 100)
	company, err3 := cleanLine("company", sub.Company, 200)
	phone, err4 := cleanLine("phone", sub.Phone, 40)
	msg, err5 := cleanText("message", sub.Message, 5000)
	for _, e := range []error{err1, err2, err3, err4, err5} {
		if e != nil {
			return uuid.Nil, e
		}
	}
	a := FormActor(f.WorkspaceID, f.Name)
	var oppID uuid.UUID
	err = mapErr(s.db.InWorkspace(ctx, f.WorkspaceID, func(q *store.Queries) error {
		form, err := q.GetLeadForm(ctx, store.GetLeadFormParams{WorkspaceID: f.WorkspaceID, ID: f.ID})
		if err != nil {
			return err
		}
		channel := form.ChannelID.String()
		companyID, err := s.leadCompany(ctx, q, f.WorkspaceID, email, company, channel)
		if err != nil {
			return err
		}
		contactID, err := s.leadContact(ctx, q, f.WorkspaceID, email, first, last, phone, companyID, channel)
		if err != nil {
			return err
		}
		title := strings.TrimSpace(first + " " + last)
		if company != "" {
			title = strings.TrimSpace(company + " · " + title)
		}
		if title == "" {
			title = email
		}
		pipe := form.PipelineID.String()
		cid := contactID.String()
		in := OpportunityInput{Title: ptr("Lead: " + title), Pipeline: &pipe, SourceChannel: &channel, PrimaryContact: &cid}
		if companyID != nil {
			cs := companyID.String()
			in.Company = &cs
		}
		w, err := s.upsertOpportunity(ctx, q, a, in)
		if err != nil {
			return err
		}
		o, st := w.o, w.stage
		oppID = o.ID
		if err := s.recordMove(ctx, q, a, o.ID, w.move); err != nil {
			return err
		}
		_, _, err = s.logInteraction(ctx, q, a, InteractionInput{
			Type: "form", Direction: "inbound", Channel: channel, Subject: "Form: " + f.Name,
			Summary: leadSummary(msg, sub.Extra), Contact: cid, Opportunity: o.ID.String(),
		})
		if err != nil {
			return err
		}
		return s.audit(ctx, q, a, "lead.capture", "opportunity", o.ID, map[string]any{"form": f.Name, "stage": st.Name})
	}))
	return oppID, err
}

func (s *Service) leadCompany(ctx context.Context, q *store.Queries, ws uuid.UUID, email, name, channel string) (*uuid.UUID, error) {
	domain := EmailDomain(email)
	if freemail[domain] {
		if name == "" {
			return nil, nil
		}
		domain = "" // a named company with a personal email: create without domain
	}
	if domain != "" {
		c, err := q.GetCompanyByDomain(ctx, store.GetCompanyByDomainParams{WorkspaceID: ws, Domain: &domain})
		if err == nil {
			return &c.ID, nil
		} else if !isNoRows(err) {
			return nil, err
		}
	}
	if name == "" {
		name = domain
	}
	in := CompanyInput{Name: &name, SourceChannel: &channel}
	if domain != "" {
		in.Domain = &domain
	}
	c, _, err := upsertCompany(ctx, q, ws, in)
	if err != nil {
		return nil, err
	}
	return &c.ID, nil
}

func (s *Service) leadContact(ctx context.Context, q *store.Queries, ws uuid.UUID, email, first, last, phone string, companyID *uuid.UUID, channel string) (uuid.UUID, error) {
	existing, err := q.GetContactByEmail(ctx, store.GetContactByEmailParams{WorkspaceID: ws, Email: &email})
	if err != nil && !isNoRows(err) {
		return uuid.Nil, err
	}
	in := ContactInput{Email: &email}
	if err == nil {
		// Only fill what is empty; never overwrite.
		in.ID = ptr(existing.ID.String())
		in.Email = nil
		fillIfEmpty(&in.FirstName, existing.FirstName, first)
		fillIfEmpty(&in.LastName, existing.LastName, last)
		fillIfEmpty(&in.Phone, existing.Phone, phone)
		if existing.CompanyID == nil && companyID != nil {
			in.Company = ptr(companyID.String())
		}
	} else {
		in.FirstName, in.LastName, in.Phone, in.SourceChannel = &first, &last, &phone, &channel
		if companyID != nil {
			in.Company = ptr(companyID.String())
		}
	}
	c, _, err := upsertContact(ctx, q, ws, in)
	return c.ID, err
}

func fillIfEmpty(dst **string, current, v string) {
	if current == "" && v != "" {
		*dst = &v
	}
}

// leadSummary renders the message and extra fields as plain text.
func leadSummary(msg string, extra map[string]string) string {
	var b strings.Builder
	b.WriteString(msg)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i >= 20 {
			break
		}
		kk, _ := cleanLine("field", k, 64)
		vv, _ := cleanLine("value", extra[k], 500)
		if kk != "" && vv != "" {
			fmt.Fprintf(&b, "\n%s: %s", kk, vv)
		}
	}
	return truncate(strings.TrimSpace(b.String()), 10000)
}

func ptr[T any](v T) *T { return &v }

// LeadFormView is a lead form for the admin UI.
type LeadFormView = store.LeadForm

// LeadFormInput creates a public lead form (admin).
type LeadFormInput struct {
	Name           string   `json:"name"`
	Pipeline       string   `json:"pipeline,omitempty"`
	Channel        string   `json:"channel"`
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	RedirectURL    string   `json:"redirect_url,omitempty"`
	RequireCaptcha bool     `json:"require_captcha,omitempty"`
}

// CreateLeadForm creates a form with a fresh unguessable public key.
func (s *Service) CreateLeadForm(ctx context.Context, a Actor, in LeadFormInput) (LeadFormView, error) {
	name, err := requiredName("name", in.Name)
	if err != nil {
		return LeadFormView{}, err
	}
	redirect, err := cleanURL("redirect_url", in.RedirectURL)
	if err != nil {
		return LeadFormView{}, err
	}
	origins := make([]string, 0, len(in.AllowedOrigins))
	for _, o := range in.AllowedOrigins {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o == "" {
			continue
		}
		u, err := cleanURL("allowed_origins", o)
		if err != nil || strings.Count(u, "/") != 2 {
			return LeadFormView{}, invalid("allowed_origins", "each must be an origin like https://www.example.com")
		}
		origins = append(origins, u)
	}
	if len(origins) > 20 {
		return LeadFormView{}, invalid("allowed_origins", "at most 20")
	}
	var out LeadFormView
	err = s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		r := resolvers{ctx, q, a.WorkspaceID}
		pipe, err := resolvePipeline(ctx, q, a.WorkspaceID, in.Pipeline)
		if err != nil {
			return err
		}
		ch, err := r.channel(in.Channel)
		if err != nil {
			return err
		}
		if in.RequireCaptcha {
			if err := s.captchaReady(ctx, q, a.WorkspaceID); err != nil {
				return err
			}
		}
		out, err = q.CreateLeadForm(ctx, store.CreateLeadFormParams{
			WorkspaceID: a.WorkspaceID, Name: name, PublicKey: secure.Token("lf_", 24), PipelineID: pipe.ID,
			ChannelID: ch, AllowedOrigins: origins, RedirectUrl: redirect, RequireCaptcha: in.RequireCaptcha,
		})
		if err != nil {
			return err
		}
		return s.audit(ctx, q, a, "form.create", "form", out.ID, map[string]any{"name": name, "require_captcha": in.RequireCaptcha})
	})
	return out, err
}

// ListLeadForms lists the workspace's forms (admin).
func (s *Service) ListLeadForms(ctx context.Context, a Actor) ([]LeadFormView, error) {
	var out []LeadFormView
	err := s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		var err error
		out, err = q.ListLeadForms(ctx, a.WorkspaceID)
		return err
	})
	return out, err
}

// SetLeadFormActive enables or disables a form (admin).
func (s *Service) SetLeadFormActive(ctx context.Context, a Actor, id uuid.UUID, active bool) error {
	return s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		n, err := q.SetLeadFormActive(ctx, store.SetLeadFormActiveParams{WorkspaceID: a.WorkspaceID, ID: id, IsActive: active})
		if err != nil {
			return err
		}
		if n == 0 {
			return &notFoundError{what: "form"}
		}
		return s.audit(ctx, q, a, "form.update", "form", id, map[string]any{"active": active})
	})
}

// SetLeadFormCaptcha turns the Turnstile requirement on or off (admin).
func (s *Service) SetLeadFormCaptcha(ctx context.Context, a Actor, id uuid.UUID, required bool) error {
	return s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		if required {
			if err := s.captchaReady(ctx, q, a.WorkspaceID); err != nil {
				return err
			}
		}
		n, err := q.SetLeadFormCaptcha(ctx, store.SetLeadFormCaptchaParams{WorkspaceID: a.WorkspaceID, ID: id, RequireCaptcha: required})
		if err != nil {
			return err
		}
		if n == 0 {
			return &notFoundError{what: "form"}
		}
		return s.audit(ctx, q, a, "form.update", "form", id, map[string]any{"require_captcha": required})
	})
}
