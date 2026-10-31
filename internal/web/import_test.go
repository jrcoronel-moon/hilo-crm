package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

// TestImportCSVPage walks Ajustes → Importar: upload, mapping with a dry run,
// then the real import; and checks that members without admin cannot open it.
func TestImportCSVPage(t *testing.T) {
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
	u, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: "r", Email: "root@acme.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := id.CreateWorkspace(ctx, u.ID, "Acme")
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
	do := func(method, path, ctype string, body io.Reader) (int, string) {
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
		return res.StatusCode, string(b)
	}

	if st, body := do(http.MethodGet, "/settings/import", "", nil); st != http.StatusOK || !strings.Contains(body, "Importar desde CSV") {
		t.Fatalf("GET: %d %s", st, body)
	}

	// Step 1: upload. The mapping is guessed and validated with a dry run.
	owner := crm.NewActor(u.ID, ws.ID, "owner", "session", "", "")
	if _, _, err := crm.New(d, log).UpsertCompany(ctx, owner, crm.CompanyInput{Name: ptr("Globex"), Domain: ptr("globex.test")}); err != nil {
		t.Fatal(err)
	}
	csvText := "Nombre,E-mail,Empresa,Cargo\nAna López,ANA@globex.test,globex.test,CTO\nSin email,,,\nBob,not-an-email,,\n"
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf", sess.CSRFToken)
	_ = mw.WriteField("kind", "contact")
	_ = mw.WriteField("source", "hs")
	fw, _ := mw.CreateFormFile("file", "contactos.csv")
	_, _ = fw.Write([]byte(csvText))
	_ = mw.Close()
	st, body := do(http.MethodPost, "/settings/import", mw.FormDataContentType(), &buf)
	if st != http.StatusOK || !strings.Contains(body, "Validación") || !strings.Contains(body, "nada guardado") ||
		!strings.Contains(body, `<option value="email" selected>`) || !strings.Contains(body, "2 válidos") || !strings.Contains(body, "1 rechazados") {
		t.Fatalf("upload: %d %s", st, body)
	}
	if page, _ := crm.New(d, log).ListContacts(ctx, owner, crm.ListFilter{}); len(page.Items) != 0 {
		t.Fatalf("dry run saved %d contacts", len(page.Items))
	}

	// Step 2: the user maps "Cargo" to a custom field and imports.
	form := url.Values{"csrf": {sess.CSRFToken}, "kind": {"contact"}, "csv": {csvText}, "source": {"hs"}, "dates": {"dmy"}, "tags": {"importado"},
		"col_0": {"name"}, "col_1": {"email"}, "col_2": {"company"}, "col_3": {"custom"}, "action": {"import"}}
	st, body = do(http.MethodPost, "/settings/import", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if st != http.StatusOK || !strings.Contains(body, "Importado") || !strings.Contains(body, "2 guardados") || !strings.Contains(body, "2 nuevos") {
		t.Fatalf("import: %d %s", st, body)
	}
	page, err := crm.New(d, log).ListContacts(ctx, owner, crm.ListFilter{Query: "Ana"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Email != "ana@globex.test" || page.Items[0].LastName != "López" ||
		!strings.Contains(string(page.Items[0].Custom), `"CTO"`) || strings.Join(page.Items[0].Tags, ",") != "importado" {
		t.Fatalf("imported contact: %+v %v", page.Items, err)
	}

	// A broken mapping is reported on the page, not as a 500.
	form.Set("col_0", "")
	form.Set("col_1", "")
	form.Set("action", "preview")
	if st, body := do(http.MethodPost, "/settings/import", "application/x-www-form-urlencoded", strings.NewReader(form.Encode())); st != http.StatusBadRequest || !strings.Contains(body, "Revisá el mapeo") {
		t.Fatalf("bad mapping: %d %s", st, body)
	}

	// Members cannot import.
	if err := id.Invite(ctx, identity.Caller{UserID: u.ID, WorkspaceID: ws.ID, Role: "owner"}, "bob@acme.test", "member"); err != nil {
		t.Fatal(err)
	}
	bob, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: "b", Email: "bob@acme.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	if token, err = id.CreateSession(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := do(http.MethodGet, "/settings/import", "", nil); st != http.StatusForbidden {
		t.Fatalf("member opened the import page: %d", st)
	}
}
