package hcbapi

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// PageOpts are cursor-pagination options for paginated list endpoints.
type PageOpts struct {
	Limit int    // default 25, max 100
	After string // cursor: public id of the last item of the previous page
}

func (p PageOpts) apply(q url.Values) {
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	if p.After != "" {
		q.Set("after", p.After)
	}
}

func expandQuery(expand []string) url.Values {
	q := url.Values{}
	if len(expand) > 0 {
		q.Set("expand", strings.Join(expand, ","))
	}
	return q
}

// --- Current user ---

// GetCurrentUser returns the authenticated user's profile.
// expand: shipping_address, billing_address. avatarSize (px) sizes the avatar URL; 0 = default.
func (c *Client) GetCurrentUser(ctx context.Context, expand []string, avatarSize int) (json.RawMessage, error) {
	q := expandQuery(expand)
	if avatarSize > 0 {
		q.Set("avatar_size", strconv.Itoa(avatarSize))
	}
	return c.Get(ctx, "/api/v4/user", q)
}

// AvailableIcons returns the mobile-app icon flags the user has unlocked.
func (c *Client) AvailableIcons(ctx context.Context) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/user/available_icons", nil)
}

// GetUser looks up any user by public id (requires admin:read + auditor role).
func (c *Client) GetUser(ctx context.Context, id string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/users/"+url.PathEscape(id), nil)
}

// GetUserByEmail looks up any user by email (requires admin:read + auditor role).
func (c *Client) GetUserByEmail(ctx context.Context, email string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/users/by_email/"+url.PathEscape(email), nil)
}

// TokenInfo returns Doorkeeper's introspection of the current token.
func (c *Client) TokenInfo(ctx context.Context) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/oauth/token/info", nil)
}

// --- Organizations ---

// ListMyOrganizations returns the orgs the user belongs to.
// expand: balance_cents, users, account_number, reporting.
func (c *Client) ListMyOrganizations(ctx context.Context, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/user/organizations", expandQuery(expand))
}

// GetOrganization returns one org by public id (org_…) or slug.
// expand: balance_cents, users, account_number, reporting.
func (c *Client) GetOrganization(ctx context.Context, id string, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(id), expandQuery(expand))
}

// BalanceByDate returns the org's daily running balance series (past year).
func (c *Client) BalanceByDate(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/balance_by_date", nil)
}

// ListFollowers returns the org's followers.
func (c *Client) ListFollowers(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/followers", nil)
}

// ListSubOrganizations returns the org's sub-organizations.
func (c *Client) ListSubOrganizations(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/sub_organizations", nil)
}

// --- Ledger ---

// TransactionFilters mirror the v4 ledger's filters[…] and type params.
type TransactionFilters struct {
	Search          string
	Type            string // ach_transfer, mailed_check, hcb_transfer, card_charge, check_deposit, donation, invoice, refund, fiscal_sponsorship_fee, reimbursement, wire, paypal_transfer, wise_transfer
	TagID           string
	Expenses        bool
	Revenue         bool
	MinimumAmount   string // dollars, e.g. "1.50"
	MaximumAmount   string
	StartDate       string // YYYY-MM-DD
	EndDate         string
	UserID          string
	MissingReceipts bool
	Category        string
	Merchant        string
	OrderBy         string
}

func (f TransactionFilters) apply(q url.Values) {
	set := func(key, val string) {
		if val != "" {
			q.Set("filters["+key+"]", val)
		}
	}
	set("search", f.Search)
	set("tag_id", f.TagID)
	set("minimum_amount", f.MinimumAmount)
	set("maximum_amount", f.MaximumAmount)
	set("start_date", f.StartDate)
	set("end_date", f.EndDate)
	set("user_id", f.UserID)
	set("category", f.Category)
	set("merchant", f.Merchant)
	set("order_by", f.OrderBy)
	if f.Expenses {
		q.Set("filters[expenses]", "true")
	}
	if f.Revenue {
		q.Set("filters[revenue]", "true")
	}
	if f.MissingReceipts {
		q.Set("filters[missing_receipts]", "true")
	}
	if f.Type != "" {
		q.Set("type", f.Type)
	}
}

// ListOrgTransactions returns a page of the org's ledger (pending + settled).
func (c *Client) ListOrgTransactions(ctx context.Context, org string, filters TransactionFilters, page PageOpts) (*Page, error) {
	q := url.Values{}
	filters.apply(q)
	page.apply(q)
	return c.GetPage(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/transactions", q)
}

// GetTransaction returns one transaction (HcbCode). If org is non-empty the
// org-scoped route is used (amounts/memos are rendered from that org's view);
// otherwise the top-level route auto-expands the organization.
func (c *Client) GetTransaction(ctx context.Context, id, org string) (json.RawMessage, error) {
	if org != "" {
		return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/transactions/"+url.PathEscape(id), nil)
	}
	return c.Get(ctx, "/api/v4/transactions/"+url.PathEscape(id), nil)
}

// MemoSuggestions returns up to 4 suggested memos for a transaction.
func (c *Client) MemoSuggestions(ctx context.Context, org, txn string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/transactions/"+url.PathEscape(txn)+"/memo_suggestions", nil)
}

// ListMissingReceiptTransactions returns the user's own card charges still missing a receipt.
func (c *Client) ListMissingReceiptTransactions(ctx context.Context, page PageOpts) (*Page, error) {
	q := url.Values{}
	page.apply(q)
	return c.GetPage(ctx, "/api/v4/user/transactions/missing_receipt", q)
}

// --- Receipts ---

// ListReceipts lists receipts on a transaction, or the user's Receipt Bin if
// transactionID is empty. Each receipt carries signed url/preview_url fields.
func (c *Client) ListReceipts(ctx context.Context, transactionID string) (json.RawMessage, error) {
	q := url.Values{}
	if transactionID != "" {
		q.Set("transaction_id", transactionID)
	}
	return c.Get(ctx, "/api/v4/receipts", q)
}

// --- Comments ---

// ListComments lists a transaction's comments (oldest first).
func (c *Client) ListComments(ctx context.Context, transactionID string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/comments", url.Values{"transaction_id": {transactionID}})
}

// --- Tags ---

// ListTags lists an org's transaction tags.
func (c *Client) ListTags(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/tags", url.Values{"event_id": {org}})
}

// GetTag returns one tag by id (tag_…).
func (c *Client) GetTag(ctx context.Context, id string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/tags/"+url.PathEscape(id), nil)
}

// --- Cards ---

// ListMyCards lists the user's cards across all orgs.
// expand: organization, user, total_spent_cents.
func (c *Client) ListMyCards(ctx context.Context, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/user/cards", expandQuery(expand))
}

// ListOrgCards lists an org's cards.
func (c *Client) ListOrgCards(ctx context.Context, org string, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/cards", expandQuery(expand))
}

// GetCard returns one card (crd_…).
// expand: organization, user, last_frozen_by, total_spent_cents, balance_available.
func (c *Client) GetCard(ctx context.Context, id string, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/cards/"+url.PathEscape(id), expandQuery(expand))
}

// ListCardDesigns lists card personalization designs; org ("" = common only)
// adds that org's designs.
func (c *Client) ListCardDesigns(ctx context.Context, org string) (json.RawMessage, error) {
	q := url.Values{}
	if org != "" {
		q.Set("event_id", org)
	}
	return c.Get(ctx, "/api/v4/cards/card_designs", q)
}

// ListCardTransactions returns a page of a card's charges.
func (c *Client) ListCardTransactions(ctx context.Context, cardID string, missingReceipts bool, page PageOpts) (*Page, error) {
	q := url.Values{}
	if missingReceipts {
		q.Set("missing_receipts", "true")
	}
	page.apply(q)
	return c.GetPage(ctx, "/api/v4/cards/"+url.PathEscape(cardID)+"/transactions", q)
}

// --- Card grants ---

// ListMyCardGrants lists grants the user has received.
func (c *Client) ListMyCardGrants(ctx context.Context, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/user/card_grants", expandQuery(expand))
}

// ListOrgCardGrants lists an org's card grants.
func (c *Client) ListOrgCardGrants(ctx context.Context, org string, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/card_grants", expandQuery(expand))
}

// GetCardGrant returns one grant (cdg_…).
// expand: user, organization, balance_cents, disbursements.
func (c *Client) GetCardGrant(ctx context.Context, id string, expand []string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/card_grants/"+url.PathEscape(id), expandQuery(expand))
}

// ListCardGrantTransactions returns a page of spending on the grant's card.
func (c *Client) ListCardGrantTransactions(ctx context.Context, id string, page PageOpts) (*Page, error) {
	q := url.Values{}
	page.apply(q)
	return c.GetPage(ctx, "/api/v4/card_grants/"+url.PathEscape(id)+"/transactions", q)
}

// --- Invitations ---

// ListMyInvitations lists the user's pending org invitations.
func (c *Client) ListMyInvitations(ctx context.Context) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/user/invitations", nil)
}

// GetInvitation returns one of the user's invitations (ivt_…).
func (c *Client) GetInvitation(ctx context.Context, id string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/user/invitations/"+url.PathEscape(id), nil)
}

// ListOrgInvitations lists an org's pending invitations.
func (c *Client) ListOrgInvitations(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/organizations/"+url.PathEscape(org)+"/invitations", nil)
}

// --- Money movement (read) ---

// ListChecks lists an org's mailed checks.
func (c *Client) ListChecks(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/checks", url.Values{"event_id": {org}})
}

// GetCheck returns one mailed check (chk_…). NOTE: upstream bug — the server's
// policy has no show? so this currently returns 403 for everyone.
func (c *Client) GetCheck(ctx context.Context, id string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/checks/"+url.PathEscape(id), nil)
}

// ListCheckDeposits lists an org's check deposits.
func (c *Client) ListCheckDeposits(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/check_deposits", url.Values{"event_id": {org}})
}

// GetCheckDeposit returns one check deposit (ckd_…).
func (c *Client) GetCheckDeposit(ctx context.Context, id string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/check_deposits/"+url.PathEscape(id), nil)
}

// --- Invoicing & sponsors ---

// ListSponsors lists an org's sponsors.
func (c *Client) ListSponsors(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/sponsors", url.Values{"event_id": {org}})
}

// GetSponsor returns one sponsor (spo_…).
func (c *Client) GetSponsor(ctx context.Context, id string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/sponsors/"+url.PathEscape(id), nil)
}

// ListInvoices lists an org's invoices.
func (c *Client) ListInvoices(ctx context.Context, org string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/invoices", url.Values{"event_id": {org}})
}

// GetInvoice returns one invoice (inv_…).
func (c *Client) GetInvoice(ctx context.Context, id string) (json.RawMessage, error) {
	return c.Get(ctx, "/api/v4/invoices/"+url.PathEscape(id), nil)
}
