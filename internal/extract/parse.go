// Field parsing: turn raw CV text into a model.Candidate.
//
// Everything here is deterministic and explainable on purpose. No LLM: a
// recruiter must be able to ask "why did it read 5 years?" and get an answer
// that is a rule, not a guess. The LLM belongs one layer up, for summaries.
package extract

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/qifani35/ats-matcher/internal/model"
	"github.com/qifani35/ats-matcher/internal/taxonomy"
)

// Parsed bundles the candidate with what the parser noticed, so the UI can
// show confidence and a human can correct it.
type Parsed struct {
	Candidate model.Candidate
	Doc       Document
	// Notes explains each guess: "pengalaman: dari rentang 2019-2023 (4 th)"
	Notes []string
}

// cities are the locations we recognise. Extend per market.
var cities = []string{
	"Jakarta", "Bandung", "Surabaya", "Yogyakarta", "Semarang", "Medan",
	"Makassar", "Denpasar", "Bali", "Tangerang", "Bekasi", "Depok", "Bogor",
	"Malang", "Palembang", "Batam", "Solo", "Pekanbaru", "Balikpapan",
	"Singapore", "Kuala Lumpur", "Remote",
}

// roleWords help find the candidate's title line.
var roleWords = []string{
	"engineer", "developer", "programmer", "analyst", "designer", "manager",
	"konsultan", "consultant", "specialist", "administrator", "supervisor",
	"lead", "architect", "tester", "qa", "devops", "scientist", "intern",
	"staff", "officer", "head of", "direktur", "director",
}

var (
	yearsExplicit = regexp.MustCompile(`(?i)(\d{1,2})\s*(?:\+|plus)?\s*(?:tahun|thn|th|years?|yrs?)`)
	yearRange     = regexp.MustCompile(`(?i)(19|20)(\d{2})\s*[-–—~to]+\s*(?:(19|20)(\d{2})|sekarang|now|present|current)`)
	emailRe       = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	phoneRe       = regexp.MustCompile(`(?:\+62|62|0)8[0-9\-\s]{7,14}`)
	singleYear    = regexp.MustCompile(`\b(19|20)\d{2}\b`)
)

// ParseDocument builds a candidate from one extracted document.
// The id is derived from the filename so results are reproducible.
func ParseDocument(doc Document, refYear int, idFromName func(string) string) Parsed {
	if refYear == 0 {
		refYear = 2026
	}

	p := Parsed{Doc: doc}
	text := doc.Text
	lines := strings.Split(text, "\n")

	p.Candidate.ID = idFromName(doc.Path)

	// A file with no text layer tells us nothing. Guessing a name, location or
	// seniority here would fabricate data — worse than returning an empty
	// candidate that the caller can route to OCR.
	if doc.Kind == KindScan || doc.Kind == "error" || strings.TrimSpace(text) == "" {
		p.Notes = append(p.Notes, "tidak ada teks: perlu OCR sebelum diparse")
		return p
	}

	name, nameNote := guessName(lines)
	p.Candidate.Name = name
	if nameNote != "" {
		p.Notes = append(p.Notes, nameNote)
	}

	title, titleNote := guessTitle(lines)
	p.Candidate.Title = title
	if titleNote != "" {
		p.Notes = append(p.Notes, titleNote)
	}

	years, yearNote := guessYears(text)
	p.Candidate.Years = years
	p.Notes = append(p.Notes, yearNote)

	p.Candidate.Location = guessLocation(text)
	if p.Candidate.Location == "" {
		p.Candidate.Location = "Jakarta" // market default; flagged in Notes
		p.Notes = append(p.Notes, "lokasi: tidak terdeteksi, default Jakarta (perlu konfirmasi)")
	}

	p.Candidate.WillingRemote = strings.Contains(strings.ToLower(text), "remote")

	skills, lastYear := detectSkills(text, refYear)
	p.Candidate.Skills = skills
	p.Candidate.LastUsedYear = lastYear
	p.Candidate.Summary = summarize(text)

	return p
}

// guessName looks at the first few lines for something shaped like a person's
// name. CVs put the name first, but headers with logos often start with noise.
func guessName(lines []string) (string, string) {
	for i, raw := range lines {
		if i > 6 {
			break
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// "Nama: Budi Santoso"
		if idx := strings.Index(strings.ToLower(line), "nama"); idx >= 0 && idx < 6 {
			if parts := strings.SplitN(line, ":", 2); len(parts) == 2 {
				if n := strings.TrimSpace(parts[1]); n != "" {
					return n, "nama: dari baris 'Nama:'"
				}
			}
		}
		if !looksLikeName(line) {
			continue
		}
		return line, "nama: baris pertama yang berbentuk nama"
	}
	return "", "nama: tidak terdeteksi"
}

func looksLikeName(line string) bool {
	if len(line) > 42 || len(line) < 3 {
		return false
	}
	if emailRe.MatchString(line) || phoneRe.MatchString(line) {
		return false
	}
	if strings.ContainsAny(line, "@:/\\|") {
		return false
	}
	lower := strings.ToLower(line)
	for _, w := range roleWords {
		if strings.Contains(lower, w) {
			return false
		}
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || len(fields) > 4 {
		return false
	}
	for _, f := range fields {
		for _, r := range f {
			if r >= '0' && r <= '9' {
				return false
			}
		}
	}
	// At least two words must start with an uppercase letter.
	upper := 0
	for _, f := range fields {
		r := []rune(f)[0]
		if r >= 'A' && r <= 'Z' {
			upper++
		}
	}
	return upper >= 2
}

// guessTitle finds the most likely professional headline.
func guessTitle(lines []string) (string, string) {
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || len(line) > 70 {
			continue
		}
		if emailRe.MatchString(line) || phoneRe.MatchString(line) {
			continue
		}
		lower := strings.ToLower(line)
		for _, w := range roleWords {
			if strings.Contains(lower, w) {
				return line, "judul: baris pertama yang mengandung kata peran (" + w + ")"
			}
		}
	}
	return "", "judul: tidak terdeteksi"
}

// guessYears estimates total professional experience, preferring an explicit
// statement and falling back to summing date ranges.
func guessYears(text string) (int, string) {
	explicit := -1
	for _, m := range yearsExplicit.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > explicit && n <= 45 {
			explicit = n
		}
	}

	spanTotal := 0
	spans := yearRange.FindAllStringSubmatch(text, -1)
	for _, m := range spans {
		start, _ := strconv.Atoi(m[1] + m[2])
		end := 0
		if m[3] != "" && m[4] != "" {
			end, _ = strconv.Atoi(m[3] + m[4])
		} else {
			end = currentYearGuess(text)
		}
		if start > 0 && end >= start && end-start < 50 {
			spanTotal += end - start
		}
	}

	switch {
	case explicit >= 0 && spanTotal > 0:
		if explicit >= spanTotal {
			return explicit, "pengalaman: pernyataan eksplisit (" + strconv.Itoa(explicit) + " tahun)"
		}
		return spanTotal, "pengalaman: jumlah rentang tahun (" + strconv.Itoa(spanTotal) + " tahun)"
	case explicit >= 0:
		return explicit, "pengalaman: pernyataan eksplisit (" + strconv.Itoa(explicit) + " tahun)"
	case spanTotal > 0:
		return spanTotal, "pengalaman: jumlah rentang tahun (" + strconv.Itoa(spanTotal) + " tahun)"
	default:
		return 0, "pengalaman: tidak terdeteksi (0 tahun)"
	}
}

// currentYearGuess takes the largest year mentioned, which is a decent proxy
// for "present" in a CV that lists ongoing roles.
func currentYearGuess(text string) int {
	max := 0
	for _, m := range singleYear.FindAllString(text, -1) {
		if n, err := strconv.Atoi(m); err == nil && n > max && n <= 2100 {
			max = n
		}
	}
	if max == 0 {
		return 2026
	}
	return max
}

func guessLocation(text string) string {
	lower := strings.ToLower(text)
	for _, c := range cities {
		if strings.Contains(lower, strings.ToLower(c)) {
			return c
		}
	}
	return ""
}

// detectSkills scans the text for taxonomy aliases.
//
// Longest alias first, and each match is blanked out so a shorter alias cannot
// match inside it: without that, "machine learning" would also yield "ml" and
// "c++" would also yield "c".
func detectSkills(text string, refYear int) ([]string, map[string]int) {
	work := []byte(strings.ToLower(text))
	lower := string(work)

	seen := map[string]bool{}
	lastYear := map[string]int{}

	for _, a := range taxonomy.Aliases() {
		needle := strings.ToLower(a.Text)
		if len(needle) == 0 {
			continue
		}
		for {
			idx := indexWithBoundary(lower, needle)
			if idx < 0 {
				break
			}
			if !seen[a.ID] {
				seen[a.ID] = true
				// Record the latest year mentioned near this mention.
				lastYear[a.Text] = nearestYear(lower, idx, len(needle), refYear)
			}
			// Blank the match so shorter aliases cannot re-match inside it.
			for i := idx; i < idx+len(needle); i++ {
				work[i] = ' '
			}
			lower = string(work)
		}
	}

	skills := make([]string, 0, len(seen))
	for id := range seen {
		// Store the canonical label so downstream code sees a clean name.
		skills = append(skills, taxonomy.Label(id))
	}
	sort.Strings(skills)

	// Map back to label-keyed last-used years for the matcher.
	byLabel := map[string]int{}
	for alias, year := range lastYear {
		if id, ok := taxonomy.Canonical(alias); ok {
			lbl := taxonomy.Label(id)
			if year > byLabel[lbl] {
				byLabel[lbl] = year
			}
		}
	}
	return skills, byLabel
}

// indexWithBoundary finds needle in haystack where the match is not part of a
// longer alphanumeric token. "c" must not match inside "c++" or "react".
func indexWithBoundary(haystack, needle string) int {
	from := 0
	for {
		rel := strings.Index(haystack[from:], needle)
		if rel < 0 {
			return -1
		}
		idx := from + rel
		end := idx + len(needle)

		beforeOK := idx == 0 || !isAlnum(haystack[idx-1])
		afterOK := end >= len(haystack) || !isAlnum(haystack[end])

		if beforeOK && afterOK {
			return idx
		}
		from = idx + 1
		if from >= len(haystack) {
			return -1
		}
	}
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// nearestYear finds the closest 4-digit year within a window around a match.
func nearestYear(text string, idx, length, fallback int) int {
	start := idx - 90
	if start < 0 {
		start = 0
	}
	end := idx + length + 90
	if end > len(text) {
		end = len(text)
	}
	window := text[start:end]

	best := 0
	bestDist := 1 << 30
	for _, loc := range singleYear.FindAllStringIndex(window, -1) {
		y, err := strconv.Atoi(window[loc[0]:loc[1]])
		if err != nil || y < 1990 || y > 2100 {
			continue
		}
		dist := abs(loc[0] - (idx - start))
		if dist < bestDist {
			bestDist = dist
			best = y
		}
	}
	if best == 0 {
		return fallback
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// summarize takes the longest sentence-ish line as the summary.
func summarize(text string) string {
	lines := strings.Split(text, "\n")
	best := ""
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if len(l) > 40 && len(l) < 200 && !emailRe.MatchString(l) && !phoneRe.MatchString(l) {
			if len(l) > len(best) {
				best = l
			}
		}
	}
	if best == "" && len(lines) > 1 {
		best = strings.TrimSpace(lines[1])
	}
	return best
}

// IDFromFilename makes a stable, readable id from a path.
func IDFromFilename(path string) string {
	base := path
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "cv"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
