package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hackclub/hcb-cli-and-mcp/internal/hcbapi"
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
	return pagedCompact(fn, func(T) bool { return false })
}

// pagedCompact is paged with per-call transaction compaction: when
// isCompact(args) is true the page's data array is rewritten into one small
// summary object per transaction.
func pagedCompact[T any](fn func(ctx context.Context, args T) (*hcbapi.Page, error), isCompact func(T) bool) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, args T) (*mcp.CallToolResult, any, error) {
		p, err := fn(ctx, args)
		if err != nil {
			return errResult(err)
		}
		if isCompact(args) {
			if p, err = hcbapi.CompactPage(p); err != nil {
				return errResult(err)
			}
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

type compactPageArgs struct {
	Compact bool   `json:"compact,omitempty" jsonschema:"return one small summary object per transaction (id, date, amount, memo, type, counterparty) instead of full nested detail — strongly recommended for limits above ~25 to keep results small"`
	Limit   int    `json:"limit,omitempty" jsonschema:"page size, default 25, max 100"`
	After   string `json:"after,omitempty" jsonschema:"cursor: last item id of previous page"`
}

type toolOptions struct {
	AllowLocalFileWrites bool
}

// registerTools registers every tool on server, bound to the given API
// client (the param shadows the package global so closures capture it).
func registerTools(server *mcp.Server, client *hcbapi.Client) {
	registerToolsWithOptions(server, client, toolOptions{AllowLocalFileWrites: true})
}

func registerToolsWithOptions(server *mcp.Server, client *hcbapi.Client, opts toolOptions) {
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
		var body json.RawMessage
		var err error
		if strings.Contains(a.Query, "@") {
			body, err = client.GetUserByEmail(ctx, a.Query)
		} else {
			body, err = client.GetUser(ctx, a.Query)
		}
		var apiErr *hcbapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 403 {
			return nil, fmt.Errorf("%w — user lookup requires the admin:read scope on an auditor/admin HCB account; this 403 is about the current token's scope, not the user you searched for", err)
		}
		return body, err
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
	type listTransactionsArgs struct {
		Organization    string `json:"organization" jsonschema:"organization id or slug"`
		Search          string `json:"search,omitempty" jsonschema:"case-insensitive substring match on memo text ONLY — it will not find counterparty/recipient/merchant names; use type/user/tag/amount/date filters for those"`
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
		Compact         bool   `json:"compact,omitempty" jsonschema:"return one small summary object per transaction (id, date, amount, memo, type, counterparty) instead of full nested detail — strongly recommended for limits above ~25 to keep results small"`
		Limit           int    `json:"limit,omitempty" jsonschema:"page size, max 100"`
		After           string `json:"after,omitempty" jsonschema:"cursor txn_… id"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name: "hcb_list_transactions",
		Description: "List an organization's transactions (pending + settled ledger), newest first, paginated. " +
			"Filter by search text (matches memo text only), type (card_charge, ach_transfer, mailed_check, hcb_transfer, check_deposit, donation, invoice, refund, fiscal_sponsorship_fee, reimbursement, wire, paypal_transfer, wise_transfer), date range, amount range (dollars), user, tag, expenses/revenue, missing receipts. " +
			"Set compact=true for large pages: full results with limit 100 can exceed client token limits.",
	}, pagedCompact(func(ctx context.Context, a listTransactionsArgs) (*hcbapi.Page, error) {
		return client.ListOrgTransactions(ctx, a.Organization, hcbapi.TransactionFilters{
			Search: a.Search, Type: a.Type, TagID: a.TagID, Expenses: a.Expenses, Revenue: a.Revenue,
			MinimumAmount: a.MinimumAmount, MaximumAmount: a.MaximumAmount,
			StartDate: a.StartDate, EndDate: a.EndDate, UserID: a.UserID,
			MissingReceipts: a.MissingReceipts, Category: a.Category, Merchant: a.Merchant,
		}, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}, func(a listTransactionsArgs) bool { return a.Compact }))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_transaction",
		Description: "Get a transaction's full details (memo, amount, tags, receipts status, and type-specific info like merchant for card charges). Pass organization to view from that org's perspective. Only txn_… public ids work — internal HCB-… hcb_codes cannot be looked up directly.",
	}, raw(func(ctx context.Context, a struct {
		ID           string `json:"id" jsonschema:"txn_… id (not an HCB-… code)"`
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
	}, pagedCompact(func(ctx context.Context, a compactPageArgs) (*hcbapi.Page, error) {
		return client.ListMissingReceiptTransactions(ctx, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}, func(a compactPageArgs) bool { return a.Compact }))

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
		if !opts.AllowLocalFileWrites {
			return errResult(fmt.Errorf("file downloads are disabled in HTTP mode"))
		}
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
		if !opts.AllowLocalFileWrites {
			return errResult(fmt.Errorf("file downloads are disabled in HTTP mode"))
		}
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

	type cardTransactionsArgs struct {
		ID              string `json:"id" jsonschema:"crd_… id"`
		MissingReceipts bool   `json:"missing_receipts,omitempty"`
		Compact         bool   `json:"compact,omitempty" jsonschema:"return one small summary object per transaction instead of full nested detail"`
		Limit           int    `json:"limit,omitempty"`
		After           string `json:"after,omitempty"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_card_transactions",
		Description: "List a card's transactions, paginated. Set missing_receipts=true to only show charges lacking receipts.",
	}, pagedCompact(func(ctx context.Context, a cardTransactionsArgs) (*hcbapi.Page, error) {
		return client.ListCardTransactions(ctx, a.ID, a.MissingReceipts, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}, func(a cardTransactionsArgs) bool { return a.Compact }))

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

	type grantTransactionsArgs struct {
		ID      string `json:"id" jsonschema:"cdg_… id"`
		Compact bool   `json:"compact,omitempty" jsonschema:"return one small summary object per transaction instead of full nested detail"`
		Limit   int    `json:"limit,omitempty"`
		After   string `json:"after,omitempty"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_card_grant_transactions",
		Description: "List spending on a card grant's card, paginated.",
	}, pagedCompact(func(ctx context.Context, a grantTransactionsArgs) (*hcbapi.Page, error) {
		return client.ListCardGrantTransactions(ctx, a.ID, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}, func(a grantTransactionsArgs) bool { return a.Compact }))

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

	// --- donations ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_donations",
		Description: "List an organization's donations (newest first), paginated: donor name/email, amount, payment method, status. Filter with status (e.g. deposited, in_transit); expand=stats adds total_cents successfully raised.",
	}, raw(func(ctx context.Context, a struct {
		Organization string `json:"organization" jsonschema:"organization id (org_…) or slug"`
		Status       string `json:"status,omitempty" jsonschema:"filter by state, e.g. deposited or in_transit"`
		Expand       string `json:"expand,omitempty" jsonschema:"comma-separated: stats (adds total_cents raised)"`
		Limit        int    `json:"limit,omitempty" jsonschema:"page size, max 100"`
		After        string `json:"after,omitempty" jsonschema:"cursor: don_… id"`
	}) (json.RawMessage, error) {
		return client.ListDonations(ctx, a.Organization, a.Status, split(a.Expand), hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_donation",
		Description: "Get a donation (don_…): donor, amount, payment method, attribution, status.",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetDonation(ctx, a.ID)
	}))

	// --- wires ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_list_wires",
		Description: "List an organization's outgoing international wire transfers (newest first), paginated: recipient, amount, currency, state.",
	}, paged(func(ctx context.Context, a struct {
		Organization string `json:"organization" jsonschema:"organization id (org_…) or slug"`
		Limit        int    `json:"limit,omitempty" jsonschema:"page size, max 100"`
		After        string `json:"after,omitempty" jsonschema:"cursor: wir_… id"`
	}) (*hcbapi.Page, error) {
		return client.ListWires(ctx, a.Organization, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}))

	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_get_wire",
		Description: "Get an international wire transfer (wir_…): recipient, amount, currency, state, sender.",
	}, raw(func(ctx context.Context, a idArgs) (json.RawMessage, error) {
		return client.GetWire(ctx, a.ID)
	}))

	// --- team ---
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hcb_org_team",
		Description: "List an organization's team members (organizer positions), paginated: each member's user record with their role (manager/member/reader) and whether they are a signee. Richer than hcb_get_organization expand=users.",
	}, paged(func(ctx context.Context, a struct {
		Organization string `json:"organization" jsonschema:"organization id (org_…) or slug"`
		Limit        int    `json:"limit,omitempty" jsonschema:"page size, max 100"`
		After        string `json:"after,omitempty" jsonschema:"cursor: opn_… id"`
	}) (*hcbapi.Page, error) {
		return client.ListOrganizerPositions(ctx, a.Organization, []string{"user"}, hcbapi.PageOpts{Limit: a.Limit, After: a.After})
	}))
}
