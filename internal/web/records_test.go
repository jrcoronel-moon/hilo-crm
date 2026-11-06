package web

import "testing"

func TestParseAmount(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{{"1500", 1500}, {"1500.5", 1500.5}, {"1500,5", 1500.5}, {"1.500,50", 1500.5}, {"1,500.50", 1500.5}, {" 12 000,25 ", 12000.25}, {"0,99", 0.99}}
	for _, c := range cases {
		if got, err := parseAmount(c.in); err != nil || got != c.want {
			t.Errorf("%q: got %v %v want %v", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"abc", "1,2,3", "", "1.2.3"} {
		if _, err := parseAmount(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
