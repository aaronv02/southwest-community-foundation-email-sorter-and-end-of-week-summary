import type { Category, Settings, PersistedState } from './types.js';

/**
 * The bucket for anything the classifier isn't sure about.
 *
 * This category is load-bearing. A confident wrong label costs more trust than
 * an admitted unknown, so everything below the confidence threshold lands here
 * rather than in a plausible-looking guess.
 */
export const NEEDS_REVIEW_ID = 'needs-review';

/**
 * Cold-start taxonomy for the Executive Director of a community foundation,
 * derived from the foundation's actual programs and funds.
 *
 * There is no filing history to learn from on day one, so these descriptions are
 * the entire model at first. They are written for an LLM reader: concrete
 * nouns, real program names, and explicit disambiguation against neighbouring
 * categories. All of it is editable in the taskpane.
 */
export const SEED_TAXONOMY: Category[] = [
  {
    id: 'donors',
    name: 'Donors & Gifts',
    color: 'Preset0',
    description:
      'Correspondence with individual donors, families, and businesses who give. Gift notifications and acknowledgements, questions about opening or adding to a donor advised fund, field of interest fund, or designated fund, planned giving and bequest conversations, stock and QCD transfers, and thank-you correspondence. Distinguish from Finance: a gift arriving from a named person is Donors & Gifts, whereas a custodian statement or platform payout report is Finance.',
  },
  {
    id: 'grants',
    name: 'Grants',
    color: 'Preset1',
    description:
      'Anything from or about organizations seeking money. Letters of inquiry, grant applications and attachments, questions about eligibility or deadlines, grant agreements, interim and final grant reports, declines and appeals, and the LAUNCH Fund and Community Emergency Relief Fund pipelines. Distinguish from Nonprofit Partners: a nonprofit asking for or reporting on funding is Grants, whereas the same nonprofit asking about a workshop or fiscal sponsorship is Nonprofit Partners.',
  },
  {
    id: 'scholarships',
    name: 'Scholarships',
    color: 'Preset2',
    description:
      'Student scholarship applicants and their families, transcripts and recommendation letters, scholarship review committee scheduling and deliberations, award notifications, disbursement and enrollment verification with schools, and scholarship fund donors asking about their named award. Student-facing mail belongs here rather than in Grants.',
  },
  {
    id: 'board',
    name: 'Board & Governance',
    color: 'Preset3',
    description:
      'Board of directors and committee business. Meeting agendas, packets, minutes, board member recruitment and onboarding, conflict of interest and policy documents, bylaws, audit and finance committee mail, executive committee threads, and strategic planning. Mail from a board member wearing a donor hat is Donors & Gifts; mail about governance is here.',
  },
  {
    id: 'finance',
    name: 'Finance',
    color: 'Preset4',
    description:
      'Money mechanics and record keeping. Investment and custodian statements, bank notices, ColoradoGives and other giving-platform payout and disbursement reports, invoices and bills, bookkeeping and QuickBooks correspondence, payroll, audit fieldwork, 990 preparation, and insurance. Automated statements and reports belong here even when they concern donor gifts.',
  },
  {
    id: 'events',
    name: 'Events',
    color: 'Preset5',
    description:
      'Logistics for the foundation\'s fundraising and community events: Durango Wine Experience, Hoedown at the Mancos Opera House, 19th Hole Concerts, Payroll Department\'s Pitch Palooza, Making a Difference Speaker Series, and Tips & Tricks or Year-End Ask workshop sessions. Includes sponsors and sponsorship packets, venues, caterers, ticketing, auction items, volunteers, and run-of-show. Event sponsorship solicitation is Events, not Donors & Gifts.',
  },
  {
    id: 'partners',
    name: 'Nonprofit Partners',
    color: 'Preset6',
    description:
      'The regional nonprofit sector the foundation serves, in a non-funding capacity. Fiscal sponsorship arrangements, professional development workshop registration and questions, agency fund holders, capacity-building requests, collaboration and referral, and the CAUSE Youth Internship and DWE Nonprofit Partners programs. If they are asking for grant money it is Grants; if they are asking for help, training, or partnership it is here.',
  },
  {
    id: 'press',
    name: 'Press & Community',
    color: 'Preset7',
    description:
      'Outward-facing community communication. Reporters and media requests, press releases, interview and podcast invitations, award nominations, letters to the editor, speaking invitations, community announcements from Durango and the five-county region (Archuleta, La Plata, Dolores, Montezuma, San Juan), and chamber or civic group mail.',
  },
  {
    id: 'sector',
    name: 'Sector News',
    color: 'Preset8',
    description:
      'Bulk philanthropy-sector reading with no action required. Newsletters and bulletins from the Council on Foundations, Colorado Nonprofit Association, Philanthropy Colorado, Chronicle of Philanthropy, Candid, webinar and conference promotions, and sector research digests. Characteristically a mailing list with an unsubscribe link and no personal addressing. Low urgency by definition.',
  },
  {
    id: 'vendors',
    name: 'Vendors & Admin',
    color: 'Preset9',
    description:
      'Running the office. Software and SaaS notifications and renewals, IT and Microsoft 365 service mail, password resets and security alerts, office supplies, phone and internet, landlord and facilities, professional memberships and dues, and general administrative housekeeping. Automated transactional mail from tools belongs here unless it concerns money movement, which is Finance.',
  },
  {
    id: NEEDS_REVIEW_ID,
    name: '⚠ Needs Review',
    color: 'Preset10',
    description:
      'Reserved. Never choose this category directly - it is applied automatically when no other category clears the confidence threshold.',
  },
];

export const DEFAULT_SETTINGS: Settings = {
  geminiApiKey: '',
  // Flash-class model: the free tier's highest request allowance, and email
  // classification does not need a reasoning model.
  model: 'gemini-2.5-flash',
  autonomy: 'suggest',
  dataSharing: 'full',
  confidenceThreshold: 0.65,
  promoteThreshold: 3,
  agreementRate: 0,
  bootstrapped: false,
};

export const STATE_VERSION = 1;

export function defaultState(): PersistedState {
  return {
    version: STATE_VERSION,
    settings: { ...DEFAULT_SETTINGS },
    taxonomy: SEED_TAXONOMY.map((c) => ({ ...c })),
    senderRules: [],
    recentCorrections: [],
  };
}

/** Categories the user may actually pick, i.e. everything except the gate bucket. */
export function selectableCategories(taxonomy: Category[]): Category[] {
  return taxonomy.filter((c) => c.id !== NEEDS_REVIEW_ID);
}

export function categoryById(taxonomy: Category[], id: string): Category | undefined {
  return taxonomy.find((c) => c.id === id);
}

export function categoryByName(taxonomy: Category[], name: string): Category | undefined {
  const needle = name.trim().toLowerCase();
  return taxonomy.find((c) => c.name.trim().toLowerCase() === needle);
}
