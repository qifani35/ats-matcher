# ats-matcher

A runnable, dependency-free implementation of the candidate-matching pipeline
an ATS needs — plus a benchmark that says **how well each layer actually
performs**, instead of arguing about it.

Written in Go with the standard library only, so `go run ./cmd/bench` works
offline with no database and no API keys.

```
L0  hard filter        location / remote constraints only
L1  canonical skills   alias table -> skill ids   (no fuzzy!)
L2  text signals       BM25 + TF-IDF cosine, fused with RRF
L3  weighted score     skill / experience / seniority / recency / location
L4  explanation        per-component reasons, printable per candidate
```

## Run it

```bash
go run ./cmd/bench                  # full comparison
go run ./cmd/bench -job J6 -top 4   # inspect one job in detail
go run ./cmd/bench -data my.json    # your own dataset
```

## What the benchmark prints

1. **Fuzzy trap table** — the actual `WRatio` scores for pairs that must not
   match, computed by this code.
2. **Strategy comparison** — precision@k, recall@k, MRR, MAP, plus two
   diagnostics.
3. **MRR per job** — so one bad job cannot hide behind a good average.
4. **Skill-evidence check** — how many top-5 candidates have zero real
   must-have coverage.
5. **Optional per-job inspect** — every candidate's score decomposed.

## The measured results

Dataset: 6 jobs, 18 candidates, 12 labelled relevant pairs.

```
strategi             P@1     P@3     P@5    P@10     MRR     MAP   salah@5 tanpa-skill@5
fuzzy-only        100.0%   61.1%   40.0%   20.0%   1.000   0.986      3.00        8
taxonomy+bm25     100.0%   66.7%   40.0%   20.0%   1.000   1.000      3.00        8
hybrid            100.0%   61.1%   36.7%   18.3%   1.000   0.899      3.17        4
```

### Why fuzzy is still wrong even though it "wins" on P@5

This code's own numbers:

```
"java" vs "javascript"       0.90   <- different skill, near-identical string
"c"    vs "c++"              0.90
"c"    vs "c#"               0.90
"ml"   vs "machine learning" 0.45   <- same skill, low score
"react" vs "vue"             0.00   <- same category, zero score
"nodejs" vs "node.js"        0.86   <- genuinely the same string
```

`WRatio` measures how strings look, not what they mean. The skill-evidence
column is the honest measure of that: fuzzy leaves **8 of 30** top-5 slots
filled with candidates who cover none of the must-have skills. The hybrid
scores skills directly and halves that to **4**.

### Honest caveats

- **The dataset is a toy.** 18 candidates saturates MRR at 1.000 for every
  strategy, so MRR cannot separate them here. A real evaluation needs hundreds
  of labelled pairs.
- **The hybrid does not win every column.** It loses on MAP (0.899 vs 0.986)
  because a graded skill score sometimes ranks a labelled-relevant candidate
  just below an irrelevant one. That is a weight-calibration problem, not a
  proof the approach is wrong — and it is exactly the kind of thing you only
  learn from measuring.
- **"Related skill" credit is hand-tuned.** `RelatedWeight = 0.4` is a guess.
  In production this should be learned from recruiter decisions.
- **No embeddings here.** `Cosine` runs over TF-IDF, which is a stand-in. Swapping
  in real embeddings is a one-function change in `internal/textsim`.

## Bugs this exercise surfaced (and fixed)

Worth listing because each one is a trap you will meet in a real project:

1. **`partial_ratio` on tokens instead of characters.** The first version
   scored `java` vs `javascript` at 0.24, which made fuzzy look safe. Real
   fuzzywuzzy slides a character window, so it scores 0.90. Getting the
   reference implementation wrong hides the exact problem you are testing for.
2. **One-directional skill relations.** PostgreSQL was not counted as
   satisfying a SQL requirement, so a strong candidate scored zero on the most
   important field. Relations are now checked both ways via `Satisfies`.
3. **Filtering on minimum years.** Dropping candidates for being one year short
   destroyed recall on three of six jobs. Years are now a graded score, not a
   pass/fail gate. Filters are for constraints you cannot negotiate (on-site
   location, remote availability); everything else is a weight.
4. **A metric that measured the wrong thing.** The first "trap" metric counted
   top-5 candidates holding a confusable skill, which blamed text rankers for
   coincidences. It now measures the cause-independent question: does this
   candidate have any real must-have evidence at all?

## Layout

```
cmd/bench/            comparison harness + inspect mode
internal/taxonomy/    canonical skills, aliases, Satisfies()
internal/textsim/     tokenizer, Levenshtein, WRatio, BM25, TF-IDF cosine, RRF
internal/match/       the three strategies
internal/eval/        precision@k, recall@k, MRR, MAP
internal/model/       Job, Candidate, Label, Dataset
data/dataset.json     jobs, candidates, human labels
```

## Extending

| To do this | Change |
|---|---|
| Real semantic matching | replace `textsim.Cosine` with an embedding call; keep the interface |
| Store in a database | implement a `CandidateSource` that reads rows into `model.Candidate` |
| Multi-tenant | add `OrgID` to every query and never rank across orgs |
| Explain a score in the UI | `match.Scored.Parts` already holds each component |
| Reproduce a score later | `Scored` is pure data; log the `Parts` map with the ranking |

## Not included

Payroll, attendance, interview scheduling, e-sign, job-board integrations.
Matching is the part that is easy to get quietly wrong, so it is the part
worth building first and measuring first.

## License

MIT
