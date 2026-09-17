package score

import (
	"fmt"
	"math"
	"sort"
)

// Bucket is the evidence behind one feature value.
type Bucket struct {
	Ups   int
	Downs int
}

// Weights is one generation of a user's model.
type Weights struct {
	Version int
	// Buckets[feature][value] = evidence.
	Buckets map[string]map[string]Bucket
}

// NewWeights returns an empty model of the given version.
func NewWeights(version int) *Weights {
	return &Weights{Version: version, Buckets: map[string]map[string]Bucket{}}
}

// Rate is the Laplace-smoothed like rate of a bucket: (ups+1)/(n+2). Smoothing
// keeps a single 👍 from reading as certainty, and keeps an unseen bucket at 0.5.
func (b Bucket) Rate() float64 {
	return float64(b.Ups+1) / float64(b.Ups+b.Downs+2)
}

// Score is the mean of ln(rate/0.5) over the features that have evidence.
//
// A feature whose bucket was never observed contributes nothing at all: there is
// no evidence, so there is no adjustment, which is the honest prior. Features are
// averaged rather than summed so that a listing with more extractable fields does
// not automatically score higher.
//
// It returns the score and how many buckets backed it, so callers can be honest
// about confidence instead of printing a number with none.
func (w *Weights) Score(f Features) (float64, int) {
	if w == nil || len(w.Buckets) == 0 {
		return 0, 0
	}
	var sum float64
	used := 0
	for name, value := range f {
		bucket, ok := w.Buckets[name][value]
		if !ok {
			continue
		}
		sum += math.Log(bucket.Rate() / 0.5)
		used++
	}
	if used == 0 {
		return 0, 0
	}
	return sum / float64(used), used
}

// Reason is a plain-language justification for a listing's score.
type Reason struct {
	Feature string
	Value   string
	Bucket  Bucket
	Text    string
}

// minReasonSupport is how much evidence a bucket needs before it is shown. With
// one rating, "you liked 1 of 1" is noise dressed as a reason; the trust the model
// depends on is worth more than the extra explanation.
const minReasonSupport = 3

// Reasons returns up to max plain-language reasons, strongest first. It returns
// nothing when no bucket has enough support, rather than inventing one.
func (w *Weights) Reasons(f Features, max int) []Reason {
	if w == nil {
		return nil
	}
	type candidate struct {
		feature, value string
		bucket         Bucket
		weight         float64
	}
	var candidates []candidate
	for name, value := range f {
		bucket, ok := w.Buckets[name][value]
		if !ok || bucket.Ups+bucket.Downs < minReasonSupport {
			continue
		}
		candidates = append(candidates, candidate{
			feature: name, value: value, bucket: bucket,
			weight: math.Abs(bucket.Rate() - 0.5),
		})
	}

	// Strongest deviation from the prior first; ties broken deterministically so
	// the same listing always shows the same reasons.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].weight != candidates[j].weight {
			return candidates[i].weight > candidates[j].weight
		}
		if candidates[i].feature != candidates[j].feature {
			return candidates[i].feature < candidates[j].feature
		}
		return candidates[i].value < candidates[j].value
	})

	var out []Reason
	for _, c := range candidates {
		if len(out) >= max {
			break
		}
		out = append(out, Reason{
			Feature: c.feature,
			Value:   c.value,
			Bucket:  c.bucket,
			Text:    reasonText(c.feature, c.value, c.bucket),
		})
	}
	return out
}

// reasonText renders the reason in Spanish, because this is the one sentence the
// user has to trust.
func reasonText(feature, value string, b Bucket) string {
	switch {
	case feature == "partido":
		return fmt.Sprintf("%s: te gustaron %d de %d", value, b.Ups, b.Ups+b.Downs)
	case feature == "banios":
		return fmt.Sprintf("%s baño(s): te gustaron %d de %d", value, b.Ups, b.Ups+b.Downs)
	default:
		return fmt.Sprintf("%s: te gustaron %d de %d", value, b.Ups, b.Ups+b.Downs)
	}
}

// Add records one rating into the model being built.
func (w *Weights) Add(f Features, like bool) {
	for name, value := range f {
		if w.Buckets[name] == nil {
			w.Buckets[name] = map[string]Bucket{}
		}
		b := w.Buckets[name][value]
		if like {
			b.Ups++
		} else {
			b.Downs++
		}
		w.Buckets[name][value] = b
	}
}
