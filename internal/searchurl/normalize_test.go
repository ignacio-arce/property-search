package searchurl

import "testing"

func TestNormalizeStripsTrackingAndCanonicalizes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "tracking params removed",
			in:   "https://www.zonaprop.com.ar/departamentos-venta.html?n_src=Listado&n_pg=2&n_pos=7",
			want: "https://www.zonaprop.com.ar/departamentos-venta.html",
		},
		{
			name: "host and scheme lowercased",
			in:   "HTTPS://WWW.ZonaProp.com.AR/departamentos-venta.html",
			want: "https://www.zonaprop.com.ar/departamentos-venta.html",
		},
		{
			name: "kept params sorted",
			in:   "https://www.zonaprop.com.ar/x.html?z=1&a=2",
			want: "https://www.zonaprop.com.ar/x.html?a=2&z=1",
		},
		{
			name: "utm stripped and fragment dropped",
			in:   "https://www.zonaprop.com.ar/x.html?utm_source=nl&a=2#anchor",
			want: "https://www.zonaprop.com.ar/x.html?a=2",
		},
		{
			name: "same search from another page collapses",
			in:   "https://www.zonaprop.com.ar/x.html?n_search_id=abc&n_pg=3&n_pos=12",
			want: "https://www.zonaprop.com.ar/x.html",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Normalize(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"missing scheme", "www.zonaprop.com.ar/x.html"},
		{"unsupported scheme", "ftp://www.zonaprop.com.ar/x.html"},
		{"missing host", "https:///x.html"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Normalize(tc.in); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestIsZonapropURL(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://www.zonaprop.com.ar/x.html", true},
		{"https://zonaprop.com.ar/x.html", true},
		{"https://ZONAPROP.com.ar/x.html", true},
		// A substring check would accept both of these, and the deep validation
		// would then load them in a real browser on the operator's LAN.
		{"https://notzonaprop.com.ar/x.html", false},
		{"https://zonaprop.com.ar.attacker.test/x.html", false},
		{"https://zonaprop.com.ar.evil.com/x.html", false},
		// http is not accepted: only https is fetched.
		{"http://www.zonaprop.com.ar/x.html", false},
		{"https://www.argenprop.com/x.html", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := IsZonapropURL(tc.in); got != tc.want {
				t.Errorf("IsZonapropURL(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
