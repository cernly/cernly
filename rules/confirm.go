package rules

import (
	"sort"
	"strings"
)

// Confirmation is a decision a person made outside the repo: a stack's
// role, or what an outside party or host is. It is an input to the rules,
// like the default branch, recorded in the map's settings; it never changes
// a fact. A declaration in the scan (.cernly.yml or the operator's file)
// wins over it.
type Confirmation struct {
	ID      int64  `json:"id"`      // the entry in the instance's trail
	Kind    string `json:"kind"`    // stack_role, party or host
	Subject string `json:"subject"` // a stack key, party:<id> or host:<host>
	Value   string `json:"value"`   // stack_role: production or none; party: approved or not_supplier; host: also own
	Reason  string `json:"reason"`
	By      string `json:"by"`               // display name
	Issuer  string `json:"issuer,omitempty"` // who, as the sign-in provider knows them
	Person  string `json:"person,omitempty"` // the provider's subject for them
	At      string `json:"at"`               // when it was made, RFC 3339
}

// Confirmation kinds and values.
const (
	ConfirmStackRole = "stack_role"
	ConfirmParty     = "party"
	ConfirmHost      = "host"
	ConfirmDriver    = "driver" // subject a dependency key; value store:<the store it talks to> or not_used

	ValueApproved    = "approved"
	ValueNotSupplier = "not_supplier"
	ValueOwn         = "own"
	ValueNotUsed     = "not_used"
	ValueStorePrefix = "store:"
)

// Confirmation statuses in the map.
const (
	StatusApplied    = "applied"
	StatusOverridden = "overridden" // a declaration in the scan says otherwise, and wins
	StatusNoSubject  = "no_subject" // this scan has no such stack, party or host
	StatusIgnored    = "ignored"    // a kind or value these rules do not know
)

// ConfirmationUse is what one confirmation did to this map.
type ConfirmationUse struct {
	Confirmation
	Status string `json:"status"`
}

// ConfirmRef marks an item a confirmation is about.
type ConfirmRef struct {
	ID         int64  `json:"id"`
	Value      string `json:"value"`
	By         string `json:"by"`
	At         string `json:"at"`
	Overridden bool   `json:"overridden,omitempty"` // a declaration in the scan wins
}

// Dismissed is a party or host a person said is not a supplier: moved here
// from parties or unknown, so a reader can tell it from one never found.
type Dismissed struct {
	Subject   string     `json:"subject"`
	Kind      string     `json:"kind"` // party or host
	Name      string     `json:"name"`
	Confirmed ConfirmRef `json:"confirmed"`
	From      []string   `json:"from"`
}

// ValidConfirmation reports whether the rules know this kind and value, and
// the subject has the kind's form.
func ValidConfirmation(kind, subject, value string) bool {
	switch kind {
	case ConfirmStackRole:
		return strings.HasPrefix(subject, "stack:") && (value == RoleProduction || value == RoleNone)
	case ConfirmParty:
		return strings.HasPrefix(subject, "party:") && len(subject) > 6 && (value == ValueApproved || value == ValueNotSupplier)
	case ConfirmHost:
		return strings.HasPrefix(subject, "host:") && len(subject) > 5 && (value == ValueApproved || value == ValueNotSupplier || value == ValueOwn)
	case ConfirmDriver:
		store := strings.TrimPrefix(value, ValueStorePrefix)
		named := store != value && strings.TrimSpace(store) == store && store != "" && len(store) <= 200
		return strings.HasPrefix(subject, "dependency:") && (value == ValueNotUsed || named)
	}
	return false
}

func ref(c Confirmation) ConfirmRef {
	return ConfirmRef{ID: c.ID, Value: c.Value, By: c.By, At: c.At}
}

// confirm applies the confirmations in id order, after the rules and the
// declarations have run.
func confirm(m *Map) {
	cs := append([]Confirmation(nil), m.Settings.Confirmations...)
	if len(cs) == 0 {
		m.Settings.Confirmations = nil
		return
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
	m.Settings.Confirmations = cs
	drop := func(rule, subject string) {
		out := m.Findings[:0]
		for _, f := range m.Findings {
			if f.Rule != rule || f.Subject != subject {
				out = append(out, f)
			}
		}
		m.Findings = out
	}
	for _, c := range cs {
		status := StatusIgnored
		if ValidConfirmation(c.Kind, c.Subject, c.Value) {
			switch c.Kind {
			case ConfirmStackRole:
				status = confirmStack(m, c)
				if status == StatusApplied {
					drop(FindingProductionUnconfirmed, c.Subject)
				}
			case ConfirmParty:
				status = confirmParty(m, c)
				if status == StatusApplied {
					drop(FindingPartyUnconfirmed, c.Subject)
				}
			case ConfirmHost:
				status = confirmHost(m, c)
			case ConfirmDriver:
				// Store named or not used: either way the finding is settled.
				status = StatusNoSubject
				for _, f := range m.Findings {
					if f.Rule == FindingDriverWithoutStore && f.Subject == c.Subject {
						status = StatusApplied
					}
				}
				drop(FindingDriverWithoutStore, c.Subject)
			}
		}
		m.Confirmations = append(m.Confirmations, ConfirmationUse{Confirmation: c, Status: status})
	}
	sort.Slice(m.Own, func(i, j int) bool { return m.Own[i].Host < m.Own[j].Host })
	sort.Slice(m.Dismissed, func(i, j int) bool { return m.Dismissed[i].Subject < m.Dismissed[j].Subject })
}

func confirmStack(m *Map, c Confirmation) string {
	for i := range m.Stacks {
		st := &m.Stacks[i]
		if st.Stack != c.Subject {
			continue
		}
		r := ref(c)
		if st.Declared != nil && st.DerivedRole != "" { // the scan declares a role
			r.Overridden = true
			st.Confirmed = &r
			return StatusOverridden
		}
		st.DerivedRole, st.Role = st.Role, c.Value
		st.Confirmed = &r
		return StatusApplied
	}
	return StatusNoSubject
}

func confirmParty(m *Map, c Confirmation) string {
	id := strings.TrimPrefix(c.Subject, "party:")
	for i := range m.Parties {
		p := &m.Parties[i]
		if p.ID != id {
			continue
		}
		r := ref(c)
		if c.Value == ValueApproved {
			p.Confirmed = &r
			return StatusApplied
		}
		var from []string
		for _, s := range p.Signals {
			from = append(from, s.Fact)
		}
		m.Dismissed = append(m.Dismissed, Dismissed{Subject: c.Subject, Kind: ConfirmParty, Name: p.Name, Confirmed: r, From: uniq(from)})
		m.Parties = append(m.Parties[:i], m.Parties[i+1:]...)
		return StatusApplied
	}
	return StatusNoSubject
}

func confirmHost(m *Map, c Confirmation) string {
	h := strings.TrimPrefix(c.Subject, "host:")
	r := ref(c)
	for i := range m.Own {
		o := &m.Own[i]
		if o.Host != h {
			continue
		}
		if o.Declared != nil || c.Value != ValueOwn {
			// Declared own, or own by a route the repo serves: the scan wins.
			r.Overridden = true
			o.Confirmed = &r
			return StatusOverridden
		}
		o.Confirmed = &r
		return StatusApplied
	}
	switch c.Value {
	case ValueApproved, ValueNotSupplier:
		for i := range m.Unknown {
			u := &m.Unknown[i]
			if u.Host != h {
				continue
			}
			if c.Value == ValueApproved {
				u.Confirmed = &r
			} else {
				m.Dismissed = append(m.Dismissed, Dismissed{Subject: c.Subject, Kind: ConfirmHost, Name: h, Confirmed: r, From: u.From})
				m.Unknown = append(m.Unknown[:i], m.Unknown[i+1:]...)
			}
			return StatusApplied
		}
	case ValueOwn:
		// As domains.own: the host and its subdomains.
		found := false
		keep := m.Unknown[:0]
		for _, u := range m.Unknown {
			if u.Host == h || strings.HasSuffix(u.Host, "."+h) {
				rr := r
				m.Own = append(m.Own, OwnHost{Host: u.Host, Domain: h, Confirmed: &rr, From: u.From})
				found = true
				continue
			}
			keep = append(keep, u)
		}
		m.Unknown = keep
		if found {
			return StatusApplied
		}
	}
	return StatusNoSubject
}
