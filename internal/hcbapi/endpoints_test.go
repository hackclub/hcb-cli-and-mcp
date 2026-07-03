package hcbapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Table-driven test: every endpoint method must hit the right path+query and
// return the recorded fixture body verbatim.
func TestEndpoints(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name      string
		fixture   string
		wantPath  string
		wantQuery url.Values
		call      func(c *Client) (any, error)
	}{
		{
			name: "GetCurrentUser", fixture: "user.json",
			wantPath:  "/api/v4/user",
			wantQuery: url.Values{"expand": {"shipping_address,billing_address"}, "avatar_size": {"128"}},
			call: func(c *Client) (any, error) {
				return c.GetCurrentUser(ctx, []string{"shipping_address", "billing_address"}, 128)
			},
		},
		{
			name: "GetCurrentUserPlain", fixture: "user.json",
			wantPath: "/api/v4/user", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetCurrentUser(ctx, nil, 0) },
		},
		{
			name: "AvailableIcons", fixture: "available_icons.json",
			wantPath: "/api/v4/user/available_icons", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.AvailableIcons(ctx) },
		},
		{
			name: "GetUser", fixture: "user.json",
			wantPath: "/api/v4/users/usr_abc123", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetUser(ctx, "usr_abc123") },
		},
		{
			name: "GetUserByEmail", fixture: "user.json",
			wantPath: "/api/v4/users/by_email/user@example.com", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetUserByEmail(ctx, "user@example.com") },
		},
		{
			name: "TokenInfo", fixture: "token_info.json",
			wantPath: "/api/v4/oauth/token/info", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.TokenInfo(ctx) },
		},
		{
			name: "ListMyOrganizations", fixture: "user_organizations.json",
			wantPath: "/api/v4/user/organizations", wantQuery: url.Values{"expand": {"balance_cents"}},
			call: func(c *Client) (any, error) { return c.ListMyOrganizations(ctx, []string{"balance_cents"}) },
		},
		{
			name: "GetOrganization", fixture: "organization.json",
			wantPath:  "/api/v4/organizations/org_Ky1EWQ",
			wantQuery: url.Values{"expand": {"balance_cents,users"}},
			call: func(c *Client) (any, error) {
				return c.GetOrganization(ctx, "org_Ky1EWQ", []string{"balance_cents", "users"})
			},
		},
		{
			name: "BalanceByDate", fixture: "balance_by_date.json",
			wantPath: "/api/v4/organizations/hq/balance_by_date", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.BalanceByDate(ctx, "hq") },
		},
		{
			name: "ListFollowers", fixture: "followers.json",
			wantPath: "/api/v4/organizations/hq/followers", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListFollowers(ctx, "hq") },
		},
		{
			name: "ListSubOrganizations", fixture: "sub_organizations.json",
			wantPath: "/api/v4/organizations/hq/sub_organizations", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListSubOrganizations(ctx, "hq") },
		},
		{
			name: "ListOrgTransactions", fixture: "transactions.json",
			wantPath: "/api/v4/organizations/hq/transactions",
			wantQuery: url.Values{
				"limit": {"5"}, "after": {"txn_cursor"}, "type": {"card_charge"},
				"filters[search]":           {"pizza"},
				"filters[start_date]":       {"2026-01-01"},
				"filters[end_date]":         {"2026-06-30"},
				"filters[minimum_amount]":   {"1.50"},
				"filters[maximum_amount]":   {"100"},
				"filters[user_id]":          {"usr_abc123"},
				"filters[tag_id]":           {"tag_abc"},
				"filters[missing_receipts]": {"true"},
				"filters[expenses]":         {"true"},
				"filters[category]":         {"food"},
				"filters[merchant]":         {"DOMINOS"},
				"filters[order_by]":         {"amount"},
			},
			call: func(c *Client) (any, error) {
				return c.ListOrgTransactions(ctx, "hq", TransactionFilters{
					Search: "pizza", Type: "card_charge", StartDate: "2026-01-01", EndDate: "2026-06-30",
					MinimumAmount: "1.50", MaximumAmount: "100", UserID: "usr_abc123", TagID: "tag_abc",
					MissingReceipts: true, Expenses: true, Category: "food", Merchant: "DOMINOS", OrderBy: "amount",
				}, PageOpts{Limit: 5, After: "txn_cursor"})
			},
		},
		{
			name: "ListOrgTransactionsPlain", fixture: "transactions.json",
			wantPath: "/api/v4/organizations/hq/transactions", wantQuery: url.Values{},
			call: func(c *Client) (any, error) {
				return c.ListOrgTransactions(ctx, "hq", TransactionFilters{}, PageOpts{})
			},
		},
		{
			name: "GetTransaction", fixture: "transaction.json",
			wantPath: "/api/v4/transactions/txn_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetTransaction(ctx, "txn_abc", "") },
		},
		{
			name: "GetTransactionOrgScoped", fixture: "transaction.json",
			wantPath: "/api/v4/organizations/hq/transactions/txn_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetTransaction(ctx, "txn_abc", "hq") },
		},
		{
			name: "MemoSuggestions", fixture: "memo_suggestions.json",
			wantPath: "/api/v4/organizations/hq/transactions/txn_abc/memo_suggestions", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.MemoSuggestions(ctx, "hq", "txn_abc") },
		},
		{
			name: "ListMissingReceiptTransactions", fixture: "missing_receipt.json",
			wantPath:  "/api/v4/user/transactions/missing_receipt",
			wantQuery: url.Values{"limit": {"2"}},
			call: func(c *Client) (any, error) {
				return c.ListMissingReceiptTransactions(ctx, PageOpts{Limit: 2})
			},
		},
		{
			name: "ListReceiptsBin", fixture: "receipts_bin.json",
			wantPath: "/api/v4/receipts", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListReceipts(ctx, "") },
		},
		{
			name: "ListReceiptsTxn", fixture: "receipts_txn.json",
			wantPath: "/api/v4/receipts", wantQuery: url.Values{"transaction_id": {"txn_abc"}},
			call: func(c *Client) (any, error) { return c.ListReceipts(ctx, "txn_abc") },
		},
		{
			name: "ListComments", fixture: "comments.json",
			wantPath: "/api/v4/comments", wantQuery: url.Values{"transaction_id": {"txn_abc"}},
			call: func(c *Client) (any, error) { return c.ListComments(ctx, "txn_abc") },
		},
		{
			name: "ListTags", fixture: "tags.json",
			wantPath: "/api/v4/tags", wantQuery: url.Values{"event_id": {"hq"}},
			call: func(c *Client) (any, error) { return c.ListTags(ctx, "hq") },
		},
		{
			name: "GetTag", fixture: "tag.json",
			wantPath: "/api/v4/tags/tag_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetTag(ctx, "tag_abc") },
		},
		{
			name: "ListMyCards", fixture: "user_cards.json",
			wantPath: "/api/v4/user/cards", wantQuery: url.Values{"expand": {"organization"}},
			call: func(c *Client) (any, error) { return c.ListMyCards(ctx, []string{"organization"}) },
		},
		{
			name: "ListOrgCards", fixture: "org_cards.json",
			wantPath: "/api/v4/organizations/hq/cards", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListOrgCards(ctx, "hq", nil) },
		},
		{
			name: "GetCard", fixture: "card.json",
			wantPath: "/api/v4/cards/crd_abc", wantQuery: url.Values{"expand": {"total_spent_cents"}},
			call: func(c *Client) (any, error) { return c.GetCard(ctx, "crd_abc", []string{"total_spent_cents"}) },
		},
		{
			name: "ListCardDesigns", fixture: "card_designs.json",
			wantPath: "/api/v4/cards/card_designs", wantQuery: url.Values{"event_id": {"hq"}},
			call: func(c *Client) (any, error) { return c.ListCardDesigns(ctx, "hq") },
		},
		{
			name: "ListCardDesignsCommon", fixture: "card_designs.json",
			wantPath: "/api/v4/cards/card_designs", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListCardDesigns(ctx, "") },
		},
		{
			name: "ListCardTransactions", fixture: "card_transactions.json",
			wantPath:  "/api/v4/cards/crd_abc/transactions",
			wantQuery: url.Values{"limit": {"3"}, "missing_receipts": {"true"}},
			call: func(c *Client) (any, error) {
				return c.ListCardTransactions(ctx, "crd_abc", true, PageOpts{Limit: 3})
			},
		},
		{
			name: "ListMyCardGrants", fixture: "user_card_grants.json",
			wantPath: "/api/v4/user/card_grants", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListMyCardGrants(ctx, nil) },
		},
		{
			name: "ListOrgCardGrants", fixture: "org_card_grants.json",
			wantPath: "/api/v4/organizations/hq/card_grants", wantQuery: url.Values{"expand": {"balance_cents"}},
			call: func(c *Client) (any, error) { return c.ListOrgCardGrants(ctx, "hq", []string{"balance_cents"}) },
		},
		{
			name: "GetCardGrant", fixture: "card_grant.json",
			wantPath: "/api/v4/card_grants/cdg_abc", wantQuery: url.Values{"expand": {"balance_cents,disbursements"}},
			call: func(c *Client) (any, error) {
				return c.GetCardGrant(ctx, "cdg_abc", []string{"balance_cents", "disbursements"})
			},
		},
		{
			name: "ListCardGrantTransactions", fixture: "card_grant_transactions.json",
			wantPath: "/api/v4/card_grants/cdg_abc/transactions", wantQuery: url.Values{"after": {"txn_x"}},
			call: func(c *Client) (any, error) {
				return c.ListCardGrantTransactions(ctx, "cdg_abc", PageOpts{After: "txn_x"})
			},
		},
		{
			name: "ListMyInvitations", fixture: "user_invitations.json",
			wantPath: "/api/v4/user/invitations", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListMyInvitations(ctx) },
		},
		{
			name: "GetInvitation", fixture: "user_invitations.json",
			wantPath: "/api/v4/user/invitations/ivt_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetInvitation(ctx, "ivt_abc") },
		},
		{
			name: "ListOrgInvitations", fixture: "org_invitations.json",
			wantPath: "/api/v4/organizations/hq/invitations", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.ListOrgInvitations(ctx, "hq") },
		},
		{
			name: "ListChecks", fixture: "checks.json",
			wantPath: "/api/v4/checks", wantQuery: url.Values{"event_id": {"hq"}},
			call: func(c *Client) (any, error) { return c.ListChecks(ctx, "hq") },
		},
		{
			name: "GetCheck", fixture: "checks.json",
			wantPath: "/api/v4/checks/chk_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetCheck(ctx, "chk_abc") },
		},
		{
			name: "ListCheckDeposits", fixture: "check_deposits.json",
			wantPath: "/api/v4/check_deposits", wantQuery: url.Values{"event_id": {"hq"}},
			call: func(c *Client) (any, error) { return c.ListCheckDeposits(ctx, "hq") },
		},
		{
			name: "GetCheckDeposit", fixture: "check_deposit.json",
			wantPath: "/api/v4/check_deposits/ckd_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetCheckDeposit(ctx, "ckd_abc") },
		},
		{
			name: "ListSponsors", fixture: "sponsors.json",
			wantPath: "/api/v4/sponsors", wantQuery: url.Values{"event_id": {"hq"}},
			call: func(c *Client) (any, error) { return c.ListSponsors(ctx, "hq") },
		},
		{
			name: "GetSponsor", fixture: "sponsor.json",
			wantPath: "/api/v4/sponsors/spo_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetSponsor(ctx, "spo_abc") },
		},
		{
			name: "ListInvoices", fixture: "invoices.json",
			wantPath: "/api/v4/invoices", wantQuery: url.Values{"event_id": {"hq"}},
			call: func(c *Client) (any, error) { return c.ListInvoices(ctx, "hq") },
		},
		{
			name: "GetInvoice", fixture: "invoice.json",
			wantPath: "/api/v4/invoices/inv_abc", wantQuery: url.Values{},
			call: func(c *Client) (any, error) { return c.GetInvoice(ctx, "inv_abc") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			if err != nil {
				t.Fatalf("fixture %s: %v", tc.fixture, err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.wantPath {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.wantPath)
				}
				if got := r.URL.Query(); !reflect.DeepEqual(got, tc.wantQuery) {
					t.Errorf("query = %v, want %v", got, tc.wantQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(fixture)
			}))
			defer srv.Close()

			c := newTestClient(t, srv, nil)
			got, err := tc.call(c)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			// The result must round-trip to the same JSON as the fixture.
			var gotJSON []byte
			switch v := got.(type) {
			case json.RawMessage:
				gotJSON = v
			case *Page:
				var envelope map[string]json.RawMessage
				if err := json.Unmarshal(fixture, &envelope); err != nil {
					t.Fatalf("fixture not an envelope: %v", err)
				}
				if !jsonEqual(t, v.Data, envelope["data"]) {
					t.Errorf("page data mismatch")
				}
				return
			default:
				t.Fatalf("unexpected return type %T", got)
			}
			if !jsonEqual(t, gotJSON, fixture) {
				t.Errorf("body mismatch for %s", tc.name)
			}
		})
	}
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}
