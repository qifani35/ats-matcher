// Package model holds the domain types the matcher works on.
//
// Deliberately flat: in a real ATS these come from the database, and the
// matcher should never touch the DB directly — it takes plain structs so it
// can be unit-tested and benchmarked without a server.
package model

// Job is a requisition, not a posting. One requisition can have many postings.
type Job struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Seniority   string   `json:"seniority"` // junior | mid | senior | lead
	MinYears    int      `json:"min_years"`
	Location    string   `json:"location"`
	Remote      bool     `json:"remote"`
	MustHave    []string `json:"must_have"`  // raw skill strings
	NiceToHave  []string `json:"nice_to_have"`
}

// Candidate is one person's parsed profile. Skills are raw strings exactly as
// they appeared in the CV, so the matcher has to canonicalize them itself —
// that is the point of the exercise.
type Candidate struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Title         string         `json:"title"`
	Summary       string         `json:"summary"`
	Years         int            `json:"years"`
	Location      string         `json:"location"`
	WillingRemote bool           `json:"willing_remote"`
	Skills        []string       `json:"skills"`
	// LastUsedYear maps a raw skill to the year it was last used, for recency.
	// Optional; a missing entry means "unknown".
	LastUsedYear map[string]int `json:"last_used_year,omitempty"`
}

// Dataset is the benchmark input: jobs, candidates, and human relevance labels.
type Dataset struct {
	// ReferenceYear anchors the recency calculation so results are stable.
	ReferenceYear int         `json:"reference_year"`
	Jobs          []Job       `json:"jobs"`
	Candidates    []Candidate `json:"candidates"`
	Labels        []Label     `json:"labels"`
}

// Label is one judged pair. Unlisted pairs are treated as NOT relevant, which
// matches how a recruiter actually reviews a shortlist.
type Label struct {
	JobID       string `json:"job_id"`
	CandidateID string `json:"candidate_id"`
	Relevant    bool   `json:"relevant"`
	// Why is the human's one-line justification. It makes disagreements
	// debuggable instead of mysterious.
	Why string `json:"why,omitempty"`
}

// RelevantSet returns the candidate IDs judged relevant for a job.
func (d *Dataset) RelevantSet(jobID string) map[string]bool {
	out := map[string]bool{}
	for _, l := range d.Labels {
		if l.JobID == jobID && l.Relevant {
			out[l.CandidateID] = true
		}
	}
	return out
}

// CandidateByID indexes candidates for fast lookup.
func (d *Dataset) CandidateByID() map[string]Candidate {
	out := make(map[string]Candidate, len(d.Candidates))
	for _, c := range d.Candidates {
		out[c.ID] = c
	}
	return out
}

// SeniorityRank turns a label into a comparable number.
func SeniorityRank(s string) int {
	switch s {
	case "junior":
		return 1
	case "mid":
		return 2
	case "senior":
		return 3
	case "lead":
		return 4
	default:
		return 0
	}
}
