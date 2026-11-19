// Package taxonomy maps the many spellings of a skill onto one canonical id.
//
// This is layer L1 of the matcher. It deliberately does NOT use fuzzy string
// distance: "Java" and "JavaScript" are 90% similar as strings and completely
// different as skills. A curated alias table is boring, but it is correct and
// auditable — you can show a recruiter exactly why a skill matched.
package taxonomy

import (
	"sort"
	"strings"
)

// Skill is a canonical concept.
type Skill struct {
	ID      string
	Label   string
	Related []string // other canonical ids that satisfy or partly satisfy this
}

// skills is the canonical vocabulary. Keep it small and honest; a real system
// loads this from a database so recruiters can extend it.
var skills = []Skill{
	// --- languages -------------------------------------------------------
	{ID: "lang.go", Label: "Go", Related: []string{"lang.rust", "lang.c"}},
	{ID: "lang.java", Label: "Java", Related: []string{"lang.kotlin"}},
	{ID: "lang.javascript", Label: "JavaScript"},
	{ID: "lang.typescript", Label: "TypeScript", Related: []string{"lang.javascript"}},
	{ID: "lang.python", Label: "Python"},
	{ID: "lang.php", Label: "PHP"},
	{ID: "lang.kotlin", Label: "Kotlin", Related: []string{"lang.java"}},
	{ID: "lang.swift", Label: "Swift"},
	{ID: "lang.c", Label: "C", Related: []string{"lang.cpp"}},
	{ID: "lang.cpp", Label: "C++", Related: []string{"lang.c"}},
	{ID: "lang.csharp", Label: "C#", Related: []string{"lang.java"}},
	{ID: "lang.rust", Label: "Rust", Related: []string{"lang.cpp"}},

	// --- backend ---------------------------------------------------------
	{ID: "be.node", Label: "Node.js", Related: []string{"lang.javascript"}},
	{ID: "be.spring", Label: "Spring Boot", Related: []string{"lang.java"}},
	{ID: "be.django", Label: "Django", Related: []string{"lang.python"}},
	{ID: "be.rest", Label: "REST API"},
	{ID: "be.graphql", Label: "GraphQL", Related: []string{"be.rest"}},
	{ID: "be.grpc", Label: "gRPC", Related: []string{"be.rest"}},
	{ID: "be.microservices", Label: "Microservices"},
	{ID: "be.queue", Label: "Message Queue", Related: []string{"be.microservices"}},

	// --- frontend --------------------------------------------------------
	{ID: "fe.react", Label: "React", Related: []string{"fe.vue", "fe.svelte"}},
	{ID: "fe.vue", Label: "Vue", Related: []string{"fe.react"}},
	{ID: "fe.svelte", Label: "Svelte", Related: []string{"fe.react"}},
	{ID: "fe.css", Label: "CSS"},
	{ID: "fe.a11y", Label: "Accessibility", Related: []string{"fe.css"}},

	// --- mobile ----------------------------------------------------------
	{ID: "mob.android", Label: "Android", Related: []string{"lang.kotlin"}},
	{ID: "mob.ios", Label: "iOS", Related: []string{"lang.swift"}},
	{ID: "mob.flutter", Label: "Flutter", Related: []string{"mob.android", "mob.ios"}},
	{ID: "mob.reactnative", Label: "React Native", Related: []string{"fe.react", "mob.android"}},

	// --- data / ml -------------------------------------------------------
	{ID: "ml.concept", Label: "Machine Learning"},
	{ID: "ml.llm", Label: "LLM Engineering", Related: []string{"ml.concept"}},
	{ID: "ml.vision", Label: "Computer Vision", Related: []string{"ml.concept"}},
	{ID: "data.sql", Label: "SQL"},
	{ID: "data.postgres", Label: "PostgreSQL", Related: []string{"data.sql"}},
	{ID: "data.mysql", Label: "MySQL", Related: []string{"data.sql"}},
	{ID: "data.analytics", Label: "Analytics", Related: []string{"data.sql"}},
	{ID: "data.etl", Label: "ETL", Related: []string{"data.sql"}},

	// --- infra -----------------------------------------------------------
	{ID: "ops.docker", Label: "Docker"},
	{ID: "ops.k8s", Label: "Kubernetes", Related: []string{"ops.docker"}},
	{ID: "ops.ci", Label: "CI/CD", Related: []string{"ops.docker"}},
	{ID: "ops.aws", Label: "AWS", Related: []string{"ops.gcp", "ops.azure"}},
	{ID: "ops.gcp", Label: "GCP", Related: []string{"ops.aws"}},
	{ID: "ops.azure", Label: "Azure", Related: []string{"ops.aws"}},
	{ID: "ops.terraform", Label: "Terraform", Related: []string{"ops.aws"}},
	{ID: "ops.linux", Label: "Linux"},
	{ID: "ops.networking", Label: "Networking", Related: []string{"ops.linux"}},

	// --- quality ---------------------------------------------------------
	{ID: "qa.manual", Label: "Manual QA"},
	{ID: "qa.automation", Label: "Test Automation", Related: []string{"qa.manual"}},
	{ID: "qa.selenium", Label: "Selenium", Related: []string{"qa.automation"}},

	// --- product / soft --------------------------------------------------
	{ID: "pm.product", Label: "Product Management"},
	{ID: "pm.agile", Label: "Agile", Related: []string{"pm.product"}},
	{ID: "soft.english", Label: "English"},
}

// aliasIndex maps a normalized alias to its canonical id.
// Built once at init from the alias table below.
var aliasIndex = map[string]string{}

// byID lets Expand look up Related edges.
var byID = map[string]Skill{}

// aliases is the heart of the layer: every spelling a candidate might type.
// Add freely; an unknown alias simply fails to match, which is safe.
var aliases = map[string]string{
	// Go
	"go": "lang.go", "golang": "lang.go", "go lang": "lang.go",
	// Java family
	"java": "lang.java", "java se": "lang.java", "java ee": "lang.java",
	"j2ee": "lang.java", "jdk": "lang.java",
	// JavaScript / TypeScript
	"javascript": "lang.javascript", "js": "lang.javascript",
	"es6": "lang.javascript", "ecmascript": "lang.javascript",
	"typescript": "lang.typescript", "ts": "lang.typescript",
	// Python
	"python": "lang.python", "python3": "lang.python",
	// PHP
	"php": "lang.php", "laravel": "lang.php", "codeigniter": "lang.php",
	// Kotlin / Swift
	"kotlin": "lang.kotlin", "swift": "lang.swift", "swiftui": "lang.swift",
	// C family — these are the classic fuzzy-matching traps
	"c": "lang.c", "cpp": "lang.cpp", "c++": "lang.cpp", "cplusplus": "lang.cpp",
	"c#": "lang.csharp", "csharp": "lang.csharp", "dotnet": "lang.csharp",
	".net": "lang.csharp", "asp.net": "lang.csharp",
	"rust": "lang.rust",
	// Backend runtimes / frameworks
	"node": "be.node", "nodejs": "be.node", "node.js": "be.node",
	"express": "be.node", "nestjs": "be.node", "nest.js": "be.node",
	"spring": "be.spring", "springboot": "be.spring", "spring boot": "be.spring",
	"django": "be.django", "flask": "be.django", "fastapi": "be.django",
	"rest": "be.rest", "restful": "be.rest", "rest api": "be.rest",
	"api": "be.rest", "http api": "be.rest",
	"graphql": "be.graphql", "grpc": "be.grpc",
	"microservices": "be.microservices", "microservice": "be.microservices",
	"rabbitmq": "be.queue", "kafka": "be.queue", "sqs": "be.queue",
	// Frontend
	"react": "fe.react", "reactjs": "fe.react", "react.js": "fe.react",
	"nextjs": "fe.react", "next.js": "fe.react",
	"vue": "fe.vue", "vuejs": "fe.vue", "vue.js": "fe.vue", "nuxt": "fe.vue",
	"svelte": "fe.svelte", "sveltekit": "fe.svelte",
	"css": "fe.css", "tailwind": "fe.css", "sass": "fe.css", "scss": "fe.css",
	"accessibility": "fe.a11y", "a11y": "fe.a11y",
	// Mobile
	"android": "mob.android", "android sdk": "mob.android",
	"ios": "mob.ios", "uikit": "mob.ios",
	"flutter": "mob.flutter", "dart": "mob.flutter",
	"react native": "mob.reactnative", "reactnative": "mob.reactnative",
	// ML / data
	"ml": "ml.concept", "machine learning": "ml.concept",
	"machinelearning": "ml.concept", "deep learning": "ml.concept",
	"artificial intelligence": "ml.concept", "ai": "ml.concept",
	"llm": "ml.llm", "llms": "ml.llm", "rag": "ml.llm",
	"prompt engineering": "ml.llm", "fine tuning": "ml.llm",
	"computer vision": "ml.vision", "opencv": "ml.vision", "cv": "ml.vision",
	"sql": "data.sql", "postgresql": "data.postgres", "postgres": "data.postgres",
	"mysql": "data.mysql", "mariadb": "data.mysql",
	"analytics": "data.analytics", "bigquery": "data.analytics",
	"etl": "data.etl", "airflow": "data.etl", "dbt": "data.etl",
	// Infra
	"docker": "ops.docker", "container": "ops.docker",
	"kubernetes": "ops.k8s", "k8s": "ops.k8s", "openshift": "ops.k8s",
	"ci/cd": "ops.ci", "cicd": "ops.ci", "jenkins": "ops.ci",
	"github actions": "ops.ci", "gitlab ci": "ops.ci",
	"aws": "ops.aws", "amazon web services": "ops.aws", "ec2": "ops.aws",
	"gcp": "ops.gcp", "google cloud": "ops.gcp",
	"azure": "ops.azure", "terraform": "ops.terraform", "ansible": "ops.terraform",
	"linux": "ops.linux", "ubuntu": "ops.linux", "debian": "ops.linux",
	"networking": "ops.networking", "tcp/ip": "ops.networking",
	// QA
	"qa": "qa.manual", "manual testing": "qa.manual", "manual qa": "qa.manual",
	"test automation": "qa.automation", "automation testing": "qa.automation",
	"selenium": "qa.selenium", "cypress": "qa.selenium", "playwright": "qa.selenium",
	// Product
	"product management": "pm.product", "product manager": "pm.product",
	"agile": "pm.agile", "scrum": "pm.agile", "kanban": "pm.agile",
	"english": "soft.english", "bahasa inggris": "soft.english",
}

func init() {
	for _, s := range skills {
		byID[s.ID] = s
	}
	for alias, id := range aliases {
		aliasIndex[Normalize(alias)] = id
	}
	// A canonical label is always its own alias, so "Node.js" resolves even if
	// nobody added the exact spelling above.
	for _, s := range skills {
		key := Normalize(s.Label)
		if _, exists := aliasIndex[key]; !exists {
			aliasIndex[key] = s.ID
		}
	}
}

// Normalize lowercases and collapses separators so alias lookup is stable.
// "Node.js" -> "node.js", "  React JS " -> "react js".
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Collapse runs of whitespace; keep dots, +, # and / because they carry
	// meaning in "node.js", "c++", "c#", "ci/cd".
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Canonical resolves one raw skill string to a canonical id.
// The bool is false when the skill is outside the vocabulary.
func Canonical(raw string) (string, bool) {
	id, ok := aliasIndex[Normalize(raw)]
	return id, ok
}

// Label returns the human-readable label for a canonical id.
func Label(id string) string {
	if s, ok := byID[id]; ok {
		return s.Label
	}
	return id
}

// Related returns the canonical ids that partially satisfy id.
func Related(id string) []string {
	if s, ok := byID[id]; ok {
		return s.Related
	}
	return nil
}

// Expand turns a canonical set into a weighted concept set.
// A required skill weighs 1.0; a related skill weighs RelatedWeight.
// This is the cheap stand-in for embeddings: "Kotlin" partially satisfies a
// "Java" requirement without pretending they are identical.
const relatedWeight = 0.4

// RelatedWeight is the credit a related-but-not-identical skill earns.
const RelatedWeight = relatedWeight

// Satisfies reports whether having candID gives credit toward reqID.
//
// The relation is checked in BOTH directions on purpose:
//   - candidate has a specialisation that implies the requirement
//     (PostgreSQL satisfies SQL, Kotlin satisfies Java)
//   - candidate has a sibling skill in the same family
//     (Rust is related to Go, Vue is related to React)
//
// Getting this direction wrong is a classic taxonomy bug: without it, a
// PostgreSQL expert scores zero on a "SQL required" job.
func Satisfies(candID, reqID string) bool {
	if candID == reqID {
		return true
	}
	for _, rel := range Related(candID) {
		if rel == reqID {
			return true
		}
	}
	for _, rel := range Related(reqID) {
		if rel == candID {
			return true
		}
	}
	return false
}

func Expand(ids []string) map[string]float64 {
	out := make(map[string]float64, len(ids)*2)
	for _, id := range ids {
		if w, seen := out[id]; !seen || w < 1.0 {
			out[id] = 1.0
		}
		for _, rel := range Related(id) {
			if w, seen := out[rel]; !seen || w < relatedWeight {
				out[rel] = relatedWeight
			}
		}
	}
	return out
}

// Size reports how many canonical skills and aliases are loaded, for the
// banner the bench prints at startup.
func Size() (skillCount, aliasCount int) {
	return len(skills), len(aliasIndex)
}

// Alias is one spelling mapped to a canonical id.
type Alias struct {
	Text string // normalized alias, e.g. "node.js"
	ID   string // canonical id, e.g. "be.node"
}

// Aliases returns every alias, longest first.
//
// Order matters for detection: the caller blanks out each match, so a long
// alias such as "machine learning" is consumed before the short alias "ml"
// can match inside it. "C++" must also win over "C".
func Aliases() []Alias {
	out := make([]Alias, 0, len(aliasIndex))
	for text, id := range aliasIndex {
		out = append(out, Alias{Text: text, ID: id})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if len(out[i].Text) != len(out[j].Text) {
			return len(out[i].Text) > len(out[j].Text)
		}
		return out[i].Text < out[j].Text
	})
	return out
}
