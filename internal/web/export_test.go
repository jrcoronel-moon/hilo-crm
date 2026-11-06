package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/importer"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

// TestExportCSVRoundTrip walks Ajustes → Exportar: each kind downloads as a
// CSV that Ajustes → Importar loads into another workspace unchanged; cells
// a spreadsheet would run are neutralised; members and other sites cannot
// export, and every export is audited.
func TestExportCSVRoundTrip(t *testing.T) {
	d := testdb.New(t)
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewUnstartedServer(nil)
	cfg := &config.Config{BaseURL: "http://" + ts.Listener.Addr().String(), SessionSecret: []byte(strings.Repeat("s", 40))}
	mux, err := Mount(t.Context(), cfg, d, log)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)

	ctx := context.Background()
	id := identity.New(d, log, map[string]bool{"root@acme.test": true})
	svc := crm.New(d, log)
	u, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: "r", Email: "root@acme.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	acme, err := id.CreateWorkspace(ctx, u.ID, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	token, err := id.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := id.ResolveSession(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, ctype string, body io.Reader) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, ts.URL+path, body)
		req.AddCookie(&http.Cookie{Name: "hilo_session", Value: token})
		req.Header.Set("Accept-Language", "es")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	export := func(kind, csrf string) (*http.Response, string) {
		t.Helper()
		form := url.Values{"csrf": {csrf}, "kind": {kind}}
		return do(http.MethodPost, "/settings/export", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	}

	owner := crm.NewActor(u.ID, acme.ID, "owner", "session", "", "")
	must := func(_ any, _ bool, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	custom := func(k, v string) *map[string]any { return &map[string]any{k: v} }
	must(svc.UpsertCompany(ctx, owner, crm.CompanyInput{Name: ptr("Globex"), Domain: ptr("globex.test"), Industry: ptr("Software"),
		Tags: &[]string{"vip", "saas"}, Custom: custom("segment", "=SUM(A1)")}))
	must(svc.UpsertCompany(ctx, owner, crm.CompanyInput{Name: ptr("Taller sin dominio"), ExternalID: ptr("hs:42"), Country: ptr("AR")}))
	must(svc.UpsertContact(ctx, owner, crm.ContactInput{FirstName: ptr("Ana"), LastName: ptr("López"), Email: ptr("ana@globex.test"),
		Phone: ptr("+54 11 5555-0000"), Company: ptr("globex.test"), Custom: custom("score", "alto")}))
	must(svc.UpsertContact(ctx, owner, crm.ContactInput{FirstName: ptr("Bob"), Company: ptr("hs:42"), Title: ptr("Dueño, socio")}))
	amount, prob := 1234.5, 40
	must(svc.UpsertOpportunity(ctx, owner, crm.OpportunityInput{Title: ptr("Renovación 2027"), Company: ptr("hs:42"),
		PrimaryContact: ptr("ana@globex.test"), Amount: &amount, Currency: ptr("USD"), Probability: &prob, ExpectedCloseDate: ptr("2027-01-31"),
		NextStep: ptr("Llamar"), Description: ptr("Dos líneas,\ncon \"comillas\""), Tags: &[]string{"renovación"}, Custom: custom("origen", "evento")}))

	if res, body := do(http.MethodGet, "/settings/export", "", nil); res.StatusCode != http.StatusOK || !strings.Contains(body, "Exportar a CSV") ||
		strings.Count(body, `action="/settings/export"`) != 3 {
		t.Fatalf("GET: %d %s", res.StatusCode, body)
	}
	files := map[string]string{}
	for _, kind := range crm.ExportKinds {
		res, body := export(kind, sess.CSRFToken)
		if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/csv") ||
			!strings.Contains(res.Header.Get("Content-Disposition"), "attachment; filename=\"hilo-") || !strings.HasPrefix(body, "\uFEFFexternal_id,") {
			t.Fatalf("export %s: %d %v %q", kind, res.StatusCode, res.Header, body)
		}
		files[kind] = body
	}
	for kind, want := range map[string][]string{
		crm.KindCompany:     {"custom:segment", "'=SUM(A1)", "hs:42", `"vip, saas"`},
		crm.KindContact:     {"'+54 11 5555-0000,,,globex.test,Globex,", "hs:42,Taller sin dominio", `"Dueño, socio"`, "custom:score"},
		crm.KindOpportunity: {"1234.5,USD,40,2027-01-31,hs:42,Taller sin dominio", "ana@globex.test,Ana López", "\"Dos líneas,\ncon \"\"comillas\"\"\"", "custom:origen"},
	} {
		for _, w := range want {
			if !strings.Contains(files[kind], w) {
				t.Errorf("%s export lacks %q:\n%s", kind, w, files[kind])
			}
		}
	}
	if audit, err := svc.ListAudit(ctx, owner, "", 10); err != nil || len(audit) < 3 || audit[0].Action != "opportunity.export" {
		t.Fatalf("audit: %+v %v", audit, err)
	}

	// Another site cannot trigger an export, and an unknown kind is refused.
	if res, _ := export(crm.KindContact, "forged"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("export without the CSRF token: %d", res.StatusCode)
	}
	if res, _ := export("interaction", sess.CSRFToken); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown kind: %d", res.StatusCode)
	}

	// The files load into a new workspace through Importar as they are: the
	// guessed mapping, no errors, and the same records on the other side.
	beta, err := id.CreateWorkspace(ctx, u.ID, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	if err := id.SwitchWorkspace(ctx, sess, beta.ID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range crm.ExportKinds {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("csrf", sess.CSRFToken)
		_ = mw.WriteField("kind", kind)
		fw, _ := mw.CreateFormFile("file", kind+".csv")
		_, _ = fw.Write([]byte(files[kind]))
		_ = mw.Close()
		res, body := do(http.MethodPost, "/settings/import", mw.FormDataContentType(), &buf)
		if res.StatusCode != http.StatusOK || !strings.Contains(body, "nada guardado") || strings.Contains(body, "rechazados") {
			t.Fatalf("validate %s: %d %s", kind, res.StatusCode, body)
		}
		header, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(files[kind], "\uFEFF"))).Read()
		if err != nil {
			t.Fatal(err)
		}
		form := url.Values{"csrf": {sess.CSRFToken}, "kind": {kind}, "csv": {files[kind]}, "dates": {"dmy"}, "action": {"import"}}
		for i, target := range importer.Guess(kind, header) {
			form.Set("col_"+strconv.Itoa(i), target)
		}
		res, body = do(http.MethodPost, "/settings/import", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		if res.StatusCode != http.StatusOK || !strings.Contains(body, "Importado") || strings.Contains(body, "rechazados") {
			t.Fatalf("import %s: %d %s", kind, res.StatusCode, body)
		}
	}
	bundle := func(ws crm.Actor) string {
		t.Helper()
		b, err := svc.Export(ctx, ws)
		if err != nil {
			t.Fatal(err)
		}
		b.Interactions = nil
		out, _ := json.MarshalIndent(b, "", " ")
		return string(out)
	}
	if a, b := bundle(owner), bundle(crm.NewActor(u.ID, beta.ID, "owner", "session", "", "")); a != b {
		t.Fatalf("round trip differs:\nAcme %s\nBeta %s", a, b)
	}

	// Members cannot export.
	if err := id.Invite(ctx, identity.Operator(acme.ID), "bob@acme.test", "member"); err != nil {
		t.Fatal(err)
	}
	bob, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: "b", Email: "bob@acme.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	if token, err = id.CreateSession(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	if sess, err = id.ResolveSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if res, _ := do(http.MethodGet, "/settings/export", "", nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member opened the export page: %d", res.StatusCode)
	}
	if res, _ := export(crm.KindContact, sess.CSRFToken); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member exported: %d", res.StatusCode)
	}
}
