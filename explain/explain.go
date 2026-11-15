// Package explain says what each finding means to someone who is not reading
// code, and what to do about it. Every view of a scan uses the same words, so a
// finding reads the same everywhere. Only words: what is a finding is decided by
// the rules.
package explain

// Level orders findings for a reader: act first, then confirm, then info.
type Level string

const (
	Act     Level = "act"     // something to change
	Confirm Level = "confirm" // a person checks it and confirms it, or declares it in .cernly.yml
	Info    Level = "info"    // worth knowing, nothing to do
)

type Rule struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Meaning string `json:"meaning"`
	Action  string `json:"action"`
	Level   Level  `json:"level"`
}

// mapRules are the findings of one scan's map.
var mapRules = []Rule{
	{"image_pinning", "Images on a moving tag",
		"The image is named by a tag (latest, 16) or a version without a digest: what runs can change without any change in the repository.",
		"Pin the image by digest (name@sha256:…), so the repository says exactly what runs.", Act},
	{"production_unconfirmed", "Production not confirmed",
		"A CI job starts this stack, but its conditions could not be read, so it is not certain that this stack is production.",
		"Confirm the stack's role in the app, with a reason (production or none), or declare it in .cernly.yml at the repository root (stacks, the stack's name, role); a declaration in the repository wins.", Confirm},
	{"endpoint_anonymous_public", "Anonymous endpoint on a public route",
		"This endpoint is marked anonymous in code and served on a public production route. That is often intended, as with health checks and login pages.",
		"Check that the endpoint is meant to be open. If it is not, require authentication in the code.", Info},
	{"party_unconfirmed", "Outside party seen once",
		"Only one kind of signal names this provider (an SDK, an env name or a host), so it may not be in use.",
		"Check that the code really uses it, then confirm it in the app as an approved supplier, or dismiss it as not a supplier (a test or example URL, a docs link), with a reason.", Confirm},
	{"driver_without_store", "Database driver without a data store",
		"The service ships a database driver, but the repository describes no data store of that engine: the store it talks to is outside what the code shows, or the driver is not used.",
		"Name the data store it talks to in the app, or dismiss the driver as not used, with a reason.", Confirm},
	{"no_code_link", "Service not traced to code",
		"The repository builds this service, but its image could not be traced to a folder of code.",
		"Check its Dockerfile and build context.", Confirm},
	{"link_conflict", "Two builds claim a service",
		"More than one place in the repository builds this service, so which code runs is ambiguous.",
		"Keep one build per service, or declare which one is used.", Confirm},
}

// gateRules are what a scan adds since its base. The level a reader sees is
// the policy's (warn or block), not this one.
var gateRules = []Rule{
	{"new_outside_party", "New outside party",
		"Since the base, the code started to use a provider, or to request a host no one claims: data may now go somewhere new.",
		"Check that the party is approved for this data, then confirm it in the app; or except this finding with a reason.", Act},
	{"new_sensitive_column", "New column to check",
		"Since the base, a database column whose name suggests personal or secret data was added.",
		"Check that it is encrypted or access-controlled as the policy requires; or except this finding with a reason.", Act},
	{"new_unpinned_image", "New image on a moving tag",
		"Since the base, an image named by a tag rather than a digest was added: what runs can change without a change in the repository.",
		"Pin the image by digest (name@sha256:…).", Act},
	{"new_production_stack", "New production stack",
		"Since the base, a stack became production, or may have (its deploy condition could not be read).",
		"Check that the release is intended and reviewed; confirm the stack's role in the app.", Act},
	{"new_dependency", "New direct dependency",
		"Since the base, the code depends directly on a package it did not use before.",
		"Check that the package is wanted and maintained; or except this finding with a reason.", Confirm},
	{"new_transitive_dependency", "New transitive dependency",
		"Since the base, a lockfile pulls in a package it did not before, through another one.",
		"Check the package that brought it in.", Info},
	{"licence_not_allowed", "Licence not allowed",
		"A dependency carries a licence the policy does not allow.",
		"Replace the dependency, or except this finding with a reason that the licence is acceptable here.", Act},
	{"new_anonymous_public_endpoint", "New anonymous public endpoint",
		"Since the base, an endpoint marked anonymous in code is served on a public production route. That is often intended, as with health checks and login pages.",
		"Check that the endpoint is meant to be open; or except this finding with a reason.", Confirm},
	{"gate_loosened", "Gate policy loosened",
		"Since the base, .cernly.yml lowered a gate rule's level or added an exclusion, so less is checked.",
		"Check that the change to the policy was agreed; the operator's floor still applies.", Act},
}

var byID = func() map[string]Rule {
	m := map[string]Rule{}
	for _, r := range All() {
		m[r.ID] = r
	}
	return m
}()

// StackRole is the .cernly.yml that declares the stacks' roles, with the
// version line a new file needs: how a person settles production_unconfirmed
// in the repository. A stack is its name in the scan (the key's last part),
// never a display label.
func StackRole(stacks ...string) string {
	s := "version: 1\nstacks:\n"
	for _, st := range stacks {
		s += "  " + st + ":\n    role: production   # or none\n"
	}
	return s
}

// Of returns a rule's words; an unknown rule reads as its id.
func Of(id string) Rule {
	if r, ok := byID[id]; ok {
		return r
	}
	return Rule{ID: id, Title: id, Level: Info}
}

// MapRules returns the map's findings, in a fixed order.
func MapRules() []Rule { return append([]Rule(nil), mapRules...) }

// GateRules returns the gate's findings, in a fixed order.
func GateRules() []Rule { return append([]Rule(nil), gateRules...) }

// All returns every rule of one scan or two, in a fixed order: the map's,
// then the gate's.
func All() []Rule { return append(MapRules(), gateRules...) }

// Rank orders levels: act 0, confirm 1, info 2.
func Rank(l Level) int {
	switch l {
	case Act:
		return 0
	case Confirm:
		return 1
	}
	return 2
}
