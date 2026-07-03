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
hcb login --admin  # request admin read visibility, if your HCB account supports it
hcb orgs           # list your organizations
```

`hcb login` brokers through the hosted server's OAuth bridge by default; pass `--client-id`/`--client-secret` to use your own HCB OAuth app directly.

Admin login requests only `read admin:read`. It does not request `write`, `admin:write`, `pii`, or `restricted`. The boundary is this app's read-only CLI/MCP surface and OAuth scope allowlist, so the HCB OAuth app used by the bridge must be registered for `admin:read`.

### Hosted server credentials

If `hcb-mcp --http` uses `MCP_AUTH_TOKEN`, requests with that shared secret use server-owned HCB credentials. In that mode the server refuses to start unless `HCB_CREDENTIALS_KEY` is set and the credentials file is already an encrypted AES-256-GCM envelope. If the file is missing, `HCB_CREDENTIALS_JSON` must also be an encrypted envelope; plaintext server credential seeds are rejected.

Generate a key with:

```sh
openssl rand -base64 32
```

## License

MIT
