package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hackclub/hcb-mcp/internal/hcbapi"
	"github.com/spf13/cobra"
)

func splitExpand(expand string) []string {
	if expand == "" {
		return nil
	}
	parts := strings.Split(expand, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// --- current user ---

func userCmd() *cobra.Command {
	var expand string
	var avatarSize int
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Get my profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				return client.GetCurrentUser(ctx(), splitExpand(expand), avatarSize)
			})
		},
	}
	cmd.Flags().StringVar(&expand, "expand", "", "comma-separated: shipping_address,billing_address")
	cmd.Flags().IntVar(&avatarSize, "avatar-size", 0, "avatar size in px")
	return cmd
}

func iconsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "icons",
		Short: "Get my available app icons",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.AvailableIcons(ctx()) })
		},
	}
}

func lookupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "lookup <usr_id | email>",
		Short: "Look up a user by id or email (requires admin:read)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				if strings.Contains(args[0], "@") {
					return client.GetUserByEmail(ctx(), args[0])
				}
				return client.GetUser(ctx(), args[0])
			})
		},
	}
}

// --- organizations ---

func orgsCmd() *cobra.Command {
	var expand string
	cmd := &cobra.Command{
		Use:   "orgs",
		Short: "List organizations I belong to",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				return client.ListMyOrganizations(ctx(), splitExpand(expand))
			})
		},
	}
	cmd.Flags().StringVar(&expand, "expand", "", "comma-separated: balance_cents,users,account_number,reporting")
	return cmd
}

func orgCmd() *cobra.Command {
	var expand string
	cmd := &cobra.Command{
		Use:   "org <org_id | slug>",
		Short: "Get an organization",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				return client.GetOrganization(ctx(), args[0], splitExpand(expand))
			})
		},
	}
	cmd.Flags().StringVar(&expand, "expand", "", "comma-separated: balance_cents,users,account_number,reporting")
	return cmd
}

func balanceHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "balance-history <org>",
		Short: "Get an org's daily balance series (past year)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.BalanceByDate(ctx(), args[0]) })
		},
	}
}

func followersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "followers <org>",
		Short: "List an org's followers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListFollowers(ctx(), args[0]) })
		},
	}
}

func subOrgsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sub-orgs <org>",
		Short: "List an org's sub-organizations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListSubOrganizations(ctx(), args[0]) })
		},
	}
}

// --- ledger ---

func transactionsCmd() *cobra.Command {
	var f hcbapi.TransactionFilters
	var limit int
	var after string
	cmd := &cobra.Command{
		Use:   "transactions <org>",
		Short: "List an org's transactions (ledger), with filters",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPage(func() (*hcbapi.Page, error) {
				return client.ListOrgTransactions(ctx(), args[0], f, hcbapi.PageOpts{Limit: limit, After: after})
			})
		},
	}
	cmd.Flags().StringVar(&f.Search, "search", "", "full-text search")
	cmd.Flags().StringVar(&f.Type, "type", "", "ach_transfer|mailed_check|hcb_transfer|card_charge|check_deposit|donation|invoice|refund|fiscal_sponsorship_fee|reimbursement|wire|paypal_transfer|wise_transfer")
	cmd.Flags().StringVar(&f.TagID, "tag", "", "tag_… id")
	cmd.Flags().BoolVar(&f.Expenses, "expenses", false, "only expenses")
	cmd.Flags().BoolVar(&f.Revenue, "revenue", false, "only revenue")
	cmd.Flags().StringVar(&f.MinimumAmount, "min", "", "minimum amount in dollars")
	cmd.Flags().StringVar(&f.MaximumAmount, "max", "", "maximum amount in dollars")
	cmd.Flags().StringVar(&f.StartDate, "start", "", "start date YYYY-MM-DD")
	cmd.Flags().StringVar(&f.EndDate, "end", "", "end date YYYY-MM-DD")
	cmd.Flags().StringVar(&f.UserID, "user", "", "usr_… id")
	cmd.Flags().BoolVar(&f.MissingReceipts, "missing-receipts", false, "only transactions missing receipts")
	cmd.Flags().StringVar(&f.Category, "category", "", "category filter")
	cmd.Flags().StringVar(&f.Merchant, "merchant", "", "merchant filter")
	cmd.Flags().StringVar(&f.OrderBy, "order-by", "", "sort order")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (max 100)")
	cmd.Flags().StringVar(&after, "after", "", "cursor: txn_… id")
	return cmd
}

func transactionCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "transaction <txn_id>",
		Short: "Get a transaction's full details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.GetTransaction(ctx(), args[0], org) })
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "view from this org's perspective")
	return cmd
}

func memoSuggestionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "memo-suggestions <org> <txn_id>",
		Short: "Get suggested memos for a transaction",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.MemoSuggestions(ctx(), args[0], args[1]) })
		},
	}
}

func missingReceiptsCmd() *cobra.Command {
	var limit int
	var after string
	cmd := &cobra.Command{
		Use:   "missing-receipts",
		Short: "List my transactions still missing receipts",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPage(func() (*hcbapi.Page, error) {
				return client.ListMissingReceiptTransactions(ctx(), hcbapi.PageOpts{Limit: limit, After: after})
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (max 100)")
	cmd.Flags().StringVar(&after, "after", "", "cursor: txn_… id")
	return cmd
}

// --- receipts & files ---

func receiptsCmd() *cobra.Command {
	var transaction string
	cmd := &cobra.Command{
		Use:   "receipts",
		Short: "List my Receipt Bin, or a transaction's receipts with --transaction",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListReceipts(ctx(), transaction) })
		},
	}
	cmd.Flags().StringVar(&transaction, "transaction", "", "txn_… id")
	return cmd
}

func receiptDownloadCmd() *cobra.Command {
	var out, receiptID string
	var preview, bin bool
	cmd := &cobra.Command{
		Use:   "receipt-download [txn_id]",
		Short: "Download receipt files for a transaction (or --bin for your Receipt Bin)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			txn := ""
			if len(args) == 1 {
				txn = args[0]
			}
			if !bin && txn == "" {
				return fmt.Errorf("pass a txn_… id or --bin")
			}
			raw, err := client.ListReceipts(ctx(), txn)
			if err != nil {
				return err
			}
			var receipts []struct {
				ID         string  `json:"id"`
				URL        string  `json:"url"`
				PreviewURL *string `json:"preview_url"`
				Filename   string  `json:"filename"`
			}
			if err := json.Unmarshal(raw, &receipts); err != nil {
				return fmt.Errorf("parsing receipts: %w", err)
			}
			if len(receipts) == 0 {
				fmt.Println("no receipts found")
				return nil
			}
			for _, r := range receipts {
				if receiptID != "" && r.ID != receiptID {
					continue
				}
				u, name := r.URL, r.Filename
				if preview {
					if r.PreviewURL == nil {
						fmt.Printf("%s: no preview available\n", r.ID)
						continue
					}
					u, name = *r.PreviewURL, r.ID+"-preview.png"
				}
				path, err := client.DownloadFile(ctx(), u, out, name)
				if err != nil {
					return fmt.Errorf("downloading %s: %w", r.ID, err)
				}
				fmt.Printf("%s -> %s\n", r.ID, path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", ".", "destination directory")
	cmd.Flags().StringVar(&receiptID, "receipt", "", "only this rct_… id")
	cmd.Flags().BoolVar(&preview, "preview", false, "download the preview image instead of the original")
	cmd.Flags().BoolVar(&bin, "bin", false, "download from my Receipt Bin")
	return cmd
}

func downloadCmd() *cobra.Command {
	var out, name string
	cmd := &cobra.Command{
		Use:   "download <url>",
		Short: "Download any signed HCB file URL (org logos, comment attachments, check deposit images)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := client.DownloadFile(ctx(), args[0], out, name)
			if err != nil {
				return err
			}
			fmt.Println(path)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", ".", "destination directory")
	cmd.Flags().StringVar(&name, "name", "", "override filename")
	return cmd
}

// --- comments & tags ---

func commentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "comments <txn_id>",
		Short: "List a transaction's comments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListComments(ctx(), args[0]) })
		},
	}
}

func tagsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tags <org>",
		Short: "List an org's transaction tags",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListTags(ctx(), args[0]) })
		},
	}
}

func tagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <tag_id>",
		Short: "Get a tag",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.GetTag(ctx(), args[0]) })
		},
	}
}

// --- cards ---

func cardsCmd() *cobra.Command {
	var org, expand string
	cmd := &cobra.Command{
		Use:   "cards",
		Short: "List my cards, or an org's cards with --org",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				if org != "" {
					return client.ListOrgCards(ctx(), org, splitExpand(expand))
				}
				return client.ListMyCards(ctx(), splitExpand(expand))
			})
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "org id or slug")
	cmd.Flags().StringVar(&expand, "expand", "", "comma-separated: organization,user,total_spent_cents")
	return cmd
}

func cardCmd() *cobra.Command {
	var expand string
	cmd := &cobra.Command{
		Use:   "card <crd_id>",
		Short: "Get a card (incl. shipping status for physical cards)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				return client.GetCard(ctx(), args[0], splitExpand(expand))
			})
		},
	}
	cmd.Flags().StringVar(&expand, "expand", "", "comma-separated: organization,user,last_frozen_by,total_spent_cents,balance_available")
	return cmd
}

func cardTransactionsCmd() *cobra.Command {
	var missing bool
	var limit int
	var after string
	cmd := &cobra.Command{
		Use:   "card-transactions <crd_id>",
		Short: "List a card's transactions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPage(func() (*hcbapi.Page, error) {
				return client.ListCardTransactions(ctx(), args[0], missing, hcbapi.PageOpts{Limit: limit, After: after})
			})
		},
	}
	cmd.Flags().BoolVar(&missing, "missing-receipts", false, "only charges missing receipts")
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (max 100)")
	cmd.Flags().StringVar(&after, "after", "", "cursor: txn_… id")
	return cmd
}

func cardDesignsCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "card-designs",
		Short: "List available card designs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListCardDesigns(ctx(), org) })
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "include this org's designs")
	return cmd
}

// --- card grants ---

func grantsCmd() *cobra.Command {
	var org, expand string
	cmd := &cobra.Command{
		Use:   "grants",
		Short: "List my card grants, or an org's with --org",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				if org != "" {
					return client.ListOrgCardGrants(ctx(), org, splitExpand(expand))
				}
				return client.ListMyCardGrants(ctx(), splitExpand(expand))
			})
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "org id or slug")
	cmd.Flags().StringVar(&expand, "expand", "", "comma-separated: user,organization,balance_cents,disbursements")
	return cmd
}

func grantCmd() *cobra.Command {
	var expand string
	cmd := &cobra.Command{
		Use:   "grant <cdg_id>",
		Short: "Get a card grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				return client.GetCardGrant(ctx(), args[0], splitExpand(expand))
			})
		},
	}
	cmd.Flags().StringVar(&expand, "expand", "", "comma-separated: user,organization,balance_cents,disbursements")
	return cmd
}

func grantTransactionsCmd() *cobra.Command {
	var limit int
	var after string
	cmd := &cobra.Command{
		Use:   "grant-transactions <cdg_id>",
		Short: "List spending on a card grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPage(func() (*hcbapi.Page, error) {
				return client.ListCardGrantTransactions(ctx(), args[0], hcbapi.PageOpts{Limit: limit, After: after})
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "page size (max 100)")
	cmd.Flags().StringVar(&after, "after", "", "cursor: txn_… id")
	return cmd
}

// --- invitations ---

func invitationsCmd() *cobra.Command {
	var org string
	cmd := &cobra.Command{
		Use:   "invitations",
		Short: "List my pending invitations, or an org's with --org",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) {
				if org != "" {
					return client.ListOrgInvitations(ctx(), org)
				}
				return client.ListMyInvitations(ctx())
			})
		},
	}
	cmd.Flags().StringVar(&org, "org", "", "org id or slug")
	return cmd
}

func invitationCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "invitation <ivt_id>",
		Short: "Get one of my invitations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.GetInvitation(ctx(), args[0]) })
		},
	}
}

// --- money movement (read) ---

func checksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "checks <org>",
		Short: "List an org's mailed checks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListChecks(ctx(), args[0]) })
		},
	}
}

func checkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check <ick_id>",
		Short: "Get a mailed check (NOTE: upstream bug — currently 403s for everyone)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.GetCheck(ctx(), args[0]) })
		},
	}
}

func checkDepositsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check-deposits <org>",
		Short: "List an org's check deposits",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListCheckDeposits(ctx(), args[0]) })
		},
	}
}

func checkDepositCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check-deposit <cdp_id>",
		Short: "Get a check deposit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.GetCheckDeposit(ctx(), args[0]) })
		},
	}
}

// --- invoicing & sponsors ---

func sponsorsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sponsors <org>",
		Short: "List an org's sponsors",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListSponsors(ctx(), args[0]) })
		},
	}
}

func sponsorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sponsor <spr_id>",
		Short: "Get a sponsor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.GetSponsor(ctx(), args[0]) })
		},
	}
}

func invoicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "invoices <org>",
		Short: "List an org's invoices",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.ListInvoices(ctx(), args[0]) })
		},
	}
}

func invoiceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "invoice <inv_id>",
		Short: "Get an invoice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(func() (json.RawMessage, error) { return client.GetInvoice(ctx(), args[0]) })
		},
	}
}
