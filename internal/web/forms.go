package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/jrcoronel-moon/hilo-crm/internal/captcha"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
)

// Public lead-form endpoint: POST /f/{key}, urlencoded or multipart. Websites
// post here directly (a plain HTML form) or with fetch from an allowed origin.
// Protections: unguessable key, per-IP rate limit, small body limit, honeypot
// field, origin allow-list, an optional Cloudflare Turnstile captcha, and the
// "never overwrite" rule in CaptureLead.

const formMaxBody = 32 << 10

var formKnown = map[string]bool{"email": true, "first_name": true, "last_name": true, "name": true, "company": true,
	"phone": true, "message": true, "website": true, "_redirect": true, captcha.Field: true}

// captchaVerifier checks a Turnstile token with the workspace's secret.
type captchaVerifier interface {
	Verify(ctx context.Context, secret, token, remoteIP string) (captcha.Result, error)
}

func (h *Handler) submitForm(w http.ResponseWriter, r *http.Request) {
	if !h.formLimit.Allow(h.ips.Key(r)) {
		httpx.WriteError(w, http.StatusTooManyRequests, "too many submissions, try again later")
		return
	}
	form, err := h.crm.LookupForm(r.Context(), r.PathValue("key"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "form not found")
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" && len(form.AllowedOrigins) > 0 && !slices.Contains(form.AllowedOrigins, origin) {
		httpx.WriteError(w, http.StatusForbidden, "origin not allowed for this form")
		return
	}
	if origin != "" && slices.Contains(form.AllowedOrigins, origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	r.Body = http.MaxBytesReader(w, r.Body, formMaxBody)
	if err := r.ParseMultipartForm(formMaxBody); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid form body")
		return
	}
	// Honeypot: humans never fill the hidden "website" field. Answer as if it
	// worked so bots learn nothing.
	if r.PostFormValue("website") != "" {
		h.formDone(w, r, form)
		return
	}
	if form.RequireCaptcha && !h.checkCaptcha(w, r, form) {
		return
	}
	sub := crm.LeadSubmission{
		Email: r.PostFormValue("email"), FirstName: r.PostFormValue("first_name"), LastName: r.PostFormValue("last_name"),
		Company: r.PostFormValue("company"), Phone: r.PostFormValue("phone"), Message: r.PostFormValue("message"),
		Extra: map[string]string{},
	}
	if sub.FirstName == "" && sub.LastName == "" {
		sub.FirstName, sub.LastName, _ = strings.Cut(strings.TrimSpace(r.PostFormValue("name")), " ")
	}
	for k, vals := range r.PostForm {
		if !formKnown[k] && len(vals) > 0 && len(sub.Extra) < 20 {
			sub.Extra[k] = vals[0]
		}
	}
	if _, err := h.crm.CaptureLead(r.Context(), form, sub); err != nil {
		var ve *crm.ValidationError
		if errors.As(err, &ve) {
			httpx.WriteError(w, http.StatusBadRequest, ve.Error())
			return
		}
		h.log.Error("lead capture", "form_id", form.ID, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "could not save the submission")
		return
	}
	h.formDone(w, r, form)
}

// checkCaptcha verifies the Turnstile token and, when the form has an origin
// allow-list, that the widget was solved on one of those sites. It writes the
// error response itself and reports whether the submission may go on.
func (h *Handler) checkCaptcha(w http.ResponseWriter, r *http.Request, form crm.FormInfo) bool {
	if form.CaptchaSecret == "" { // keys removed or unreadable: logged by LookupForm
		httpx.WriteError(w, http.StatusServiceUnavailable, "captcha is not configured for this form")
		return false
	}
	res, err := h.captcha.Verify(r.Context(), form.CaptchaSecret, r.PostFormValue(captcha.Field), h.ips.Of(r))
	switch {
	case errors.Is(err, captcha.ErrMissing):
		httpx.WriteError(w, http.StatusBadRequest, "complete the captcha and send the form again")
		return false
	case errors.Is(err, captcha.ErrRejected):
		httpx.WriteError(w, http.StatusForbidden, "captcha verification failed, try again")
		return false
	case err != nil:
		h.log.Error("captcha verify", "form_id", form.ID, "err", err)
		httpx.WriteError(w, http.StatusServiceUnavailable, "could not verify the captcha, try again later")
		return false
	}
	if len(form.AllowedOrigins) > 0 && !slices.ContainsFunc(form.AllowedOrigins, func(o string) bool {
		u, err := url.Parse(o)
		return err == nil && strings.EqualFold(u.Hostname(), res.Hostname)
	}) {
		httpx.WriteError(w, http.StatusForbidden, "captcha was solved on a site not allowed for this form")
		return false
	}
	return true
}

// formDone redirects plain HTML posts to the form's configured thank-you URL
// (never to a URL supplied by the submitter) and answers JSON otherwise.
func (h *Handler) formDone(w http.ResponseWriter, r *http.Request, form crm.FormInfo) {
	if form.RedirectURL != "" && !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Redirect(w, r, form.RedirectURL, http.StatusSeeOther)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handler) formPreflight(w http.ResponseWriter, r *http.Request) {
	form, err := h.crm.LookupForm(r.Context(), r.PathValue("key"))
	origin := r.Header.Get("Origin")
	if err == nil && origin != "" && slices.Contains(form.AllowedOrigins, origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept")
		w.Header().Set("Vary", "Origin")
	}
	w.WriteHeader(http.StatusNoContent)
}
