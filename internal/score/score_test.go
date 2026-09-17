package score

import (
	"math"
	"strings"
	"testing"

	"zonapropbot/internal/model"
)

func iptr(v int64) *int64     { return &v }
func fptr(v float64) *float64 { return &v }
func intp(v int) *int         { return &v }

func saleListing() model.Listing {
	return model.Listing{
		Operation:   "venta",
		Currency:    "USD",
		PriceAmount: iptr(144000),
		M2Tot:       fptr(96),
		Banos:       intp(2),
		Location:    "Florida, Vicente López",
	}
}

// Numeric features are namespaced so a USD sale and an ARS rental can never share
// a bucket: their magnitudes are not comparable.
func TestExtractNamespacesNumericFeatures(t *testing.T) {
	sale := Extract(saleListing())
	if _, ok := sale["ppm2:venta:USD"]; !ok {
		t.Fatalf("missing ppm2 namespace, got %v", sale)
	}
	// 144000 / 96 = 1500, which falls in the 1500-1750 bucket.
	if got := sale["ppm2:venta:USD"]; got != "1500-1750" {
		t.Errorf("ppm2 bucket = %q, want 1500-1750 for 144000/96", got)
	}
	// This listing has no expenses, so no expenses bucket.
	if _, ok := sale["expensas:venta:USD"]; ok {
		t.Error("a listing without expensas should not emit an expensas bucket")
	}

	rent := saleListing()
	rent.Operation = "alquiler"
	rent.Currency = "ARS"
	rent.PriceAmount = iptr(450000)
	rent.Expensas = iptr(80000)
	rent.M2Tot = fptr(60)
	features := Extract(rent)
	if _, ok := features["ppm2:alquiler:ARS"]; !ok {
		t.Errorf("rental must be namespaced by alquiler:ARS, got %v", features)
	}
	if _, ok := features["ppm2:venta:USD"]; ok {
		t.Error("a rental leaked into the sale namespace")
	}
	// A non-validated namespace uses log bins instead of guessing an ARS step.
	if !strings.HasPrefix(features["ppm2:alquiler:ARS"], "log") {
		t.Errorf("unvalidated namespace should use log bins, got %q", features["ppm2:alquiler:ARS"])
	}
}

// Without a currency, no numeric bucket is emitted at all: scoring an ARS amount
// against a USD band is how a 1000x outlier owns the ranking.
func TestExtractOmitsNumericWhenCurrencyUnknown(t *testing.T) {
	l := saleListing()
	l.Currency = ""
	features := Extract(l)
	for key := range features {
		if strings.HasPrefix(key, "ppm2:") || strings.HasPrefix(key, "expensas:") {
			t.Errorf("emitted a numeric bucket without a currency: %s", key)
		}
	}
}

func TestExtractUsesUnknownNeverZero(t *testing.T) {
	features := Extract(model.Listing{})
	if features["partido"] != Unknown {
		t.Errorf("partido = %q, want unknown", features["partido"])
	}
	if features["banios"] != Unknown {
		t.Errorf("banios = %q, want unknown", features["banios"])
	}
	for key, value := range features {
		if value == "0" || strings.HasPrefix(value, "0-") {
			t.Errorf("absent value became zero for %s: %q", key, value)
		}
	}
}

func TestExtractPrefersTotalSurface(t *testing.T) {
	l := saleListing()
	l.M2Cub = fptr(80)
	features := Extract(l)
	if _, ok := features["m2:tot"]; !ok {
		t.Error("should bucket the total surface when both are present")
	}
	if _, ok := features["m2:cub"]; ok {
		t.Error("should not bucket both surfaces under different keys for the same listing")
	}
}

func TestScoreIsZeroWithoutEvidence(t *testing.T) {
	empty := NewWeights(0)
	if got, n := empty.Score(Extract(saleListing())); got != 0 || n != 0 {
		t.Errorf("Score with no evidence = (%v, %d), want (0, 0)", got, n)
	}
	var nilWeights *Weights
	if got, n := nilWeights.Score(Extract(saleListing())); got != 0 || n != 0 {
		t.Errorf("Score on nil weights = (%v, %d), want (0, 0)", got, n)
	}
}

func TestScoreAveragesKnownBuckets(t *testing.T) {
	w := NewWeights(1)
	// Vicente López: 4 likes of 4 -> rate 5/6.
	w.Buckets["partido"] = map[string]Bucket{"Vicente López": {Ups: 4}}
	// 2 bathrooms: 0 likes of 3 -> rate 1/5.
	w.Buckets["banios"] = map[string]Bucket{"2": {Downs: 3}}

	got, used := w.Score(Extract(saleListing()))
	want := (math.Log((5.0/6.0)/0.5) + math.Log((1.0/5.0)/0.5)) / 2
	if used != 2 {
		t.Fatalf("used %d buckets, want 2", used)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("Score = %v, want %v", got, want)
	}

	// A feature with no bucket must not dilute the average.
	partialScore, partialUsed := w.Score(Features{"partido": "Vicente López", "banios": "9"})
	wantSingle := math.Log((5.0 / 6.0) / 0.5)
	if partialUsed != 1 {
		t.Errorf("unknown bucket counted as evidence: used=%d", partialUsed)
	}
	if math.Abs(partialScore-wantSingle) > 1e-9 {
		t.Errorf("Score = %v, want %v", partialScore, wantSingle)
	}
}

// A reason with two data points is noise dressed as an explanation. The trust the
// model depends on is worth more than the extra line of text.
func TestReasonsNeedMinimumSupport(t *testing.T) {
	w := NewWeights(1)
	w.Buckets["partido"] = map[string]Bucket{"Vicente López": {Ups: 1, Downs: 1}}
	if got := w.Reasons(Features{"partido": "Vicente López"}, 2); len(got) != 0 {
		t.Errorf("reasons with 2 samples = %v, want none", got)
	}

	w.Buckets["partido"]["Vicente López"] = Bucket{Ups: 4, Downs: 1}
	reasons := w.Reasons(Features{"partido": "Vicente López"}, 2)
	if len(reasons) != 1 {
		t.Fatalf("reasons = %v, want one", reasons)
	}
	if !strings.Contains(reasons[0].Text, "te gustaron 4 de 5") {
		t.Errorf("reason text = %q", reasons[0].Text)
	}
}

func TestReasonsAreOrderedByStrengthAndCapped(t *testing.T) {
	w := NewWeights(1)
	w.Buckets["partido"] = map[string]Bucket{"San Isidro": {Ups: 3, Downs: 3}}        // rate 0.5 -> no deviation
	w.Buckets["banios"] = map[string]Bucket{"2": {Ups: 9}}                            // rate 10/11
	w.Buckets["ppm2:venta:USD"] = map[string]Bucket{"1250-1500": {Ups: 0, Downs: 30}} // rate 1/32, strongest

	reasons := w.Reasons(Features{
		"partido": "San Isidro", "banios": "2", "ppm2:venta:USD": "1250-1500",
	}, 2)
	if len(reasons) != 2 {
		t.Fatalf("reasons = %v, want exactly 2", reasons)
	}
	// A bucket at the prior has no signal and must not be shown.
	for _, r := range reasons {
		if r.Feature == "partido" {
			t.Errorf("a bucket at the prior was shown as a reason: %v", r)
		}
	}
	if reasons[0].Feature != "ppm2:venta:USD" {
		t.Errorf("strongest reason should come first, got %v", reasons)
	}
}

func TestBucketSummaryOrdersByEvidence(t *testing.T) {
	w := NewWeights(1)
	w.Buckets["partido"] = map[string]Bucket{"Pilar": {Ups: 1}}
	w.Buckets["banios"] = map[string]Bucket{"2": {Ups: 5, Downs: 2}}
	got := BucketSummary(w, 1)
	if !strings.Contains(got, "banios=2") {
		t.Errorf("summary = %q, want the best-supported bucket first", got)
	}
	if strings.Contains(got, "Pilar") {
		t.Errorf("summary ignored the cap: %q", got)
	}
}
