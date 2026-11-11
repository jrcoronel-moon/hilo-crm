package crm_test

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

// TestAvatars: a logo or photo URL set by an agent is fetched once, served
// from the cache, limited to raster images, never readable from another
// workspace, and only https URLs are accepted.
func TestAvatars(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	other, _ := f.workspace("Other")

	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/logo.png":
			_, _ = w.Write(pngBytes.Bytes())
		case "/evil.svg":
			w.Header().Set("Content-Type", "image/png") // lies: the bytes decide
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		case "/huge.png":
			_, _ = w.Write(append(pngBytes.Bytes(), make([]byte, 600<<10)...))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	f.crm.SetImageClient(ts.Client())

	co := must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Globex"), LogoURL: s(ts.URL + "/logo.png")}))
	if co.LogoURL != ts.URL+"/logo.png" {
		t.Fatalf("logo_url not saved: %+v", co)
	}
	for range 2 {
		data, ctype, err := f.crm.Avatar(f.ctx, a, "company", co.ID)
		if err != nil || ctype != "image/png" || !bytes.Equal(data, pngBytes.Bytes()) {
			t.Fatalf("avatar: %q %v", ctype, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("fetched %d times, want 1 (cache)", hits.Load())
	}
	// Saving the company again keeps its logo.
	must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{ID: s(co.ID.String()), Industry: s("Software")}))
	if got := must(f.crm.GetCompany(f.ctx, a, co.ID.String())); got.LogoURL == "" {
		t.Fatal("an update without logo_url cleared it")
	}
	if _, _, err := f.crm.Avatar(f.ctx, other, "company", co.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("another workspace read the logo: %v", err)
	}

	for _, path := range []string{"/evil.svg", "/huge.png", "/missing.png"} {
		ct := must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{FirstName: s("Ana" + path), PhotoURL: s(ts.URL + path)}))
		if _, _, err := f.crm.Avatar(f.ctx, a, "contact", ct.ID); !errors.Is(err, crm.ErrNotFound) {
			t.Fatalf("%s served: %v", path, err)
		}
	}
	plain := must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{FirstName: s("Sin foto")}))
	if _, _, err := f.crm.Avatar(f.ctx, a, "contact", plain.ID); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("no photo: %v", err)
	}
	for _, bad := range []string{"http://example.com/a.png", "javascript:alert(1)", "https://user:pw@example.com/a.png", "/relative.png"} {
		if _, _, err := f.crm.UpsertContact(f.ctx, a, crm.ContactInput{FirstName: s("X"), PhotoURL: s(bad)}); !isValidation(err) {
			t.Fatalf("%q accepted: %v", bad, err)
		}
	}
}
