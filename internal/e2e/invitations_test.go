package e2e_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

var invitationRe = regexp.MustCompile(`action="/invitations/accept"><input type="hidden" name="csrf" value="[^"]+"><input type="hidden" name="id" value="([0-9a-f-]{36})"`)

// acceptInvitations accepts every invitation waiting for the signed-in user,
// as they would on the invitations page.
func (s *stack) acceptInvitations() {
	s.t.Helper()
	_, page := s.do("GET", "/workspaces/new", nil)
	csrf := find(s.t, csrfRe, page)
	for _, m := range invitationRe.FindAllStringSubmatch(page, -1) {
		if res, _ := s.do("POST", "/invitations/accept", url.Values{"csrf": {csrf}, "id": {m[1]}}); res.StatusCode != http.StatusSeeOther {
			s.t.Fatalf("accept invitation: %d", res.StatusCode)
		}
	}
}

// An admin cannot pull someone into a workspace, nor claim another company's
// domain: joining always needs the person's acceptance.
func TestInvitationsNeedAcceptance(t *testing.T) {
	s := start(t)
	csrf := s.signIn() // root@acme.test owns "Acme"
	if res, _ := s.do("POST", "/admin/domain", url.Values{"csrf": {csrf}, "domain": {"acme.test"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("allow acme.test: %d", res.StatusCode)
	}

	// Ana already has her own workspace when the Acme owner invites her.
	_, _ = s.do("POST", "/logout", url.Values{"csrf": {csrf}})
	s.devLogin("ana@acme.test", "/")
	_, page := s.do("GET", "/workspaces/new", nil)
	anaCSRF := find(t, csrfRe, page)
	s.do("POST", "/workspaces", url.Values{"csrf": {anaCSRF}, "name": {"Ana Ventas"}})
	// As owner (not superadmin), Ana may claim her own email's domain only.
	if res, _ := s.do("POST", "/settings/domain", url.Values{"csrf": {anaCSRF}, "domain": {"other.test"}, "role": {"member"}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("claimed a foreign domain: %d", res.StatusCode)
	}
	if res, _ := s.do("POST", "/settings/domain", url.Values{"csrf": {anaCSRF}, "domain": {"acme.test"}, "role": {"member"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("claim own domain: %d", res.StatusCode)
	}
	_, _ = s.do("POST", "/logout", url.Values{"csrf": {anaCSRF}})

	s.devLogin("root@acme.test", "/")
	_, page = s.do("GET", "/settings/members", nil)
	csrf = find(t, csrfRe, page)
	if res, _ := s.do("POST", "/settings/invite", url.Values{"csrf": {csrf}, "email": {"ana@acme.test"}, "role": {"member"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("invite: %d", res.StatusCode)
	}
	_, _ = s.do("POST", "/logout", url.Values{"csrf": {csrf}})

	// Ana is not a member yet: her own workspace stays the active one and she
	// is told about the invitation.
	s.devLogin("ana@acme.test", "/")
	_, page = s.do("GET", "/board", nil)
	if strings.Contains(page, `>Acme · `) || !strings.Contains(page, "Tenés invitaciones a otros workspaces.") {
		t.Fatalf("invitation joined without acceptance, or no notice:\n%s", page)
	}
	_, page = s.do("GET", "/workspaces/new", nil)
	ids := invitationRe.FindAllStringSubmatch(page, -1)
	if len(ids) != 1 || !strings.Contains(page, "root@acme.test") {
		t.Fatalf("invitations page: %d invitations\n%s", len(ids), page)
	}
	anaCSRF = find(t, csrfRe, page)
	// Accepting needs the session's CSRF token.
	if res, _ := s.do("POST", "/invitations/accept", url.Values{"id": {ids[0][1]}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("accept without CSRF: %d", res.StatusCode)
	}
	s.acceptInvitations()
	_, page = s.do("GET", "/board", nil)
	if !strings.Contains(page, ">Acme · ") {
		t.Fatalf("accepted invitation: Acme not listed\n%s", page)
	}
	// An invitation cannot be accepted twice, nor someone else's.
	if res, _ := s.do("POST", "/invitations/accept", url.Values{"csrf": {anaCSRF}, "id": {ids[0][1]}}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("second accept: %d", res.StatusCode)
	}
	if res, _ := s.do("POST", "/invitations/decline", url.Values{"csrf": {anaCSRF}, "id": {"not-an-id"}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("decline with a bad id: %d", res.StatusCode)
	}
}
