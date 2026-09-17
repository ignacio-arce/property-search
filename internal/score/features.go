// Package score turns a listing into the buckets the model learns on, and turns
// the learned weights back into a score.
//
// Two rules shape everything here, both from the risk probes:
//
//   - operation_type and currency are PARTITION KEYS, not features. An ARS rental
//     and a USD sale have incomparable magnitudes; as features the model would
//     only learn "you like rentals", which means nothing.
//   - a value that is absent becomes an explicit "unknown" bucket, never zero. A
//     listing with no price must not be scored as the cheapest one.
package score

import (
	"fmt"
	"math"
	"strconv"

	"zonapropbot/internal/model"
	"zonapropbot/internal/parser"
)

// Unknown is the explicit bucket for an absent or unusable value.
const Unknown = "unknown"

// ppm2LinearStep is the only bin size validated with real data: the probe measured
// venta:USD between 896 and 2759 USD/m², which yields 9 non-empty buckets at a 250
// step. For every other namespace the scale is unknown, so a logarithmic bin
// adapts to any currency and magnitude instead of guessing an ARS step that would
// be off by orders of magnitude.
const (
	ppm2LinearStep = 250.0
	logBinRatio    = 1.12
	m2Step         = 10.0
)

// Features maps a feature name to its bucket value for one listing.
type Features map[string]string

// Extract derives the buckets of a listing. Feature names carry their namespace so
// the same feature can never mix currencies.
func Extract(l model.Listing) Features {
	f := Features{}

	// Categorical features are not partitioned: a taste for a neighbourhood or for
	// an extra bathroom does not depend on whether it is a rental or a sale.
	if p := parser.Partido(l.Location); p != "" {
		f["partido"] = p
	} else {
		f["partido"] = Unknown
	}
	f["banios"] = intBucket(l.Banos)

	// Size does not depend on the currency, only on which surface it measures.
	if l.M2Tot != nil {
		f["m2:tot"] = linearBin(*l.M2Tot, m2Step)
	} else if l.M2Cub != nil {
		f["m2:cub"] = linearBin(*l.M2Cub, m2Step)
	}

	// Numeric features are namespaced by operation and currency.
	ns := namespaceOf(l)
	if ns != "" {
		if ppm2, ok := pricePerM2(l); ok {
			f["ppm2:"+ns] = binFor(ns, "ppm2", ppm2)
		}
		if l.Expensas != nil {
			f["expensas:"+ns] = binFor(ns, "expensas", float64(*l.Expensas))
		}
	}
	// With no currency we deliberately emit no numeric bucket at all: scoring an
	// ARS amount against a USD band is how a 1000x outlier owns the ranking.
	return f
}

func namespaceOf(l model.Listing) string {
	if l.Operation == "" || l.Currency == "" {
		return ""
	}
	return l.Operation + ":" + l.Currency
}

// pricePerM2 uses the total surface: the probe found zero occurrences of covered
// m² on the real cards, so conditioning on it would leave the feature unknown
// every single time.
func pricePerM2(l model.Listing) (float64, bool) {
	if l.PriceAmount == nil || l.M2Tot == nil {
		return 0, false
	}
	if *l.M2Tot < 10 || *l.M2Tot > 1000 {
		return 0, false
	}
	return float64(*l.PriceAmount) / *l.M2Tot, true
}

func binFor(namespace, feature string, value float64) string {
	if namespace == "venta:USD" && feature == "ppm2" && value > 0 {
		return linearBin(value, ppm2LinearStep)
	}
	return logBin(value, logBinRatio)
}

func intBucket(v *int) string {
	if v == nil {
		return Unknown
	}
	return strconv.Itoa(*v)
}

// linearBin renders "1500-1750".
func linearBin(v, step float64) string {
	if v <= 0 || step <= 0 {
		return Unknown
	}
	lo := math.Floor(v/step) * step
	return fmt.Sprintf("%.0f-%.0f", lo, lo+step)
}

// logBin renders a scale-free bucket, e.g. "log52". It adapts to any magnitude.
func logBin(v, ratio float64) string {
	if v <= 0 || ratio <= 1 {
		return Unknown
	}
	return "log" + strconv.Itoa(int(math.Floor(math.Log(v)/math.Log(ratio))))
}
