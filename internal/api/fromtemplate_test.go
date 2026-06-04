package api

import "testing"

func TestExpandFromTemplate(t *testing.T) {
	full := map[string]string{
		"name":       "Test Deneme",
		"title":      "Yazılım Mühendisi",
		"department": "Ar-Ge",
		"company":    "Acme A.Ş.",
		"email":      "test@acme.test",
	}
	cases := []struct {
		name   string
		tmpl   string
		fields map[string]string
		want   string
	}{
		{
			name:   "internal name+title",
			tmpl:   "{name} ({title})",
			fields: full,
			want:   "Test Deneme (Yazılım Mühendisi)",
		},
		{
			name:   "external name+company-title",
			tmpl:   "{name} ({company} - {title})",
			fields: full,
			want:   "Test Deneme (Acme A.Ş. - Yazılım Mühendisi)",
		},
		{
			name:   "empty title drops empty parens",
			tmpl:   "{name} ({title})",
			fields: map[string]string{"name": "Test Deneme", "title": ""},
			want:   "Test Deneme",
		},
		{
			name:   "empty company drops leading separator",
			tmpl:   "{name} ({company} - {title})",
			fields: map[string]string{"name": "Test Deneme", "company": "", "title": "Müdür"},
			want:   "Test Deneme (Müdür)",
		},
		{
			name:   "empty title drops trailing separator",
			tmpl:   "{name} ({company} - {title})",
			fields: map[string]string{"name": "Test Deneme", "company": "Acme", "title": ""},
			want:   "Test Deneme (Acme)",
		},
		{
			name:   "both optional empty collapses to name",
			tmpl:   "{name} ({company} - {title})",
			fields: map[string]string{"name": "Test Deneme", "company": "", "title": ""},
			want:   "Test Deneme",
		},
		{
			name:   "plain name only",
			tmpl:   "{name}",
			fields: full,
			want:   "Test Deneme",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandFromTemplate(tc.tmpl, tc.fields); got != tc.want {
				t.Errorf("expandFromTemplate(%q) = %q, want %q", tc.tmpl, got, tc.want)
			}
		})
	}
}
