package web

import (
	"hash/fnv"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// avatar serves a company's logo or a contact's photo (crm.Avatar fetches and
// caches it). It is an image from elsewhere, so the response cannot run
// anything even when opened on its own, and the browser keeps it a day; the
// page asks with ?v=<hash of the URL>, so a new picture shows at once.
func (h *Handler) avatar(w http.ResponseWriter, r *http.Request, v view) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, ctype, err := h.crm.Avatar(r.Context(), v.Actor, r.PathValue("kind"), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", ctype)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	hd.Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data)
}

// avatarData feeds the "avatar" partial: initials on a stable hue, with the
// picture on top when the record has one (it falls back to the initials if
// it cannot load).
type avatarData struct {
	Class, Initials, Src string
}

// av builds an avatar: kind "company" (square) or "contact" (round), the
// record id, its name (and email, for people without one), its picture URL
// and a size class ("", "md", "xl").
func av(kind string, id uuid.UUID, name, email, picture, size string) avatarData {
	d := avatarData{Class: "av " + size + " " + avClass(orStr(name, email)), Initials: initials(name, email)}
	if kind == "company" {
		d.Class += " sq"
	}
	if picture != "" {
		h := fnv.New32a()
		_, _ = h.Write([]byte(picture))
		d.Src = "/avatars/" + kind + "/" + id.String() + "?v=" + strconv.FormatUint(uint64(h.Sum32()), 36)
		d.Class += " pic"
	}
	return d
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
