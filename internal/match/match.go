// Package match implements three ranking strategies so their quality can be
// compared on the same data instead of argued about:
//
//	FuzzyOnly      the "just use fuzzywuzzy" approach — the baseline
//	TaxonomyBM25   canonical skill ids + BM25 + cosine, fused with RRF
//	Hybrid         TaxonomyBM25 + L0 hard filter + weighted dimension scoring
//
// Every strategy returns per-component scores, because in hiring a score you
// cannot explain is a score a recruiter will not trust.
package match

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/qifani35/ats-matcher/internal/model"
	"github.com/qifani35/ats-matcher/internal/taxonomy"
	"github.com/qifani35/ats-matcher/internal/textsim"
)

// Scored is one candidate's result for one job.
type Scored struct {
	CandidateID string
	Score       float64
	Parts       map[string]float64 // per-component, all in [0,1]
	Reason      string
	Filtered    string // non-empty means L0 hard filter rejected the candidate
}

// Strategy ranks all candidates for a job.
type Strategy struct {
	Name  string
	Rank  func(job model.Job, cands []model.Candidate, ds *model.Dataset) []Scored
	Notes string
}

// ---------------------------------------------------------------- helpers

// jobText flattens a job into searchable text.
func jobText(j model.Job) string {
	parts := []string{j.Title, j.Description, strings.Join(j.MustHave, " "), strings.Join(j.NiceToHave, " ")}
	return strings.Join(parts, " ")
}

// candText flattens a candidate into searchable text.
func candText(c model.Candidate) string {
	parts := []string{c.Title, c.Summary, strings.Join(c.Skills, " ")}
	return strings.Join(parts, " ")
}

// canonicalSkills resolves raw skill strings to canonical ids, keeping the
// unknown ones aside so the caller can decide what to do with them.
func canonicalSkills(raw []string) (known []string, unknown []string) {
	seen := map[string]bool{}
	for _, s := range raw {
		if id, ok := taxonomy.Canonical(s); ok {
			if !seen[id] {
				seen[id] = true
				known = append(known, id)
			}
		} else if strings.TrimSpace(s) != "" {
			unknown = append(unknown, s)
		}
	}
	return
}

// skillCoverage computes weighted coverage of the required and nice-to-have
// skills. Exact matches earn full credit; related skills (PostgreSQL for SQL,
// Kotlin for Java) earn partial credit instead of zero.
//
// It returns the fit in [0,1] plus which required skills were covered and which
// were missing, so the caller can explain the score to a recruiter.
func skillCoverage(required, nice []string, have map[string]bool) (fit float64, matched, missing []string) {
	if len(required) == 0 && len(nice) == 0 {
		return 1, nil, nil // nothing asked, nothing missing
	}

	var got, total float64

	credit := func(reqID string) float64 {
		if have[reqID] {
			return 1.0
		}
		for candID := range have {
			if taxonomy.Satisfies(candID, reqID) {
				return taxonomy.RelatedWeight
			}
		}
		return 0
	}

	for _, reqID := range required {
		total += 1.0
		c := credit(reqID)
		got += c
		if c > 0 {
			matched = append(matched, reqID)
		} else {
			missing = append(missing, reqID)
		}
	}
	for _, niceID := range nice {
		total += 0.35
		got += credit(niceID) * 0.35
	}

	if total == 0 {
		return 0, matched, missing
	}
	sort.Strings(matched)
	sort.Strings(missing)
	return got / total, matched, missing
}

// experienceFit compares candidate years against the requirement.
// Overqualified is not penalised hard — it is capped at 1.0.
func experienceFit(have, want int) float64 {
	if want <= 0 {
		return 1
	}
	return math.Min(1, float64(have)/float64(want))
}

// seniorityFit rewards a close match and mildly penalises a big gap.
func seniorityFit(jobSen, candTitle string) float64 {
	want := model.SeniorityRank(jobSen)
	if want == 0 {
		return 1
	}
	got := model.SeniorityRank(inferSeniority(candTitle))
	if got == 0 {
		return 0.5 // unknown title: neutral, not zero
	}
	diff := got - want
	switch {
	case diff == 0:
		return 1
	case diff > 0:
		return 0.85 // overqualified: still hireable
	case diff == -1:
		return 0.6 // one level below
	default:
		return 0.3
	}
}

func inferSeniority(title string) string {
	t := strings.ToLower(title)
	switch {
	case strings.Contains(t, "lead"), strings.Contains(t, "head"), strings.Contains(t, "principal"):
		return "lead"
	case strings.Contains(t, "senior"), strings.Contains(t, "sr."), strings.Contains(t, "sr "):
		return "senior"
	case strings.Contains(t, "junior"), strings.Contains(t, "jr."), strings.Contains(t, "intern"), strings.Contains(t, "fresh"):
		return "junior"
	default:
		return "mid"
	}
}

// recencyFit decays per year since the skill was last used.
func recencyFit(c model.Candidate, refYear int) float64 {
	if len(c.LastUsedYear) == 0 || refYear == 0 {
		return 0.7 // unknown: neutral-positive, never a penalty for missing data
	}
	var sum float64
	var n int
	for _, y := range c.LastUsedYear {
		gap := float64(refYear - y)
		if gap < 0 {
			gap = 0
		}
		sum += math.Exp(-gap / 3.0) // halves roughly every 2 years
		n++
	}
	if n == 0 {
		return 0.7
	}
	return sum / float64(n)
}

// locationFit is a cheap boolean-ish signal.
func locationFit(j model.Job, c model.Candidate) float64 {
	if j.Remote && c.WillingRemote {
		return 1
	}
	if strings.EqualFold(strings.TrimSpace(j.Location), strings.TrimSpace(c.Location)) {
		return 1
	}
	if j.Remote && !c.WillingRemote {
		return 0.4
	}
	return 0.15
}

// hardFilter is L0: cheap elimination before any scoring work.
//
// It deliberately does NOT filter on years of experience. "Minimum 4 years" is
// a recruiter preference, not a hard constraint, and dropping people on it is a
// classic ATS anti-pattern: you lose strong candidates who are one year short.
// Years are handled as a graded score in experienceFit instead.
//
// What IS hard: you cannot do an on-site job from another city, and you cannot
// do a remote job if you refuse remote work.
func hardFilter(j model.Job, c model.Candidate) string {
	// On-site job in a city the candidate is not in.
	if !j.Remote && !strings.EqualFold(strings.TrimSpace(j.Location), strings.TrimSpace(c.Location)) {
		return fmt.Sprintf("posisi on-site di %s, kandidat di %s", j.Location, c.Location)
	}
	// Remote job, but the candidate refuses remote and lives elsewhere.
	if j.Remote && !c.WillingRemote &&
		!strings.EqualFold(strings.TrimSpace(j.Location), strings.TrimSpace(c.Location)) {
		return "menolak remote dan bukan domisili yang sama"
	}
	return ""
}

func sortDesc(s []Scored) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Filtered == "" && s[j].Filtered != "" {
			return true // unfiltered ranks above filtered
		}
		if s[i].Filtered != "" && s[j].Filtered == "" {
			return false
		}
		return s[i].Score > s[j].Score
	})
}

// ------------------------------------------------------- strategy: fuzzy only

// FuzzyOnlyRank scores with fuzzywuzzy-style WRatio over raw text.
// This is the straw man: no skills, no structure, no filters.
func FuzzyOnlyRank(job model.Job, cands []model.Candidate, ds *model.Dataset) []Scored {
	qt := job.Title + " " + strings.Join(job.MustHave, " ")
	out := make([]Scored, 0, len(cands))
	for _, c := range cands {
		score := textsim.WRatio(qt, candText(c))
		out = append(out, Scored{
			CandidateID: c.ID,
			Score:       score,
			Parts:       map[string]float64{"fuzzy_wratio": score},
			Reason:      fmt.Sprintf("WRatio(job title+must-have, CV text) = %.2f", score),
		})
	}
	sortDesc(out)
	return out
}

// --------------------------------------------- strategy: taxonomy + BM25 + cos

// TaxonomyBM25Rank canonicalizes skills, then fuses BM25 and cosine with RRF.
func TaxonomyBM25Rank(job model.Job, cands []model.Candidate, ds *model.Dataset) []Scored {
	// Build the corpus of canonical skill tokens per candidate.
	corpus := make([][]string, len(cands))
	for i, c := range cands {
		known, _ := canonicalSkills(c.Skills)
		toks := make([]string, 0, len(known)+8)
		for _, id := range known {
			toks = append(toks, "sk:"+id)
		}
		// Keep title/summary words too, but down-weighted in effect by being
		// a minority of tokens.
		toks = append(toks, textsim.Tokenize(c.Title+" "+c.Summary)...)
		corpus[i] = toks
	}

	bm25 := textsim.NewBM25(corpus)

	// Query: canonical job skills as sk: tokens plus description words.
	must, _ := canonicalSkills(job.MustHave)
	nice, _ := canonicalSkills(job.NiceToHave)
	query := make([]string, 0, len(must)+len(nice)+16)
	for _, id := range must {
		query = append(query, "sk:"+id)
	}
	for _, id := range nice {
		query = append(query, "sk:"+id)
	}
	query = append(query, textsim.Tokenize(job.Title+" "+job.Description)...)

	// Two independent rankings, then fuse.
	bmRank := rankByIndex(len(cands), func(i int) float64 { return bm25.Score(query, i) })
	cosRank := rankByIndex(len(cands), func(i int) float64 { return textsim.Cosine(corpus, query, i) })

	bmIDs := idsInOrder(cands, bmRank)
	cosIDs := idsInOrder(cands, cosRank)
	fused := textsim.ReciprocalRankFusion([][]string{bmIDs, cosIDs}, 60)

	bmScore := make(map[string]float64, len(cands))
	cosScore := make(map[string]float64, len(cands))
	for i, c := range cands {
		bmScore[c.ID] = bm25.Score(query, i)
		cosScore[c.ID] = textsim.Cosine(corpus, query, i)
	}

	// Normalise each signal to [0,1] so the reported number is comparable.
	maxBM := maxOf(bmScore)
	out := make([]Scored, 0, len(cands))
	for pos, id := range fused {
		normBM := 0.0
		if maxBM > 0 {
			normBM = bmScore[id] / maxBM
		}
		out = append(out, Scored{
			CandidateID: id,
			// Score is rank-based so it stays comparable across jobs.
			Score: 1.0 / float64(pos+1),
			Parts: map[string]float64{
				"bm25":    normBM,
				"cosine":  cosScore[id],
				"rrf_pos": float64(pos + 1),
			},
			Reason: fmt.Sprintf("RRF pos %d (BM25 %.2f, cosine %.2f)", pos+1, normBM, cosScore[id]),
		})
	}
	return out
}

// -------------------------------------------------------- strategy: hybrid

// HybridRank is the full pipeline: L0 filter, canonical skills, BM25+cosine
// fused for ranking, then an explainable weighted score.
func HybridRank(job model.Job, cands []model.Candidate, ds *model.Dataset) []Scored {
	refYear := ds.ReferenceYear

	mustIDs, _ := canonicalSkills(job.MustHave)
	niceIDs, _ := canonicalSkills(job.NiceToHave)

	// Corpus for the text signals.
	corpus := make([][]string, len(cands))
	for i, c := range cands {
		known, _ := canonicalSkills(c.Skills)
		toks := make([]string, 0, len(known)+8)
		for _, id := range known {
			toks = append(toks, "sk:"+id)
		}
		// Expanding the candidate's skills means a PostgreSQL CV also carries
		// the "sql" token, so BM25 does not punish a specialisation.
		for _, id := range known {
			for _, rel := range taxonomy.Related(id) {
				toks = append(toks, "sk:"+rel)
			}
		}
		toks = append(toks, textsim.Tokenize(c.Title+" "+c.Summary)...)
		corpus[i] = toks
	}
	bm25 := textsim.NewBM25(corpus)
	query := make([]string, 0, len(mustIDs)+len(niceIDs)+16)
	for _, id := range mustIDs {
		query = append(query, "sk:"+id)
	}
	for _, id := range niceIDs {
		query = append(query, "sk:"+id)
	}
	query = append(query, textsim.Tokenize(job.Title+" "+job.Description)...)

	textRank := rankByIndex(len(cands), func(i int) float64 {
		return 0.6*normScore(bm25.Score(query, i), maxBM25(bm25, query, len(cands))) +
			0.4*textsim.Cosine(corpus, query, i)
	})
	textQuality := map[string]float64{}
	for pos, idx := range textRank {
		textQuality[cands[idx].ID] = 1.0 / float64(pos+1)
	}
	maxTQ := 0.0
	for _, v := range textQuality {
		if v > maxTQ {
			maxTQ = v
		}
	}

	out := make([]Scored, 0, len(cands))
	for _, c := range cands {
		blocked := hardFilter(job, c)
		haveKnown, unknown := canonicalSkills(c.Skills)
		haveSet := make(map[string]bool, len(haveKnown))
		for _, id := range haveKnown {
			haveSet[id] = true
		}

		skillFit, matched, missing := skillCoverage(mustIDs, niceIDs, haveSet)
		expFit := experienceFit(c.Years, job.MinYears)
		senFit := seniorityFit(job.Seniority, c.Title)
		recFit := recencyFit(c, refYear)
		locFit := locationFit(job, c)

		tq := 0.0
		if maxTQ > 0 {
			tq = textQuality[c.ID] / maxTQ
		}

		// Weights sum to 1. Skill fit dominates because it is what the
		// recruiter is actually screening on. Experience is graded, never a
		// pass/fail gate, so "one year short" costs a little instead of
		// everything.
		score := 0.40*skillFit +
			0.25*expFit +
			0.10*senFit +
			0.10*recFit +
			0.05*locFit +
			0.10*tq

		reason := fmt.Sprintf("skill %.2f (%d/%d must-have), exp %.2f, sen %.2f, rec %.2f, loc %.2f, text %.2f",
			skillFit, len(matched), len(mustIDs), expFit, senFit, recFit, locFit, tq)
		if len(missing) > 0 {
			names := make([]string, 0, len(missing))
			for _, id := range missing {
				names = append(names, taxonomy.Label(id))
			}
			reason += "; kurang: " + strings.Join(names, ", ")
		}
		if len(unknown) > 0 {
			reason += fmt.Sprintf("; skill di luar kamus: %s", strings.Join(unknown, ", "))
		}

		out = append(out, Scored{
			CandidateID: c.ID,
			Score:       score,
			Filtered:    blocked,
			Parts: map[string]float64{
				"skill":      skillFit,
				"experience": expFit,
				"seniority":  senFit,
				"recency":    recFit,
				"location":   locFit,
				"text":       tq,
			},
			Reason: reason,
		})
	}
	sortDesc(out)
	return out
}

// -------------------------------------------------------------- utilities

func rankByIndex(n int, score func(int) float64) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return score(idx[a]) > score(idx[b])
	})
	return idx
}

func idsInOrder(cands []model.Candidate, order []int) []string {
	out := make([]string, len(order))
	for i, idx := range order {
		out[i] = cands[idx].ID
	}
	return out
}

func maxOf(m map[string]float64) float64 {
	mx := 0.0
	for _, v := range m {
		if v > mx {
			mx = v
		}
	}
	return mx
}

func maxBM25(b *textsim.BM25, query []string, n int) float64 {
	mx := 0.0
	for i := 0; i < n; i++ {
		if s := b.Score(query, i); s > mx {
			mx = s
		}
	}
	return mx
}

func normScore(v, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return v / max
}

