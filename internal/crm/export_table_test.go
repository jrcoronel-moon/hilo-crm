package crm_test

import (
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

func TestSafeCSV(t *testing.T) {
	row := crm.SafeCSV([]string{"=1+1", "+cmd", "-2", "@SUM(A1)", "normal", "", "\tx"})
	for i, want := range []string{"'=1+1", "'+cmd", "'-2", "'@SUM(A1)", "normal", "", "'\tx"} {
		if row[i] != want {
			t.Errorf("cell %d: got %q want %q", i, row[i], want)
		}
	}
}
