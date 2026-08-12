// Package labels holds the category set used when sorting mail.
//
// These descriptions are the entire model. There is no training data for this
// mailbox, so what separates "Grants" from "Nonprofit Partners" is nothing more
// than the sentences written here - which is why each one names real programs
// and states explicitly what does NOT belong, rather than describing the
// category in the abstract.
//
// Derived from the foundation's own published programs and funds.
package labels

import "strings"

// Category is one label, as it appears in Outlook.
type Category struct {
	// Stable identifier used in prompts and rules. Survives renaming.
	ID string
	// The literal Outlook category name. Must be unique in the mailbox.
	Name string
	// One of Preset0..Preset24 - Outlook offers exactly 25 colours.
	Color string
	// Read by the model doing the classifying.
	Description string
}

// NeedsReviewID is the bucket for anything the classifier is unsure about.
const NeedsReviewID = "needs-review"

// Taxonomy is the default category set for the Executive Director of a
// community foundation.
var Taxonomy = []Category{
	{
		ID: "donors", Name: "Donors & Gifts", Color: "Preset0",
		Description: "Correspondence with individual donors, families, and businesses who give. " +
			"Gift notifications and acknowledgements, questions about opening or adding to a donor " +
			"advised fund, field of interest fund, or designated fund, planned giving and bequest " +
			"conversations, stock and QCD transfers, thank-you correspondence. " +
			"Distinguish from Finance: a gift from a named person is Donors & Gifts, whereas a " +
			"custodian statement or platform payout report is Finance.",
	},
	{
		ID: "grants", Name: "Grants", Color: "Preset1",
		Description: "Anything from or about organizations seeking money. Letters of inquiry, grant " +
			"applications and attachments, eligibility and deadline questions, grant agreements, " +
			"interim and final grant reports, declines and appeals, and the LAUNCH Fund and " +
			"Community Emergency Relief Fund pipelines. " +
			"Distinguish from Nonprofit Partners: a nonprofit asking for or reporting on funding is " +
			"Grants; the same nonprofit asking about a workshop or fiscal sponsorship is Partners.",
	},
	{
		ID: "scholarships", Name: "Scholarships", Color: "Preset2",
		Description: "Student scholarship applicants and their families, transcripts and " +
			"recommendation letters, scholarship review committee scheduling and deliberations, " +
			"award notifications, disbursement and enrollment verification with schools, and " +
			"scholarship fund donors asking about their named award. Student-facing mail belongs " +
			"here rather than in Grants.",
	},
	{
		ID: "board", Name: "Board & Governance", Color: "Preset3",
		Description: "Board of directors and committee business. Meeting agendas, packets, minutes, " +
			"board recruitment and onboarding, conflict of interest and policy documents, bylaws, " +
			"audit and finance committee mail, executive committee threads, strategic planning. " +
			"Mail from a board member wearing a donor hat is Donors & Gifts; mail about governance " +
			"is here.",
	},
	{
		ID: "finance", Name: "Finance", Color: "Preset4",
		Description: "Money mechanics and record keeping. Investment and custodian statements, bank " +
			"notices, ColoradoGives and other giving-platform payout and disbursement reports, " +
			"invoices and bills, bookkeeping and QuickBooks correspondence, payroll, audit " +
			"fieldwork, 990 preparation, insurance. Automated statements and reports belong here " +
			"even when they concern donor gifts.",
	},
	{
		ID: "events", Name: "Events", Color: "Preset5",
		Description: "Logistics for the foundation's fundraising and community events: Durango Wine " +
			"Experience, Hoedown at the Mancos Opera House, 19th Hole Concerts, Payroll " +
			"Department's Pitch Palooza, Making a Difference Speaker Series, and Tips & Tricks or " +
			"Year-End Ask workshop sessions. Includes sponsors and sponsorship packets, venues, " +
			"caterers, ticketing, auction items, volunteers, run-of-show. " +
			"Event sponsorship solicitation is Events, not Donors & Gifts.",
	},
	{
		ID: "partners", Name: "Nonprofit Partners", Color: "Preset6",
		Description: "The regional nonprofit sector the foundation serves, in a non-funding " +
			"capacity. Fiscal sponsorship arrangements, professional development workshop " +
			"registration and questions, agency fund holders, capacity-building requests, " +
			"collaboration and referral, and the CAUSE Youth Internship and DWE Nonprofit Partners " +
			"programs. If they are asking for grant money it is Grants; if they are asking for " +
			"help, training, or partnership it is here.",
	},
	{
		ID: "press", Name: "Press & Community", Color: "Preset7",
		Description: "Outward-facing community communication. Reporters and media requests, press " +
			"releases, interview and podcast invitations, award nominations, letters to the editor, " +
			"speaking invitations, community announcements from Durango and the five-county region " +
			"(Archuleta, La Plata, Dolores, Montezuma, San Juan), and chamber or civic group mail.",
	},
	{
		ID: "sector", Name: "Sector News", Color: "Preset8",
		Description: "Bulk philanthropy-sector reading with no action required. Newsletters and " +
			"bulletins from the Council on Foundations, Colorado Nonprofit Association, " +
			"Philanthropy Colorado, Chronicle of Philanthropy, Candid, webinar and conference " +
			"promotions, sector research digests. Characteristically a mailing list with an " +
			"unsubscribe link and no personal addressing. Low urgency by definition.",
	},
	{
		ID: "vendors", Name: "Vendors & Admin", Color: "Preset9",
		Description: "Running the office. Software and SaaS notifications and renewals, IT and " +
			"Microsoft 365 service mail, password resets and security alerts, office supplies, " +
			"phone and internet, landlord and facilities, professional memberships and dues, " +
			"general administrative housekeeping. Automated transactional mail from tools belongs " +
			"here unless it concerns money movement, which is Finance.",
	},
	{
		ID: NeedsReviewID, Name: "⚠ Needs Review", Color: "Preset10",
		Description: "Reserved. Never choose this directly - it is for mail that genuinely does not " +
			"fit any category above, or where two categories are equally plausible.",
	},
}

// Selectable returns the categories a classifier may actually choose.
func Selectable() []Category {
	out := make([]Category, 0, len(Taxonomy))
	for _, c := range Taxonomy {
		if c.ID != NeedsReviewID {
			out = append(out, c)
		}
	}
	return out
}

// ByID finds a category, or nil.
func ByID(id string) *Category {
	for i := range Taxonomy {
		if Taxonomy[i].ID == id {
			return &Taxonomy[i]
		}
	}
	return nil
}

// ByName finds a category by its Outlook display name, case-insensitively.
func ByName(name string) *Category {
	needle := strings.ToLower(strings.TrimSpace(name))
	for i := range Taxonomy {
		if strings.ToLower(strings.TrimSpace(Taxonomy[i].Name)) == needle {
			return &Taxonomy[i]
		}
	}
	return nil
}

// Names returns every Outlook category name this tool manages.
func Names() []string {
	out := make([]string, 0, len(Taxonomy))
	for _, c := range Taxonomy {
		out = append(out, c.Name)
	}
	return out
}

// IsOurs reports whether a category name is one this tool manages, so labels
// the user applied by hand are never disturbed.
func IsOurs(name string) bool {
	return ByName(name) != nil
}

// Describe renders the taxonomy for a model to read.
func Describe() string {
	var b strings.Builder
	for _, c := range Selectable() {
		b.WriteString("- ")
		b.WriteString(c.ID)
		b.WriteString(" (\"")
		b.WriteString(c.Name)
		b.WriteString("\"): ")
		b.WriteString(c.Description)
		b.WriteString("\n")
	}
	return b.String()
}
