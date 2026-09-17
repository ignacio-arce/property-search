package score

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"zonapropbot/internal/repo"
)

// ConcordanceWindow is how far back the out-of-sample agreement looks.
const ConcordanceWindow = 30 * 24 * time.Hour

// minSamplesForConcordance is when agreement becomes worth showing. Below it the
// number would describe the user's tapping more than the model.
const minSamplesForConcordance = 30

// Retrain rebuilds one user's model from their ratings and activates the new
// generation atomically. It returns the new version.
//
// Training uses the snapshot stored on the delivery, not the listing's latest
// features: a seller can change the price, and the model must learn from what the
// user actually reacted to.
func Retrain(ctx context.Context, r *repo.Repo, userID int64) (int, error) {
	samples, err := r.TrainingSamples(ctx, userID)
	if err != nil {
		return 0, err
	}

	current, err := r.ModelVersion(ctx, userID)
	if err != nil {
		return 0, err
	}
	next := current + 1

	w := NewWeights(next)
	for _, s := range samples {
		w.Add(Extract(s.Snapshot.Listing()), s.Label == 1)
	}

	rows := make([]repo.WeightRow, 0, len(w.Buckets))
	for feature, values := range w.Buckets {
		for value, bucket := range values {
			rows = append(rows, repo.WeightRow{
				Feature: feature, Value: value, Ups: bucket.Ups, Downs: bucket.Downs,
			})
		}
	}

	if err := r.ReplaceWeights(ctx, userID, next, rows); err != nil {
		return 0, err
	}
	return next, nil
}

// Load returns the user's active model, or an empty one at version 0.
func Load(ctx context.Context, r *repo.Repo, userID int64) (*Weights, error) {
	version, rows, err := r.ActiveWeights(ctx, userID)
	if err != nil {
		return nil, err
	}
	w := NewWeights(version)
	for _, row := range rows {
		if w.Buckets[row.Feature] == nil {
			w.Buckets[row.Feature] = map[string]Bucket{}
		}
		w.Buckets[row.Feature][row.Value] = Bucket{Ups: row.Ups, Downs: row.Downs}
	}
	return w, nil
}

// Agreement computes out-of-sample concordance over the last window: among rated
// deliveries, the fraction of pairs whose scores agree with the labels.
//
// Pairwise concordance rather than accuracy, because scores tie constantly at the
// prior: with a 0.5 threshold every prior-tied 👍 would "agree" and every 👎 would
// not, which measures the user's tapping bias instead of the model. Comparing
// pairs within the same batch is the honest signal.
//
// It reports ok=false when there is not enough evidence, so the caller can say
// "still learning" instead of printing a number with no basis.
func Agreement(ctx context.Context, r *repo.Repo, userID int64, now time.Time) (float64, int, bool, error) {
	samples, err := r.ScoredRatedDeliveries(ctx, userID, now.Add(-ConcordanceWindow))
	if err != nil {
		return 0, 0, false, err
	}
	if len(samples) < minSamplesForConcordance {
		return 0, len(samples), false, nil
	}

	// Group by calendar day: pairs are only comparable within one delivery batch,
	// where the scores came from the same model generation.
	byDay := map[string][]repo.ScoredSample{}
	for _, s := range samples {
		day := s.RatedAt.Format("2006-01-02")
		byDay[day] = append(byDay[day], s)
	}

	agreeing, comparable := 0, 0
	for _, day := range byDay {
		for i := 0; i < len(day); i++ {
			for j := i + 1; j < len(day); j++ {
				a, b := day[i], day[j]
				if a.Score == b.Score || a.Label == b.Label {
					continue // not comparable
				}
				comparable++
				if (a.Score > b.Score) == (a.Label > b.Label) {
					agreeing++
				}
			}
		}
	}
	if comparable == 0 {
		return 0, len(samples), false, nil
	}
	return float64(agreeing) / float64(comparable), len(samples), true, nil
}

// SummaryText renders the model report. It only states what can be computed: the
// message keys off the ratings, not off the model version, because a user can have
// ratings before the first retrain has run.
func SummaryText(version int, likes, dislikes int, agreement float64, samples int, hasAgreement bool) string {
	if likes+dislikes == 0 {
		return "🤖 Modelo\n\nTodavía no tengo calificaciones tuyas.\nCalificá con 👍/👎 y empiezo a aprender."
	}
	head := "🤖 Modelo"
	if version > 0 {
		head = fmt.Sprintf("🤖 Modelo (versión %d)", version)
	}
	base := fmt.Sprintf("%s\nCalificaciones: %d 👍 · %d 👎\n", head, likes, dislikes)
	if !hasAgreement {
		return base + fmt.Sprintf("\nConcordancia: todavía no medible (%d de %d calificaciones).\nHasta juntar suficientes no muestro razones, para no inventar señales que no tengo.", samples, minSamplesForConcordance)
	}
	return base + fmt.Sprintf("\nConcordancia con lo que ordené: %.0f%% en %d calificaciones.\nEsto mide si el orden que te mostré coincidió con tus gustos.", agreement*100, samples)
}

// BucketSummary lists the buckets with the most evidence, strongest first. It is
// how /model shows what it actually learned, bucket by bucket, instead of asking
// the user to trust a single number.
func BucketSummary(w *Weights, top int) string {
	type row struct {
		feature, value string
		bucket         Bucket
	}
	var rows []row
	for feature, values := range w.Buckets {
		for value, bucket := range values {
			rows = append(rows, row{feature: feature, value: value, bucket: bucket})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		ni := rows[i].bucket.Ups + rows[i].bucket.Downs
		nj := rows[j].bucket.Ups + rows[j].bucket.Downs
		if ni != nj {
			return ni > nj
		}
		if rows[i].feature != rows[j].feature {
			return rows[i].feature < rows[j].feature
		}
		return rows[i].value < rows[j].value
	})

	var b strings.Builder
	for i, r := range rows {
		if i >= top {
			break
		}
		fmt.Fprintf(&b, "· %s=%s: %d 👍 / %d 👎\n", r.feature, r.value, r.bucket.Ups, r.bucket.Downs)
	}
	return b.String()
}
