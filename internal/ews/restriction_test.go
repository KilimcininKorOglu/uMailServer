package ews

import "testing"

// TestEvalFilterIsNotEqualTo verifies the IsNotEqualTo restriction operator,
// alongside IsEqualTo and a Not(IsEqualTo) equivalence check.
func TestEvalFilterIsNotEqualTo(t *testing.T) {
	mkCmp := func(uri, val string) *ComparisonFilter {
		return &ComparisonFilter{
			FieldURI: &FieldURI{URI: uri},
			FieldURIOrConstant: &FieldURIOrConstant{
				Constant: &struct {
					Value string `xml:"Value,attr"`
				}{Value: val},
			},
		}
	}
	fields := filterFields{From: "alice@ex.test"}

	cases := []struct {
		name string
		f    SearchFilter
		want bool
	}{
		{"not-equal matches when different", SearchFilter{IsNotEqualTo: mkCmp("message:From", "bob@ex.test")}, true},
		{"not-equal fails when same", SearchFilter{IsNotEqualTo: mkCmp("message:From", "alice@ex.test")}, false},
		{"equal matches when same", SearchFilter{IsEqualTo: mkCmp("message:From", "alice@ex.test")}, true},
		{"equal fails when different", SearchFilter{IsEqualTo: mkCmp("message:From", "bob@ex.test")}, false},
		{"Not(IsEqualTo) equals IsNotEqualTo", SearchFilter{Not: &SearchFilter{IsEqualTo: mkCmp("message:From", "bob@ex.test")}}, true},
	}
	for _, tc := range cases {
		if got := evalFilter(tc.f, fields, "", "", false); got != tc.want {
			t.Errorf("%s: evalFilter = %v, want %v", tc.name, got, tc.want)
		}
	}
}
