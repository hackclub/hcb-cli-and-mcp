package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hackclub/hcb-mcp/internal/hcbapi"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// raw wraps a client call returning raw JSON into an MCP tool handler.
func raw[T any](fn func(ctx context.Context, args T) (json.RawMessage, error)) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args T) (*mcp.CallToolResult, any, error) {
		body, err := fn(ctx, args)
		if err != nil {
			return errResult(err)
		}
		return jsonResult(body), nil, nil
	}
}

func paged[T any](fn func(ctx context.Context, args T) (*hcbapi.Page, error)) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args T) (*mcp.CallToolResult, any, error) {
		p, err := fn(ctx, args)
		if err != nil {
			return errResult(err)
		}
		res, err := pageResult(p)
		if err != nil {
			return errResult(err)
		}
		return res, nil, nil
	}
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

type emptyArgs struct{}

type expandArgs struct {
	Expand string `json:"expand,omitempty" jsonschema:"comma-separated expansions"`
}

type orgArgs struct {
	Organization string `json:"organization" jsonschema:"organization id (org_…) or slug"`
}

type orgExpandArgs struct {
	Organization string `json:"organization" jsonschema:"organization id (org_…) or slug"`
	Expand       string `json:"expand,omitempty" jsonschema:"comma-separated expansions"`
}

type idArgs struct {
	ID string `json:"id" jsonschema:"public id"`
}

type pageArgs struct {
	Limit int    `json:"limit,omitempty" jsonschema:"page size, default 25, max 100"`
	After string `json:"after,omitempty" jsonschema:"cursor: last item id of previous page"`
}

// registerTools registers every tool on server, bound to the given API
// client (the param shadows the package global so closures capture it).
func registerTools(server *mcp.Server, client *hcbapi.Client) {
	// --- current user ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_profile",
		Description: "Get the authenticated user's HCB profile (name, email, avatar). Expansions: shipping_address, billing_address.",
	}, raw(func(ctx context.Context, a struct {
		Expand     string `json:"expand,omitempty" jsonschema:"comma-separated: shipping_address,billing_address"`
		AvatarSize int    `json:"avatar_size,omitempty" jsonschema:"avatar size in pixels"`
	}) (json.RawMessage, error) {
		return client.GetCurrentUser(ctx, split(a.Expand), a.AvatarSize)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_available_icons",
		Description: "Get the app icon flags the authenticated user has unlocked (admin, platinum, frc, etc).",
	}, raw(func(ctx context.Context, a emptyArgs) (json.RawMessage, error) {
		return client.AvailableIcons(ctx)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_lookup_user",
		Description: "Look up any HCB user by public id (usr_…) or email. Requires a token with admin:read; returns 403 otherwise.",
	}, raw(func(ctx context.Context, a struct {
		Query string `json:"query" jsonschema:"a usr_… id or an email address"`
	}) (json.RawMessage, error) {
		if strings.Contains(a.Query, "@") {
			return client.GetUserByEmail(ctx, a.Query)
		}
		return client.GetUser(ctx, a.Query)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_token_info",
		Description: "Inspect the current OAuth token (scopes, expiry, application).",
	}, raw(func(ctx context.Context, a emptyArgs) (json.RawMessage, error) {
		return client.TokenInfo(ctx)
	}))

	// --- organizations ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_organizations",
		Description: "List HCB organizations the user belongs to. Expansions: balance_cents, users, account_number, reporting.",
	}, raw(func(ctx context.Context, a expandArgs) (json.RawMessage, error) {
		return client.ListMyOrganizations(ctx, split(a.Expand))
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_organization",
		Description: "Get an HCB organization by id (org_…) or slug. Expansions: balance_cents (balance), users (team members+roles), account_number (account/routing, permission-gated), reporting (totals).",
	}, raw(func(ctx context.Context, a orgExpandArgs) (json.RawMessage, error) {
		return client.GetOrganization(ctx, a.Organization, split(a.Expand))
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_org_balance_history",
		Description: "Get an organization's daily running balance for the past year (for charts).",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.BalanceByDate(ctx, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_org_followers",
		Description: "List users following an organization.",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.ListFollowers(ctx, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_sub_organizations",
		Description: "List an organization's sub-organizations.",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.ListSubOrganizations(ctx, a.Organization)
	}))

	// --- ledger ---
	mcp.AddTool(server, &mcp.Tool{
		Name: "hcb_list_transactions",
		Description: "List an organization's transactions (pending + settled ledger), newest first, paginated. " +
			"Filter by search text, type (card_charge, ach_transfer, mailed_check, hcb_transfer, check_deposit, donation, invoice, refund, fiscal_sponsorship_fee, reimbursement, wire, paypal_transfer, wise_transfer), date range, amount range (dollars), user, tag, expenses/revenue, missing receipts.",
	}, paged(func(ctx context.Context, a struct {
		Organization    string `json:"organization" jsonschema:"organization id or slug"`
		Search          string `json:"search,omitempty"`
		Type            string `json:"type,omitempty"`
		TagID           string `json:"tag_id,omitempty"`
		Expenses        bool   `json:"expenses,omitempty" jsonschema:"only outgoing money"`
		Revenue         bool   `json:"revenue,omitempty" jsonschema:"only incoming money"`
		MinimumAmount   string `json:"minimum_amount,omitempty" jsonschema:"dollars, e.g. 1.50"`
		MaximumAmount   string `json:"maximum_amount,omitempty" jsonschema:"dollars"`
		StartDate       string `json:"start_date,omitempty" jsonschema:"YYYY-MM-DD"`
		EndDate         string `json:"end_date,omitempty" jsonschema:"YYYY-MM-DD"`
		UserID          string `json:"user_id,omitempty" jsonschema:"usr_… id"`
		MissingReceipts bool   `json:"missing_receipts,omitempty"`
		Category        string `json:"category,omitempty"`
		Merchant        string `json:"merchant,omitempty"`
		Limit           int    `json:"limit,omitempty" jsonschema:"page size, max 100"`
		After           string `json:"after,omitempty" jsonschema:"cursor txn_… id"`
	}) (*hcbapi.Page, error) {
		return client.ListOrgTransactions(ctx, a.Organization, hcbapi.TransactionFilters{
			Search: a.Search, Type: a.Type, TagID: a.TagID, Expenses: a.Expenses, Revenue: a.Revenue,
			MinimumAmount: a.MinimumAmount, MaximumAmount: a.MaximumAmount,
			StartDate: a.StartDate, EndDate: a.EndDate, UserID: a.UserID,
			MissingReceipts: a.MissingReceipts, Category: a.Category, Merchant: a.Merchant,
		}, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_transaction",
		Description: "Get a transaction's full details (memo, amount, tags, receipts status, and type-specific info like merchant for card charges). Pass organization to view from that org's perspective.",
	}, raw(func(ctx context.Context, a struct {
		ID           string `json:"id" jsonschema:"txn_… id"`
		Organization string `json:"organization,omitempty" jsonschema:"optional org id or slug"`
	}) (json.RawMessage, error) {
		return client.GetTransaction(ctx, a.ID, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_memo_suggestions",
		Description: "Get suggested memos for a transaction (up to 4).",
	}, raw(func(ctx context.Context, a struct {
		Organization string `json:"organization" jsonschema:"org id or slug"`
		Transaction  string `json:"transaction" jsonschema:"txn_… id"`
	}) (json.RawMessage, error) {
		return client.MemoSuggestions(ctx, a.Organization, a.Transaction)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_missing_receipts",
		Description: "List the authenticated user's own card transactions still missing receipts, paginated.",
	}, paged(func(ctx context.Context, a pageArgs) (*hcbapi.Page, error) {
		return client.ListMissingReceiptTransactions(ctx, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}))

	// --- receipts & files ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_receipts",
		Description: "List receipts attached to a transaction (pass transaction), or the user's unattached Receipt Bin (omit it). Each receipt has signed url/preview_url fields downloadable via hcb_download_file.",
	}, raw(func(ctx context.Context, a struct {
		Transaction string `json:"transaction,omitempty" jsonschema:"txn_… id; omit for the Receipt Bin"`
	}) (json.RawMessage, error) {
		return client.ListReceipts(ctx, a.Transaction)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_download_receipt",
		Description: "Download a transaction's receipt files to a local directory. Returns the saved paths. Set preview=true for image previews instead of originals.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a struct {
		Transaction string `json:"transaction" jsonschema:"txn_… id"`
		ReceiptID   string `json:"receipt_id,omitempty" jsonschema:"only download this rct_… id"`
		Directory   string `json:"directory,omitempty" jsonschema:"destination directory (default: OS temp dir)"`
		Preview     bool   `json:"preview,omitempty" jsonschema:"download preview image instead of original"`
	}) (*mcp.CallToolResult, any, error) {
		rawList, err := client.ListReceipts(ctx, a.Transaction)
		if err != nil {
			return errResult(err)
		}
		var receipts []struct {
			ID         string  `json:"id"`
			URL        string  `json:"url"`
			PreviewURL *string `json:"preview_url"`
			Filename   string  `json:"filename"`
		}
		if err := json.Unmarshal(rawList, &receipts); err != nil {
			return errResult(fmt.Errorf("parsing receipts: %w", err))
		}
		dir := a.Directory
		if dir == "" {
			dir = tempDir("hcb-receipts")
		}
		var lines []string
		for _, r := range receipts {
			if a.ReceiptID != "" && r.ID != a.ReceiptID {
				continue
			}
			u, name := r.URL, r.Filename
			if a.Preview {
				if r.PreviewURL == nil {
					lines = append(lines, fmt.Sprintf("%s: no preview available", r.ID))
					continue
				}
				u, name = *r.PreviewURL, r.ID+"-preview.png"
			}
			path, err := client.DownloadFile(ctx, u, dir, name)
			if err != nil {
				return errResult(fmt.Errorf("downloading %s: %w", r.ID, err))
			}
			lines = append(lines, fmt.Sprintf("%s -> %s", r.ID, path))
		}
		if len(lines) == 0 {
			lines = []string{"no receipts found"}
		}
		return textResult(strings.Join(lines, "\n")), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_download_file",
		Description: "Download any signed HCB file URL (receipt url/preview_url, comment attachment, check deposit front_url/back_url, org logo) to a local directory. Returns the saved path.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, a struct {
		URL       string `json:"url" jsonschema:"the signed file URL from an HCB API response"`
		Directory string `json:"directory,omitempty" jsonschema:"destination directory (default: OS temp dir)"`
		Filename  string `json:"filename,omitempty" jsonschema:"override the saved filename"`
	}) (*mcp.CallToolResult, any, error) {
		dir := a.Directory
		if dir == "" {
			dir = tempDir("hcb-files")
		}
		path, err := client.DownloadFile(ctx, a.URL, dir, a.Filename)
		if err != nil {
			return errResult(err)
		}
		return textResult(path), nil, nil
	})

	// --- comments & tags ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_comments",
		Description: "List a transaction's comments (oldest first). Comments may carry a file attachment URL.",
	}, raw(func(ctx context.Context, a struct {
		Transaction string `json:"transaction" jsonschema:"txn_… id"`
	}) (json.RawMessage, error) {
		return client.ListComments(ctx, a.Transaction)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_tags",
		Description: "List an organization's transaction tags.",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.ListTags(ctx, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_tag",
		Description: "Get a transaction tag by id (tag_…).",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetTag(ctx, a.ID)
	}))

	// --- cards ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_cards",
		Description: "List the user's cards across all orgs, or one organization's cards if organization is set. Expansions: organization, user, total_spent_cents.",
	}, raw(func(ctx context.Context, a struct {
		Organization string `json:"organization,omitempty" jsonschema:"org id or slug; omit for my cards"`
		Expand       string `json:"expand,omitempty"`
	}) (json.RawMessage, error) {
		if a.Organization != "" {
			return client.ListOrgCards(ctx, a.Organization, split(a.Expand))
		}
		return client.ListMyCards(ctx, split(a.Expand))
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_card",
		Description: "Get a card (crd_…) including status, last4, and shipping status for physical cards. Expansions: organization, user, last_frozen_by, total_spent_cents, balance_available.",
	}, raw(func(ctx context.Context, a struct {
		ID     string `json:"id" jsonschema:"crd_… id"`
		Expand string `json:"expand,omitempty"`
	}) (json.RawMessage, error) {
		return client.GetCard(ctx, a.ID, split(a.Expand))
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_card_transactions",
		Description: "List a card's transactions, paginated. Set missing_receipts=true to only show charges lacking receipts.",
	}, paged(func(ctx context.Context, a struct {
		ID              string `json:"id" jsonschema:"crd_… id"`
		MissingReceipts bool   `json:"missing_receipts,omitempty"`
		Limit           int    `json:"limit,omitempty"`
		After           string `json:"after,omitempty"`
	}) (*hcbapi.Page, error) {
		return client.ListCardTransactions(ctx, a.ID, a.MissingReceipts, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_card_designs",
		Description: "List available card personalization designs (common ones; plus an org's own if organization is set).",
	}, raw(func(ctx context.Context, a struct {
		Organization string `json:"organization,omitempty"`
	}) (json.RawMessage, error) {
		return client.ListCardDesigns(ctx, a.Organization)
	}))

	// --- card grants ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_card_grants",
		Description: "List card grants the user received, or an organization's grants if organization is set. Expansions: user, organization, balance_cents, disbursements.",
	}, raw(func(ctx context.Context, a struct {
		Organization string `json:"organization,omitempty"`
		Expand       string `json:"expand,omitempty"`
	}) (json.RawMessage, error) {
		if a.Organization != "" {
			return client.ListOrgCardGrants(ctx, a.Organization, split(a.Expand))
		}
		return client.ListMyCardGrants(ctx, split(a.Expand))
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_card_grant",
		Description: "Get a card grant (cdg_…): amount, status, spending restrictions, linked card. Expansions: user, organization, balance_cents, disbursements.",
	}, raw(func(ctx context.Context, a struct {
		ID     string `json:"id" jsonschema:"cdg_… id"`
		Expand string `json:"expand,omitempty"`
	}) (json.RawMessage, error) {
		return client.GetCardGrant(ctx, a.ID, split(a.Expand))
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_card_grant_transactions",
		Description: "List spending on a card grant's card, paginated.",
	}, paged(func(ctx context.Context, a struct {
		ID    string `json:"id" jsonschema:"cdg_… id"`
		Limit int    `json:"limit,omitempty"`
		After string `json:"after,omitempty"`
	}) (*hcbapi.Page, error) {
		return client.ListCardGrantTransactions(ctx, a.ID, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}))

	// --- invitations ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_invitations",
		Description: "List the user's pending org invitations, or an organization's pending invitations if organization is set.",
	}, raw(func(ctx context.Context, a struct {
		Organization string `json:"organization,omitempty"`
	}) (json.RawMessage, error) {
		if a.Organization != "" {
			return client.ListOrgInvitations(ctx, a.Organization)
		}
		return client.ListMyInvitations(ctx)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_invitation",
		Description: "Get one of the user's invitations (ivt_…).",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetInvitation(ctx, a.ID)
	}))

	// --- money movement (read) ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_checks",
		Description: "List an organization's mailed checks.",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.ListChecks(ctx, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_check",
		Description: "Get a mailed check (ick_…). KNOWN UPSTREAM BUG: HCB currently returns 403 for everyone on this endpoint; fetch the transaction's check sub-object instead.",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetCheck(ctx, a.ID)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_check_deposits",
		Description: "List an organization's check deposits (status, rejection reason, arrival estimate; front_url/back_url images when permitted).",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.ListCheckDeposits(ctx, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_check_deposit",
		Description: "Get a check deposit (cdp_…).",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetCheckDeposit(ctx, a.ID)
	}))

	// --- invoicing & sponsors ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_sponsors",
		Description: "List an organization's sponsors (invoicing contacts).",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.ListSponsors(ctx, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_sponsor",
		Description: "Get a sponsor (spr_…).",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetSponsor(ctx, a.ID)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_invoices",
		Description: "List an organization's invoices (status, amount due, sponsor).",
	}, raw(func(ctx context.Context, a orgArgs) (json.RawMessage, error) {
		return client.ListInvoices(ctx, a.Organization)
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_invoice",
		Description: "Get an invoice (inv_…).",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetInvoice(ctx, a.ID)
	}))
}
