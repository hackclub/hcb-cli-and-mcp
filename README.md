# hcb-cli-and-mcp

This is a little repo that exposes the read-only API calls from HCB's v4 API as an MCP server and a CLI app. It's meant to help agents answer read-only questions about HCB, like: "what is Stardance spending its money on?".

## Setup / Install

### MCP

Use the URL `https://hcb-mcp.k.hackclub.dev/mcp` and add it as a connector to Claude / ChatGPT / etc. That's it! It'll ask you to log in with your HCB account.

### CLI

With [Homebrew](https://brew.sh) (macOS and Linux):

```sh
brew tap hackclub/hcb-cli-and-mcp https://github.com/hackclub/hcb-cli-and-mcp
brew install hcb
```

This installs both binaries: the `hcb` CLI and the `hcb-mcp` server. Then:

```sh
hcb login          # authorize in your browser — no client id/secret needed
hcb orgs           # list your organizations
```

`hcb login` brokers through the hosted server's OAuth bridge by default; pass `--client-id`/`--client-secret` to use your own HCB OAuth app directly.

Admins can request HCB-wide read access without write scopes:

```sh
hcb login --admin
```

This asks for `restricted read admin:read organizations:read ledgers:read receipts:read user_lookup event_followers`. Your HCB account still needs an auditor/admin role, and the HCB OAuth app used for login must be registered for those scopes.

## License

MIT
