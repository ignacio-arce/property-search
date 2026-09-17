package model

import "testing"

func TestPriceLabel(t *testing.T) {
	cases := []struct {
		name   string
		amount *int64
		cur    string
		want   string
	}{
		{"usd", iptr(144000), "USD", "USD 144.000"},
		{"ars rent", iptr(450000), "ARS", "ARS 450.000"},
		{"absent", nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Listing{PriceAmount: tc.amount, Currency: tc.cur}.PriceLabel()
			if got != tc.want {
				t.Errorf("PriceLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSizeLabel(t *testing.T) {
	rooms := 3
	cases := []struct {
		name string
		l    Listing
		want string
	}{
		{
			name: "total with basis and rooms",
			l:    Listing{M2Tot: fptr(69), M2Basis: "tot", Rooms: &rooms},
			want: "69 m² tot. · 3 amb.",
		},
		{
			name: "covered falls back",
			l:    Listing{M2Cub: fptr(220), M2Basis: "cub"},
			want: "220 m² cub.",
		},
		{name: "absent", l: Listing{}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.l.SizeLabel(); got != tc.want {
				t.Errorf("SizeLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpensasLabel(t *testing.T) {
	if got := (Listing{Expensas: iptr(180000)}).ExpensasLabel(); got != "$ 180.000" {
		t.Errorf("ExpensasLabel() = %q", got)
	}
	if got := (Listing{}).ExpensasLabel(); got != "" {
		t.Errorf("absent ExpensasLabel() = %q, want empty", got)
	}
}

func TestFormatThousands(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.000"},
		{83900, "83.900"},
		{150000000, "150.000.000"},
	}
	for _, tc := range cases {
		if got := formatThousands(tc.in); got != tc.want {
			t.Errorf("formatThousands(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func iptr(v int64) *int64     { return &v }
func fptr(v float64) *float64 { return &v }
