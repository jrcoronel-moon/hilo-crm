package crm

import "net/http"

// SetImageClient replaces the picture fetcher in tests: their httptest TLS
// server listens on loopback, which the real (safehttp) client refuses.
func (s *Service) SetImageClient(c *http.Client) { s.img = c }
