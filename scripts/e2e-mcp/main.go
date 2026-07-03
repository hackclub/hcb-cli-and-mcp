// Manual E2E for the MCP server: spawns the real hcb-mcp binary over stdio and
// calls every tool against production HCB (read-only). All IDs are discovered
// dynamically from data the authenticated user can read — nothing hardcoded.
//
// Usage: go run ./scripts/e2e-mcp [path-to-hcb-mcp-binary]
//
//	HCB_E2E_ORG overrides the org used for org-scoped checks (default: hq)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tc struct {
	tool      string
	args      map[string]any
	expectErr bool
	contains  string // substring expected in the result text
}

var session *mcp.ClientSession

func call(ctx context.Context, tool string, args map[string]any) (string, bool, error) {
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", false, err
	}
	return snippet(res), res.IsError, nil
}

// firstID extracts the first object id from a JSON array or {data: []} envelope.
func firstID(text string) string {
	var arr []map[string]any
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		var env struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(text), &env); err != nil {
			return ""
		}
		arr = env.Data
	}
	if len(arr) == 0 {
		return ""
	}
	id, _ := arr[0]["id"].(string)
	return id
}

func main() {
	bin := "./bin/hcb-mcp"
	if len(os.Args) > 1 {
		bin = os.Args[1]
	}
	org := os.Getenv("HCB_E2E_ORG")
	if org == "" {
		org = "hq"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0.0.1"}, nil)
	var err error
	session, err = client.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin)}, nil)
	if err != nil {
		fmt.Println("FATAL: connect:", err)
		os.Exit(1)
	}
	defer session.Close()

	// --- discovery phase (via the tools themselves) ---
	discover := func(tool string, args map[string]any) string {
		text, isErr, err := call(ctx, tool, args)
		if err != nil || isErr {
			return ""
		}
		return firstID(text)
	}
	profileText, _, _ := call(ctx, "hcb_get_profile", map[string]any{})
	var profile struct {
		ID string `json:"id"`
	}
	json.Unmarshal([]byte(profileText), &profile)

	var txn, rtxn string // any transaction; transaction with receipts
	if text, isErr, err := call(ctx, "hcb_list_transactions", map[string]any{"organization": org, "limit": 25}); err == nil && !isErr {
		var env struct {
			Data []map[string]any `json:"data"`
		}
		json.Unmarshal([]byte(text), &env)
		for _, tx := range env.Data {
			id, _ := tx["id"].(string)
			if txn == "" {
				txn = id
			}
			if rtxn == "" {
				if missing, ok := tx["missing_receipt"].(bool); ok && !missing {
					if rt, isErr2, err2 := call(ctx, "hcb_list_receipts", map[string]any{"transaction": id}); err2 == nil && !isErr2 && firstID(rt) != "" {
						rtxn = id
					}
				}
			}
		}
	}
	if rtxn == "" {
		rtxn = txn
	}
	tag := discover("hcb_list_tags", map[string]any{"organization": org})
	card := discover("hcb_list_cards", map[string]any{})
	grant := discover("hcb_list_card_grants", map[string]any{"organization": org})
	check := discover("hcb_list_checks", map[string]any{"organization": org})
	deposit := discover("hcb_list_check_deposits", map[string]any{"organization": org})
	sponsor := discover("hcb_list_sponsors", map[string]any{"organization": org})
	invoice := discover("hcb_list_invoices", map[string]any{"organization": org})
	fmt.Printf("discovered: txn=%s rtxn=%s tag=%s card=%s grant=%s check=%s deposit=%s sponsor=%s invoice=%s\n\n",
		txn, rtxn, tag, card, grant, check, deposit, sponsor, invoice)

	tmp, _ := os.MkdirTemp("", "hcb-mcp-e2e")

	cases := []tc{
		{tool: "hcb_get_profile", args: map[string]any{"expand": "shipping_address"}, contains: `"object":"user"`},
		{tool: "hcb_available_icons", args: map[string]any{}},
		{tool: "hcb_token_info", args: map[string]any{}, contains: "scope"},
		{tool: "hcb_lookup_user", args: map[string]any{"query": profile.ID}, expectErr: true},          // no admin:read
		{tool: "hcb_lookup_user", args: map[string]any{"query": "user@example.com"}, expectErr: true}, // no admin:read
		{tool: "hcb_list_organizations", args: map[string]any{"expand": "balance_cents"}, contains: `"organization"`},
		{tool: "hcb_get_organization", args: map[string]any{"organization": org, "expand": "balance_cents,users,account_number,reporting"}, contains: `"organization"`},
		{tool: "hcb_org_balance_history", args: map[string]any{"organization": org}, contains: `"date"`},
		{tool: "hcb_org_followers", args: map[string]any{"organization": org}},
		{tool: "hcb_list_sub_organizations", args: map[string]any{"organization": org}},
		{tool: "hcb_list_transactions", args: map[string]any{"organization": org, "limit": 3}, contains: `"total_count"`},
		{tool: "hcb_list_transactions", args: map[string]any{"organization": org, "limit": 3, "type": "card_charge", "expenses": true, "start_date": "2020-01-01"}, contains: `"has_more"`},
		{tool: "hcb_get_transaction", args: map[string]any{"id": txn}, contains: `"transaction"`},
		{tool: "hcb_get_transaction", args: map[string]any{"id": txn, "organization": org}, contains: `"transaction"`},
		{tool: "hcb_memo_suggestions", args: map[string]any{"organization": org, "transaction": txn}},
		{tool: "hcb_missing_receipts", args: map[string]any{"limit": 3}, contains: `"total_count"`},
		{tool: "hcb_list_receipts", args: map[string]any{}},
		{tool: "hcb_list_receipts", args: map[string]any{"transaction": rtxn}, contains: `"receipt"`},
		{tool: "hcb_download_receipt", args: map[string]any{"transaction": rtxn, "directory": tmp}, contains: "->"},
		{tool: "hcb_download_receipt", args: map[string]any{"transaction": rtxn, "directory": tmp, "preview": true}, contains: "->"},
		{tool: "hcb_list_comments", args: map[string]any{"transaction": txn}},
		{tool: "hcb_list_tags", args: map[string]any{"organization": org}, contains: `"tag"`},
		{tool: "hcb_get_tag", args: map[string]any{"id": tag}, contains: `"tag"`},
		{tool: "hcb_list_cards", args: map[string]any{}, contains: `"stripe_card"`},
		{tool: "hcb_list_cards", args: map[string]any{"organization": org, "expand": "user"}, contains: `"stripe_card"`},
		{tool: "hcb_get_card", args: map[string]any{"id": card, "expand": "organization,total_spent_cents"}, contains: `"stripe_card"`},
		{tool: "hcb_card_transactions", args: map[string]any{"id": card, "limit": 3}, contains: `"total_count"`},
		{tool: "hcb_card_designs", args: map[string]any{}},
		{tool: "hcb_card_designs", args: map[string]any{"organization": org}},
		{tool: "hcb_list_card_grants", args: map[string]any{}},
		{tool: "hcb_list_card_grants", args: map[string]any{"organization": org, "expand": "balance_cents"}, contains: `"card_grant"`},
		{tool: "hcb_get_card_grant", args: map[string]any{"id": grant, "expand": "balance_cents,disbursements"}, contains: `"card_grant"`},
		{tool: "hcb_card_grant_transactions", args: map[string]any{"id": grant, "limit": 3}, contains: `"total_count"`},
		{tool: "hcb_list_invitations", args: map[string]any{}},
		{tool: "hcb_list_invitations", args: map[string]any{"organization": org}},
		{tool: "hcb_get_invitation", args: map[string]any{"id": "ivt_doesnotexist"}, expectErr: true},
		{tool: "hcb_list_checks", args: map[string]any{"organization": org}, contains: `"increase_check"`},
		{tool: "hcb_get_check", args: map[string]any{"id": check}, expectErr: true}, // upstream 403 bug
		{tool: "hcb_list_check_deposits", args: map[string]any{"organization": org}, contains: `"check_deposit"`},
		{tool: "hcb_get_check_deposit", args: map[string]any{"id": deposit}, contains: `"check_deposit"`},
		{tool: "hcb_list_sponsors", args: map[string]any{"organization": org}, contains: `"sponsor"`},
		{tool: "hcb_get_sponsor", args: map[string]any{"id": sponsor}, contains: `"sponsor"`},
		{tool: "hcb_list_invoices", args: map[string]any{"organization": org}, contains: `"invoice"`},
		{tool: "hcb_get_invoice", args: map[string]any{"id": invoice}, contains: `"invoice"`},
	}

	pass, fail, skip := 0, 0, 0
	for _, c := range cases {
		if missingRequiredID(c.args) {
			skip++
			fmt.Printf("SKIP  %s (no id discovered)\n", c.tool)
			continue
		}
		text, isErr, err := call(ctx, c.tool, c.args)
		status := "PASS"
		var detail string
		switch {
		case err != nil:
			status, detail = "FAIL", fmt.Sprintf("protocol error: %v", err)
		case isErr != c.expectErr:
			status, detail = "FAIL", fmt.Sprintf("IsError=%v want %v: %.200s", isErr, c.expectErr, text)
		case c.contains != "" && !strings.Contains(text, c.contains):
			status, detail = "FAIL", fmt.Sprintf("missing %q in: %.200s", c.contains, text)
		}
		if status == "PASS" {
			pass++
		} else {
			fail++
		}
		fmt.Printf("%s  %s %s\n", status, c.tool, detail)
	}

	// hcb_download_file: fetch the org icon URL live, then download it.
	if text, isErr, err := call(ctx, "hcb_get_organization", map[string]any{"organization": org}); err == nil && !isErr {
		var o struct {
			Icon string `json:"icon"`
		}
		json.Unmarshal([]byte(text), &o)
		if o.Icon != "" {
			dtext, dErr, err := call(ctx, "hcb_download_file", map[string]any{"url": o.Icon, "directory": tmp})
			if err == nil && !dErr {
				if fi, statErr := os.Stat(strings.TrimSpace(dtext)); statErr == nil && fi.Size() > 0 {
					pass++
					fmt.Printf("PASS  hcb_download_file [org icon] -> %d bytes\n", fi.Size())
				} else {
					fail++
					fmt.Printf("FAIL  hcb_download_file: saved file missing (%v)\n", statErr)
				}
			} else {
				fail++
				fmt.Printf("FAIL  hcb_download_file: %v %s\n", err, dtext)
			}
		}
	}

	fmt.Printf("\nRESULT: %d passed, %d failed, %d skipped\n", pass, fail, skip)
	if fail > 0 {
		os.Exit(1)
	}
}

// missingRequiredID reports whether any id-carrying argument is empty
// (discovery found nothing for it).
func missingRequiredID(args map[string]any) bool {
	for k, v := range args {
		if s, ok := v.(string); ok && s == "" && k != "expand" {
			return true
		}
	}
	return false
}

func snippet(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if t, ok := res.Content[0].(*mcp.TextContent); ok {
		return t.Text
	}
	return fmt.Sprintf("%v", res.Content[0])
}
