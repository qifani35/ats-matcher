// Package eval scores a ranked list against human relevance labels.
//
// The point of these numbers is to make the argument falsifiable. "Fuzzy
// matching feels fine" becomes "fuzzy matching puts 2.4 irrelevant people in
// the top 5", which a recruiter can act on.
package eval

import (
	"fmt"
	"sort"

	"github.com/qifani35/ats-matcher/internal/match"
	"github.com/qifani35/ats-matcher/internal/model"
)

// JobResult is the outcome for one job.
type JobResult struct {
	JobID       string
	JobTitle    string
	Ranked      []string // candidate ids, best first
	Scores      map[string]float64
	Precision   map[int]float64 // k -> precision@k
	Recall      map[int]float64 // k -> recall@k
	MRR         float64
	RelevantN   int
	FilteredN   int
	Top5Wrong   int // irrelevant candidates inside the top 5
	Top5Missing int // relevant candidates pushed below 5 or filtered out
}

// Summary aggregates across jobs.
type Summary struct {
	StrategyName string
	Jobs         int
	MAP          float64 // mean average precision
	MRR          float64
	PrecisionAt  map[int]float64
	RecallAt     map[int]float64
	Top5Wrong    float64 // average irrelevant in top 5 (lower is better)
}

// KS is the list of cut-offs reported.
var KS = []int{1, 3, 5, 10}

// Evaluate runs one job's ranking through the metric functions.
func Evaluate(job model.Job, ranked []match.Scored, relevant map[string]bool) JobResult {
	res := JobResult{
		JobID:     job.ID,
		JobTitle:  job.Title,
		RelevantN: len(relevant),
		Precision: map[int]float64{},
		Recall:    map[int]float64{},
		Scores:    map[string]float64{},
		Ranked:    make([]string, 0, len(ranked)),
	}

	for _, s := range ranked {
		res.Ranked = append(res.Ranked, s.CandidateID)
		res.Scores[s.CandidateID] = s.Score
		if s.Filtered != "" {
			res.FilteredN++
		}
	}

	// Precision@k and recall@k.
	for _, k := range KS {
		hits := 0
		limit := k
		if limit > len(res.Ranked) {
			limit = len(res.Ranked)
		}
		for _, id := range res.Ranked[:limit] {
			if relevant[id] {
				hits++
			}
		}
		if k > 0 {
			res.Precision[k] = float64(hits) / float64(k)
		}
		if res.RelevantN > 0 {
			res.Recall[k] = float64(hits) / float64(res.RelevantN)
		}
	}

	// MRR: reciprocal rank of the first relevant hit.
	for i, id := range res.Ranked {
		if relevant[id] {
			res.MRR = 1.0 / float64(i+1)
			break
		}
	}

	// Top-5 diagnostics: the numbers a recruiter actually feels.
	top := 5
	if len(res.Ranked) < top {
		top = len(res.Ranked)
	}
	inTop := map[string]bool{}
	for _, id := range res.Ranked[:top] {
		inTop[id] = true
		if !relevant[id] {
			res.Top5Wrong++
		}
	}
	for id := range relevant {
		if !inTop[id] {
			res.Top5Missing++
		}
	}

	return res
}

// AveragePrecision is the area under the precision/recall curve for one job.
func AveragePrecision(res JobResult, relevant map[string]bool) float64 {
	if res.RelevantN == 0 {
		return 0
	}
	hits := 0
	sum := 0.0
	for i, id := range res.Ranked {
		if relevant[id] {
			hits++
			sum += float64(hits) / float64(i+1)
		}
	}
	return sum / float64(res.RelevantN)
}

// Summarize aggregates per-job results into one row of the comparison table.
func Summarize(name string, results []JobResult, ds *model.Dataset) Summary {
	s := Summary{
		StrategyName: name,
		Jobs:         len(results),
		PrecisionAt:  map[int]float64{},
		RecallAt:     map[int]float64{},
	}
	if len(results) == 0 {
		return s
	}

	var mrrSum, apSum, wrongSum float64
	for _, r := range results {
		mrrSum += r.MRR
		apSum += AveragePrecision(r, ds.RelevantSet(r.JobID))
		wrongSum += float64(r.Top5Wrong)
	}
	s.MRR = mrrSum / float64(len(results))
	s.MAP = apSum / float64(len(results))
	s.Top5Wrong = wrongSum / float64(len(results))

	for _, k := range KS {
		var p, r float64
		for _, res := range results {
			p += res.Precision[k]
			r += res.Recall[k]
		}
		s.PrecisionAt[k] = p / float64(len(results))
		s.RecallAt[k] = r / float64(len(results))
	}
	return s
}

// Failures lists the jobs where a strategy missed the most relevant people,
// so the comparison table cannot hide a bad job behind a good average.
func Failures(results []JobResult, limit int) []JobResult {
	cp := append([]JobResult(nil), results...)
	sort.SliceStable(cp, func(i, j int) bool {
		return cp[i].MRR < cp[j].MRR
	})
	if limit > 0 && len(cp) > limit {
		cp = cp[:limit]
	}
	return cp
}

// FormatPct renders a 0..1 value as a fixed-width percentage.
func FormatPct(v float64) string { return fmt.Sprintf("%5.1f%%", v*100) }
