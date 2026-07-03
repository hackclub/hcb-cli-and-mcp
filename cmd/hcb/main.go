// Command hcb is a read-only CLI for the HCB v4 API.
// All output is JSON (pretty-printed) so it composes with jq.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/hackclub/hcb-mcp/internal/hcbapi"
	"github.com/spf13/cobra"
)

var (
	credsPath string
	client    *hcbapi.Client
)

func main() {
	root := &cobra.Command{
		Use:           "hcb",
		Short:         "Read-only CLI for the HCB v4 API",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "login" { // login must work without existing creds
				return nil
			}
			var err error
			if credsPath == "" {
				credsPath, err = hcbapi.DefaultCredentialsPath()
				if err != nil {
					return err
				}
			}
			client, err = hcbapi.NewClient(credsPath)
			return err
		},
	}
	root.PersistentFlags().StringVar(&credsPath, "creds", "", "path to credentials.json (default ~/.config/hcb/credentials.json)")

	root.AddCommand(
		loginCmd(), authCmd(),
		userCmd(), iconsCmd(), lookupCmd(),
		orgsCmd(), orgCmd(), balanceHistoryCmd(), followersCmd(), subOrgsCmd(),
		transactionsCmd(), transactionCmd(), memoSuggestionsCmd(), missingReceiptsCmd(),
		receiptsCmd(), receiptDownloadCmd(), downloadCmd(),
		commentsCmd(), tagsCmd(), tagCmd(),
		cardsCmd(), cardCmd(), cardTransactionsCmd(), cardDesignsCmd(),
		grantsCmd(), grantCmd(), grantTransactionsCmd(),
		invitationsCmd(), invitationCmd(),
		checksCmd(), checkCmd(), checkDepositsCmd(), checkDepositCmd(),
		sponsorsCmd(), sponsorCmd(), invoicesCmd(), invoiceCmd(),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func ctx() context.Context { return context.Background() }

// printJSON pretty-prints any raw JSON payload.
func printJSON(raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// not JSON? print as-is
		fmt.Println(string(raw))
		return nil
	}
	fmt.Println(buf.String())
	return nil
}

func printPage(p *hcbapi.Page) error {
	env, err := json.Marshal(map[string]any{
		"total_count": p.TotalCount,
		"has_more":    p.HasMore,
		"data":        json.RawMessage(p.Data),
	})
	if err != nil {
		return err
	}
	return printJSON(env)
}

// run wraps a client call returning raw JSON.
func run(fn func() (json.RawMessage, error)) error {
	raw, err := fn()
	if err != nil {
		return err
	}
	return printJSON(raw)
}

func runPage(fn func() (*hcbapi.Page, error)) error {
	p, err := fn()
	if err != nil {
		return err
	}
	return printPage(p)
}

// --- auth ---

func loginCmd() *cobra.Command {
	var clientID, clientSecret, baseURL, scope string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authorize via browser (authorization-code flow with localhost callback)",
		Long: "Runs the OAuth authorization-code flow: starts a localhost:8910 listener,\n" +
			"opens the HCB authorize page, and stores tokens in the credentials file.\n" +
			"Defaults for client id/secret are reused from an existing credentials file.",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := credsPath
			if path == "" {
				var err error
				path, err = hcbapi.DefaultCredentialsPath()
				if err != nil {
					return err
				}
			}
			// reuse existing client config when flags are omitted
			if existing, err := hcbapi.LoadCredentials(path); err == nil {
				if clientID == "" {
					clientID = existing.ClientID
				}
				if clientSecret == "" {
					clientSecret = existing.ClientSecret
				}
				if baseURL == "" {
					baseURL = existing.BaseURL
				}
			}
			if baseURL == "" {
				baseURL = "https://hcb.hackclub.com"
			}
			if clientID == "" {
				return fmt.Errorf("--client-id is required (no existing credentials to reuse)")
			}
			loginCtx, cancel := context.WithTimeout(ctx(), 10*time.Minute)
			defer cancel()
			creds, err := hcbapi.Login(loginCtx, hcbapi.LoginConfig{
				BaseURL: baseURL, ClientID: clientID, ClientSecret: clientSecret, Scope: scope,
			}, os.Stderr)
			if err != nil {
				return err
			}
			if err := creds.Save(path); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Logged in. Credentials saved to %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth application UID")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth application secret (confidential apps)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "HCB base URL (default https://hcb.hackclub.com)")
	cmd.Flags().StringVar(&scope, "scope", "read", "OAuth scopes to request")
	return cmd
}

func authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Token status and refresh"}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Inspect the current token (GET /oauth/token/info)",
			RunE: func(cmd *cobra.Command, args []string) error {
				return run(func() (json.RawMessage, error) { return client.TokenInfo(ctx()) })
			},
		},
		&cobra.Command{
			Use:   "refresh",
			Short: "Force a token refresh (rotates the refresh token)",
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := client.Refresh(ctx()); err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, "Token refreshed.")
				return nil
			},
		},
	)
	return cmd
}
