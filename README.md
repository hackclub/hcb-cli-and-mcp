# hcb-cli-and-mcp

This is a little repo that exposes the read-only API calls from HCB's v4 API as an MCP server and a CLI app. It's meant to help agents answer read-only questions about HCB, like: "what is Stardance spending its money on?".

## Setup / Install

### MCP

Use the URL `https://hcb-mcp.k.hackclub.dev/mcp` and add it as a connector to Claude / etc. Right now it is setup for Claude, it still needs the redirect_uri set for ChatGPT / Codex.

### CLI

With [Homebrew](https://brew.sh) (macOS and Linux):

```sh
brew tap hackclub/hcb-cli-and-mcp https://github.com/hackclub/hcb-cli-and-mcp
brew install hcb
```

This installs both binaries: the `hcb` CLI and the `hcb-mcp` server. Then:

```sh
hcb login          # authorize in your browser
hcb orgs           # list your organizations
```

## License

MIT
