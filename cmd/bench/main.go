// Command bench compares three candidate-matching strategies on the same
// labelled dataset and prints the numbers.
//
// It exists to answer one question with evidence instead of opinion:
// is fuzzy string matching enough for an ATS?
//
//	go run ./cmd/bench
//	go run ./cmd/bench -job J2 -top 8
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/qifani35/ats-matcher/internal/eval"
	"github.com/qifani35/ats-matcher/internal/match"
	"github.com/qifani35/ats-matcher/internal/model"
	"github.com/qifani35/ats-matcher/internal/taxonomy"
	"github.com/qifani35/ats-matcher/internal/textsim"
)

func main() {
	dataPath := flag.String("data", "data/dataset.json", "path to the benchmark dataset")
	inspect := flag.String("job", "", "inspect one job id in detail (e.g. J2)")
	top := flag.Int("top", 5, "how many candidates to show per strategy when inspecting")
	flag.Parse()

	ds, err := load(*dataPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load dataset: %v\n", err)
		os.Exit(1)
	}

	skillN, aliasN := taxonomy.Size()
	relCount := 0
	for _, l := range ds.Labels {
		if l.Relevant {
			relCount++
		}
	}

	fmt.Println("ATS matcher benchmark")
	fmt.Println(strings.Repeat("=", 78))
	fmt.Printf("vocabulary     : %d skills, %d aliases\n", skillN, aliasN)
	fmt.Printf("dataset        : %d jobs, %d candidates, %d relevant labels\n",
		len(ds.Jobs), len(ds.Candidates), relCount)
	fmt.Printf("reference year : %d (for recency decay)\n", ds.ReferenceYear)
	fmt.Println()

	demoFuzzyTraps()

	strategies := []match.Strategy{
		{Name: "fuzzy-only", Rank: match.FuzzyOnlyRank,
			Notes: "WRatio over raw text, no skills, no filters"},
		{Name: "taxonomy+bm25", Rank: match.TaxonomyBM25Rank,
			Notes: "canonical skill ids + BM25 + cosine, fused with RRF"},
		{Name: "hybrid", Rank: match.HybridRank,
			Notes: "L0 hard filter + weighted dimension scoring"},
	}

	resultsByStrategy := map[string][]eval.JobResult{}
	rankedByStrategy := map[string]map[string][]match.Scored{}
	summaries := make([]eval.Summary, 0, len(strategies))

	for _, s := range strategies {
		var results []eval.JobResult
		rankedByJob := map[string][]match.Scored{}
		for _, job := range ds.Jobs {
			ranked := s.Rank(job, ds.Candidates, ds)
			rankedByJob[job.ID] = ranked
			results = append(results, eval.Evaluate(job, ranked, ds.RelevantSet(job.ID)))
		}
		resultsByStrategy[s.Name] = results
		rankedByStrategy[s.Name] = rankedByJob
		summaries = append(summaries, eval.Summarize(s.Name, results, ds))
	}

	// Skill-evidence metric: top-5 candidates with zero real must-have coverage.
	names := ds.CandidateByID()
	skilllessCounts := map[string]int{}
	skilllessDetail := map[string]map[string][]string{}
	for _, st := range strategies {
		skilllessDetail[st.Name] = map[string][]string{}
		for _, job := range ds.Jobs {
			hits := skilllessTop(job, rankedByStrategy[st.Name][job.ID], names, 5)
			if len(hits) > 0 {
				skilllessDetail[st.Name][job.ID] = hits
				skilllessCounts[st.Name] += len(hits)
			}
		}
	}

	printComparison(summaries, strategies, skilllessCounts)
	printPerJob(ds, resultsByStrategy)
	printTraps(ds, resultsByStrategy)
	printSkilllessDetail(skilllessDetail)

	if *inspect != "" {
		inspectJob(ds, strategies, *inspect, *top)
	}

	fmt.Println()
	fmt.Println("Cara membaca tabel:")
	fmt.Println("  P@k           dari k kandidat teratas, berapa persen yang benar relevan")
	fmt.Println("  R@k           dari semua kandidat relevan, berapa persen masuk k teratas")
	fmt.Println("  MRR          1/posisi kandidat relevan pertama (1.00 = selalu di puncak)")
	fmt.Println("  MAP          rata-rata presisi di setiap titik recall")
	fmt.Println("  salah@5      jumlah kandidat TIDAK relevan di dalam 5 teratas (makin kecil makin baik)")
}

func load(path string) (*model.Dataset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ds model.Dataset
	if err := json.Unmarshal(b, &ds); err != nil {
		return nil, err
	}
	if ds.ReferenceYear == 0 {
		ds.ReferenceYear = 2026
	}
	return &ds, nil
}

// demoFuzzyTraps prints the similarity scores that make fuzzy matching unsafe
// for skills. These are the actual numbers from this implementation.
func demoFuzzyTraps() {
	fmt.Println("Kenapa fuzzy berbahaya untuk skill (skor WRatio dari kode ini)")
	fmt.Println(strings.Repeat("-", 78))
	pairs := [][2]string{
		{"java", "javascript"},
		{"c", "c++"},
		{"c", "c#"},
		{"ml", "machine learning"},
		{"react", "vue"},
		{"nodejs", "node.js"},
	}
	fmt.Printf("  %-28s %8s   %s\n", "pasangan", "skor", "kenyataan")
	fmt.Printf("  %-28s %8s   %s\n", strings.Repeat("-", 28), "------", strings.Repeat("-", 24))
	for _, p := range pairs {
		score := textsim.WRatio(p[0], p[1])
		fmt.Printf("  %-28s %7.2f   %s\n", fmt.Sprintf("%q vs %q", p[0], p[1]), score, verdict(p[0], p[1], score))
	}
	fmt.Println()
	fmt.Println("  Skor tinggi untuk pasangan yang BEDA artinya = kandidat salah masuk shortlist.")
	fmt.Println("  Karena itu skill harus dicocokkan lewat id kanonik, bukan jarak karakter.")
	fmt.Println()
}

func verdict(a, b string, score float64) string {
	sameSkill := map[string]bool{
		"nodejs|node.js":  true,
		"ml|machine learning": true,
	}
	key := a + "|" + b
	if sameSkill[key] {
		if score > 0.6 {
			return "BENAR (skill sama)"
		}
		return "SALAH (harusnya tinggi)"
	}
	if score > 0.5 {
		return "SALAH (beda skill, skor tinggi)"
	}
	return "benar (beda skill, skor rendah)"
}

func printComparison(sums []eval.Summary, strategies []match.Strategy, traps map[string]int) {
	fmt.Println("Perbandingan strategi (rata-rata seluruh job)")
	fmt.Println(strings.Repeat("=", 78))

	header := fmt.Sprintf("%-16s", "strategi")
	for _, k := range eval.KS {
		header += fmt.Sprintf(" %7s", fmt.Sprintf("P@%d", k))
	}
	header += fmt.Sprintf(" %7s %7s %9s %8s", "MRR", "MAP", "salah@5", "tanpa-skill@5")
	fmt.Println(header)
	fmt.Println(strings.Repeat("-", 78))

	for _, s := range sums {
		row := fmt.Sprintf("%-16s", s.StrategyName)
		for _, k := range eval.KS {
			row += fmt.Sprintf(" %7s", eval.FormatPct(s.PrecisionAt[k]))
		}
		row += fmt.Sprintf(" %7.3f %7.3f %9.2f %8d", s.MRR, s.MAP, s.Top5Wrong, traps[s.StrategyName])
		fmt.Println(row)
	}
	fmt.Println(strings.Repeat("-", 78))
	for _, st := range strategies {
		fmt.Printf("  %-16s %s\n", st.Name, st.Notes)
	}
	fmt.Println()
}

func printPerJob(ds *model.Dataset, byStrategy map[string][]eval.JobResult) {
	fmt.Println("MRR per job (1.00 = kandidat relevan selalu di posisi 1)")
	fmt.Println(strings.Repeat("-", 78))
	fmt.Printf("%-4s %-34s %11s %11s %11s %7s\n", "id", "judul", "fuzzy", "tax+bm25", "hybrid", "relevan")
	for _, job := range ds.Jobs {
		f := find(ds, byStrategy["fuzzy-only"], job.ID)
		t := find(ds, byStrategy["taxonomy+bm25"], job.ID)
		h := find(ds, byStrategy["hybrid"], job.ID)
		fmt.Printf("%-4s %-34s %11.3f %11.3f %11.3f %7d\n",
			job.ID, truncate(job.Title, 34), f.MRR, t.MRR, h.MRR, f.RelevantN)
	}
	fmt.Println()
}

// printTraps shows the specific false positives fuzzy matching produces, using
// the labelled data as ground truth.
func printTraps(ds *model.Dataset, byStrategy map[string][]eval.JobResult) {
	fmt.Println("Kandidat TIDAK relevan yang lolos ke 5 teratas (false positive)")
	fmt.Println(strings.Repeat("-", 78))

	names := ds.CandidateByID()
	fuzzy := byStrategy["fuzzy-only"]
	hybrid := byStrategy["hybrid"]

	var lines []string
	for i, job := range ds.Jobs {
		relevant := ds.RelevantSet(job.ID)
		badF := wrongInTop(fuzzy[i], relevant, 5)
		badH := wrongInTop(hybrid[i], relevant, 5)
		if len(badF) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s %s", job.ID, truncate(job.Title, 30)))
		lines = append(lines, fmt.Sprintf("      fuzzy  : %s", describe(names, badF)))
		if len(badH) > 0 {
			lines = append(lines, fmt.Sprintf("      hybrid : %s", describe(names, badH)))
		} else {
			lines = append(lines, "      hybrid : tidak ada")
		}
	}
	if len(lines) == 0 {
		fmt.Println("  (tidak ada)")
	} else {
		fmt.Println(strings.Join(lines, "\n"))
	}
	fmt.Println()
}

func wrongInTop(res eval.JobResult, relevant map[string]bool, k int) []string {
	var out []string
	limit := k
	if len(res.Ranked) < limit {
		limit = len(res.Ranked)
	}
	for _, id := range res.Ranked[:limit] {
		if !relevant[id] {
			out = append(out, id)
		}
	}
	return out
}

func describe(names map[string]model.Candidate, ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		c, ok := names[id]
		if !ok {
			parts = append(parts, id)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", id, c.Name, strings.Join(c.Skills, "/")))
	}
	return strings.Join(parts, "; ")
}

func inspectJob(ds *model.Dataset, strategies []match.Strategy, jobID string, top int) {
	var job model.Job
	found := false
	for _, j := range ds.Jobs {
		if j.ID == jobID {
			job, found = j, true
			break
		}
	}
	if !found {
		fmt.Printf("job %q tidak ditemukan\n", jobID)
		return
	}

	relevant := ds.RelevantSet(job.ID)
	names := ds.CandidateByID()

	fmt.Printf("Detail job %s — %s\n", job.ID, job.Title)
	fmt.Printf("  must-have: %s | nice: %s\n",
		strings.Join(job.MustHave, ", "), strings.Join(job.NiceToHave, ", "))
	fmt.Printf("  min %d tahun, %s, remote=%v\n", job.MinYears, job.Location, job.Remote)
	fmt.Printf("  kandidat relevan (label): %s\n\n", strings.Join(keys(relevant), ", "))

	for _, st := range strategies {
		ranked := st.Rank(job, ds.Candidates, ds)
		fmt.Printf("  %s\n", st.Name)
		limit := top
		if len(ranked) < limit {
			limit = len(ranked)
		}
		for i := 0; i < limit; i++ {
			s := ranked[i]
			marker := " "
			if relevant[s.CandidateID] {
				marker = "✓"
			}
			name := ds.Candidates[0].Name
			if c, ok := names[s.CandidateID]; ok {
				name = c.Name
			}
			state := ""
			if s.Filtered != "" {
				state = " [FILTERED: " + s.Filtered + "]"
			}
			fmt.Printf("   %s %d. %-5s %-10s %.3f  %s%s\n",
				marker, i+1, s.CandidateID, name, s.Score, truncate(s.Reason, 72), state)
		}
		fmt.Println()
	}
}

func find(ds *model.Dataset, results []eval.JobResult, jobID string) eval.JobResult {
	for _, r := range results {
		if r.JobID == jobID {
			return r
		}
	}
	return eval.JobResult{Precision: map[int]float64{}, Recall: map[int]float64{}, Scores: map[string]float64{}}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// skilllessTop counts, in the top-k, the candidates who cover NONE of the
// job's must-have skills (exact match or legitimate related skill).
//
// This is deliberately cause-independent: it does not care WHY the candidate
// ranked, only whether there is real skill evidence behind it. A ranker that
// works on raw text will surface people for generic word overlap ("developer",
// "internal"); a ranker that scores canonical skills cannot.
//
// When such a candidate happens to carry a confusable skill name (Java for a
// JavaScript job), that is annotated — it is the classic fuzzy failure mode.
func skilllessTop(job model.Job, ranked []match.Scored, names map[string]model.Candidate, k int) []string {
	var required []string
	for _, s := range job.MustHave {
		if id, ok := taxonomy.Canonical(s); ok {
			required = append(required, id)
		}
	}
	if len(required) == 0 {
		return nil
	}

	limit := k
	if len(ranked) < limit {
		limit = len(ranked)
	}

	var hits []string
	for _, sc := range ranked[:limit] {
		c, ok := names[sc.CandidateID]
		if !ok {
			continue
		}

		realMatch := false
		var confusable []string

		for _, raw := range c.Skills {
			cid, ok := taxonomy.Canonical(raw)
			if !ok {
				continue
			}
			for _, rid := range required {
				if taxonomy.Satisfies(cid, rid) {
					realMatch = true
					break
				}
			}
		}
		if realMatch {
			continue
		}

		// No real evidence. Note any confusable skill name for diagnosis.
		for _, raw := range c.Skills {
			cid, ok := taxonomy.Canonical(raw)
			if !ok {
				continue
			}
			for _, rid := range required {
				if cid == rid || taxonomy.Satisfies(cid, rid) {
					continue
				}
				if textsim.WRatio(taxonomy.Label(cid), taxonomy.Label(rid)) > 0.8 {
					confusable = append(confusable,
						fmt.Sprintf("punya %s≈%s", taxonomy.Label(cid), taxonomy.Label(rid)))
					break
				}
			}
			if len(confusable) > 0 {
				break
			}
		}

		label := sc.CandidateID
		if len(confusable) > 0 {
			label += " (" + confusable[0] + ")"
		}
		hits = append(hits, label)
	}
	return hits
}

// printTrapDetail shows the individual confusable-skill mistakes per job.
func printSkilllessDetail(detail map[string]map[string][]string) {
	fmt.Println("Top-5 TANPA skill must-have asli (tidak ada bukti skill)")
	fmt.Println(strings.Repeat("-", 78))

	order := []string{"fuzzy-only", "taxonomy+bm25", "hybrid"}
	any := false
	for _, name := range order {
		jobs := detail[name]
		if len(jobs) == 0 {
			fmt.Printf("  %-16s tidak ada\n", name)
			continue
		}
		any = true
		ids := make([]string, 0, len(jobs))
		for id := range jobs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		fmt.Printf("  %s\n", name)
		for _, id := range ids {
			fmt.Printf("    %s: %s\n", id, strings.Join(jobs[id], "; "))
		}
	}
	if !any {
		fmt.Println("  (tidak ada)")
	}
	fmt.Println()
	fmt.Println("  Metrik ini tidak peduli KENAPA kandidat naik, hanya apakah ada")
	fmt.Println("  bukti skill di belakangnya. Ranker berbasis teks akan memunculkan")
	fmt.Println("  orang karena kata generik seperti \"developer\"; ranker berbasis id")
	fmt.Println("  kanonik tidak bisa. Anotasi \"punya X≈Y\" menandai jebakan fuzzy klasik.")
	fmt.Println()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
