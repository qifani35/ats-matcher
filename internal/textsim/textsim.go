// Package textsim holds the text-matching primitives.
//
// Three families live here, and the whole point of the benchmark is that they
// are NOT interchangeable:
//
//	Fuzzy       character-level. Great for typos and duplicate detection.
//	BM25        term-level, IDF-weighted. The workhorse for keyword search.
//	Cosine      vector-space. Used here over TF-IDF; swap in real embeddings
//	            without changing the interface.
package textsim

import (
	"math"
	"sort"
	"strings"
)

// ---------------------------------------------------------------- tokenizer

// stopwords are dropped before indexing. Kept small on purpose: over-filtering
// hurts recall on short CV text.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"to": true, "in": true, "for": true, "with": true, "on": true, "at": true,
	"by": true, "as": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "dan": true, "atau": true, "yang": true,
	"di": true, "ke": true, "dari": true, "untuk": true, "dengan": true,
	"pada": true, "saya": true, "kami": true, "ini": true, "itu": true,
}

// Tokenize lowercases and splits on anything that is not a letter, digit, or
// one of + # . / — because "c++", "c#", "node.js" and "ci/cd" must survive.
func Tokenize(s string) []string {
	var tokens []string
	var buf strings.Builder

	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
			r == '+' || r == '#' || r == '.' || r == '/' || r == '-' || r == '_':
			buf.WriteRune(r)
		default:
			if t := cleanToken(buf.String()); t != "" {
				tokens = append(tokens, t)
			}
			buf.Reset()
		}
	}
	if t := cleanToken(buf.String()); t != "" {
		tokens = append(tokens, t)
	}
	return tokens
}

// isShortButReal keeps meaningful 1-character tokens such as "c" (the
// language) while dropping noise.
func isShortButReal(t string) bool { return t == "c" || t == "r" || t == "go" }

func cleanToken(t string) string {
	t = strings.Trim(t, "./-_")
	if t == "" {
		return ""
	}
	// Keep the single letters that are real skills; drop other 1-char noise.
	if len(t) == 1 && !isShortButReal(t) {
		return ""
	}
	if stopwords[t] {
		return ""
	}
	// Strip a trailing "." only, so "node.js" stays "node.js" but "etc." -> "etc".
	return strings.TrimSuffix(t, ".")
}

// ------------------------------------------------------------------- fuzzy

// Levenshtein returns the edit distance between two strings.
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := 0; j <= len(rb); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// Ratio is the fuzzywuzzy-style similarity in [0,1] based on edit distance.
func Ratio(a, b string) float64 {
	if a == "" && b == "" {
		return 1
	}
	maxLen := len([]rune(a))
	if n := len([]rune(b)); n > maxLen {
		maxLen = n
	}
	if maxLen == 0 {
		return 1
	}
	return 1 - float64(Levenshtein(a, b))/float64(maxLen)
}

// TokenSetRatio mirrors fuzzywuzzy's token_set_ratio: compare the sorted
// intersection against each remainder, and take the best of three ratios.
// This is the function that makes "java" score highly against "javascript",
// which is exactly why it must not drive skill matching.
func TokenSetRatio(a, b string) float64 {
	ta := uniqueSorted(Tokenize(a))
	tb := uniqueSorted(Tokenize(b))

	inter, onlyA, onlyB := intersect(ta, tb)

	s1 := strings.Join(inter, " ")
	s2 := strings.TrimSpace(s1 + " " + strings.Join(onlyA, " "))
	s3 := strings.TrimSpace(s1 + " " + strings.Join(onlyB, " "))

	return math.Max(Ratio(s1, s2), math.Max(Ratio(s1, s3), Ratio(s2, s3)))
}

// PartialRatio is the best windowed match, mirroring fuzzywuzzy's
// partial_ratio: slide a CHARACTER window the length of the shorter string
// across the longer one and keep the best ratio.
//
// This must operate on characters, not tokens. "java" vs "javascript" gets
// 1.00 here precisely because the substring "java" appears verbatim — which is
// the whole reason fuzzywuzzy is unsafe for skill matching.
func PartialRatio(a, b string) float64 {
	ra, rb := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	short, long := ra, rb
	if len(short) > len(long) {
		short, long = long, short
	}
	best := 0.0
	for i := 0; i+len(short) <= len(long); i++ {
		window := string(long[i : i+len(short)])
		if r := Ratio(string(short), window); r > best {
			best = r
		}
	}
	return best
}

// WRatio combines the variants the way fuzzywuzzy's WRatio does: start from
// the best of ratio/token_set, then let partial_ratio compete, damped by 0.9
// when the two inputs differ a lot in length.
func WRatio(a, b string) float64 {
	if len(strings.TrimSpace(a)) == 0 || len(strings.TrimSpace(b)) == 0 {
		return 0
	}

	base := Ratio(strings.ToLower(a), strings.ToLower(b))
	if ts := TokenSetRatio(a, b); ts > base {
		base = ts
	}

	la, lb := len([]rune(a)), len([]rune(b))
	short, long := la, lb
	if short > long {
		short, long = long, short
	}
	if short > 0 {
		lenRatio := float64(long) / float64(short)
		weight := 1.0
		if lenRatio > 1.5 {
			weight = 0.9 // fuzzywuzzy's dampening for very different lengths
		}
		if pr := PartialRatio(a, b) * weight; pr > base {
			base = pr
		}
	}
	return base
}

func uniqueSorted(t []string) []string {
	set := make(map[string]bool, len(t))
	for _, s := range t {
		set[s] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func intersect(a, b []string) (common, onlyA, onlyB []string) {
	setB := make(map[string]bool, len(b))
	for _, s := range b {
		setB[s] = true
	}
	setA := make(map[string]bool, len(a))
	for _, s := range a {
		setA[s] = true
	}
	for _, s := range a {
		if setB[s] {
			common = append(common, s)
		} else {
			onlyA = append(onlyA, s)
		}
	}
	for _, s := range b {
		if !setA[s] {
			onlyB = append(onlyB, s)
		}
	}
	return
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// -------------------------------------------------------------------- BM25

// BM25 is the Okapi BM25 scorer over a fixed corpus.
type BM25 struct {
	docs  []map[string]int // term frequencies per document
	lens  []int            // document lengths
	df    map[string]int   // document frequency per term
	avgdl float64
	n     int
	k1, b float64
}

// NewBM25 indexes the given documents (already tokenized).
func NewBM25(docs [][]string) *BM25 {
	idx := &BM25{
		docs: make([]map[string]int, len(docs)),
		lens: make([]int, len(docs)),
		df:   make(map[string]int),
		n:    len(docs),
		k1:   1.5,
		b:    0.75,
	}
	total := 0
	for i, toks := range docs {
		tf := make(map[string]int, len(toks))
		for _, t := range toks {
			tf[t]++
		}
		idx.docs[i] = tf
		idx.lens[i] = len(toks)
		total += len(toks)
		for t := range tf {
			idx.df[t]++
		}
	}
	if idx.n > 0 {
		idx.avgdl = float64(total) / float64(idx.n)
	}
	return idx
}

// IDF is the BM25 idf with the +0.5 smoothing, floored at 0 so terms that
// appear in every document contribute nothing instead of negative scores.
func (idx *BM25) IDF(term string) float64 {
	n := float64(idx.n)
	df := float64(idx.df[term])
	idf := math.Log(1 + (n-df+0.5)/(df+0.5))
	if idf < 0 {
		return 0
	}
	return idf
}

// Score ranks one document against a tokenized query.
func (idx *BM25) Score(query []string, doc int) float64 {
	if doc < 0 || doc >= len(idx.docs) || idx.avgdl == 0 {
		return 0
	}
	tf := idx.docs[doc]
	dl := float64(idx.lens[doc])
	var score float64
	for _, q := range query {
		f := float64(tf[q])
		if f == 0 {
			continue
		}
		denom := f + idx.k1*(1-idx.b+idx.b*dl/idx.avgdl)
		score += idx.IDF(q) * (f * (idx.k1 + 1)) / denom
	}
	return score
}

// ----------------------------------------------------------- TF-IDF cosine

// Cosine scores a query against a document using TF-IDF weighted vectors.
// It is a stand-in for embedding cosine similarity: same interface, much
// weaker semantics. Replacing it with real embeddings is a one-function change.
func Cosine(corpus [][]string, query []string, doc int) float64 {
	if doc < 0 || doc >= len(corpus) {
		return 0
	}
	idf := documentFrequencies(corpus)

	qVec := tfidfVector(query, idf)
	dVec := tfidfVector(corpus[doc], idf)
	return cosineOf(qVec, dVec)
}

func documentFrequencies(corpus [][]string) map[string]float64 {
	df := make(map[string]float64, 256)
	n := float64(len(corpus))
	for _, doc := range corpus {
		seen := make(map[string]bool, len(doc))
		for _, t := range doc {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	idf := make(map[string]float64, len(df))
	for t, c := range df {
		idf[t] = math.Log((n + 1) / (c + 1))
	}
	return idf
}

func tfidfVector(tokens []string, idf map[string]float64) map[string]float64 {
	tf := make(map[string]float64, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}
	v := make(map[string]float64, len(tf))
	for t, c := range tf {
		v[t] = (1 + math.Log(c)) * idf[t]
	}
	return v
}

func cosineOf(a, b map[string]float64) float64 {
	var dot, na, nb float64
	for t, av := range a {
		na += av * av
		if bv, ok := b[t]; ok {
			dot += av * bv
		}
	}
	for _, bv := range b {
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ------------------------------------------------------- rank fusion (RRF)

// ReciprocalRankFusion merges several ranked lists into one.
// RRF(d) = sum over lists of 1 / (k + rank(d)), k = 60 by convention.
// It needs no score calibration between lists, which is why hybrid search uses it.
func ReciprocalRankFusion(lists [][]string, k int) []string {
	if k <= 0 {
		k = 60
	}
	scores := map[string]float64{}
	order := []string{}

	for _, list := range lists {
		for rank, id := range list {
			if _, seen := scores[id]; !seen {
				order = append(order, id)
			}
			scores[id] += 1.0 / float64(k+rank+1)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return scores[order[i]] > scores[order[j]]
	})
	return order
}
