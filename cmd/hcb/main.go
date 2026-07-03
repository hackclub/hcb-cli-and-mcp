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

	"github.com/hackclub/hcb-cli-and-mcp/internal/hcbapi"
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
			if cmd.Name() != "upgrade" { // the updater must never respawn itself
				maybeAutoUpdate()
			}
			switch cmd.Name() {
			case "login", "version", "upgrade": // work without existing creds
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
		versionCmd(), upgradeCmd(),
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

// defaultAuthServer is the hosted HCB MCP server; its OAuth bridge brokers
// browser logins so the CLI needs no client id or secret of its own.
const defaultAuthServer = "https://hcb-mcp.k.hackclub.dev"

func loginCmd() *cobra.Command {
	var clientID, clientSecret, baseURL, scope, authServer string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authorize via browser (authorization-code flow with localhost callback)",
		Long: "Runs the OAuth authorization-code flow in your browser and stores tokens\n" +
			"in the credentials file. With no flags the hosted HCB MCP server brokers\n" +
			"the flow — no client id or secret needed. Pass --client-id (and usually\n" +
			"--client-secret) to use your own HCB OAuth app directly; that app must\n" +
			"register http://localhost:8910/callback as a redirect URI.",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := credsPath
			if path == "" {
				var err error
				path, err = hcbapi.DefaultCredentialsPath()
				if err != nil {
					return err
				}
			}
			// Reuse a previous direct-flow client config when flags are
			// omitted. Bridge-flow credentials hold no local secret and
			// don't pin the next login to a client.
			if existing, err := hcbapi.LoadCredentials(path); err == nil {
				if clientID == "" && existing.ClientSecret != "" {
					clientID = existing.ClientID
					if clientSecret == "" {
						clientSecret = existing.ClientSecret
					}
				}
				if baseURL == "" {
					baseURL = existing.BaseURL
				}
			}
			if baseURL == "" {
				baseURL = "https://hcb.hackclub.com"
			}
			cfg := hcbapi.LoginConfig{
				BaseURL: baseURL, ClientID: clientID, ClientSecret: clientSecret, Scope: scope,
			}
			if clientID == "" {
				// Zero-config path: the hosted bridge runs the flow, holds
				// the client secret, and accepts any localhost port.
				cfg.AuthServer = authServer
				cfg.ClientID = "hcb-cli"
			}
			loginCtx, cancel := context.WithTimeout(ctx(), 10*time.Minute)
			defer cancel()
			creds, err := hcbapi.Login(loginCtx, cfg, os.Stderr)
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
	cmd.Flags().StringVar(&clientID, "client-id", "", "OAuth application UID (omit to use the hosted auth server)")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "OAuth application secret (confidential apps)")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "HCB base URL (default https://hcb.hackclub.com)")
	cmd.Flags().StringVar(&scope, "scope", "read", "OAuth scopes to request")
	cmd.Flags().StringVar(&authServer, "auth-server", defaultAuthServer, "hosted OAuth bridge used when no --client-id is given")
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
