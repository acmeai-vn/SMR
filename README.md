# SMR — Smart Model Router

SMR is a local routing daemon for Codex and Claude Code. It classifies each request into one of four difficulty tiers, then sends the request to the model you configured for that tier. Your provider API keys stay on your machine.

ACME AI sells the classification decision through `api.acmeai.vn`. SMR does not proxy model traffic through ACME AI and does not use your provider account.

## What it does

    Codex / Claude Code
            |
            v
    SMR on 127.0.0.1
            |  one representative text
            v
    api.acmeai.vn  ->  Jev classifier
            |
            |  easy | lower_medium | upper_medium | hard
            v
    SMR forwards the original request to the model configured for that tier

- Four tiers: `easy`, `lower_medium`, `upper_medium`, `hard`.
- Separate native profiles for Codex Responses and Claude Messages.
- No format translation between OpenAI and Anthropic.
- No local Jev key. The classifier is called with your ACME AI API key.
- Server-side cache: a cache hit costs 10% of the original classification charge, minimum 1 VND.

## Requirements

| Host | Required | Not supported |
|---|---|---|
| Codex | Provider API key configured in Codex | ChatGPT Plus session |
| Claude Code | Anthropic-compatible API key or token | Claude Pro OAuth session |
| OS | Windows 11, macOS 14+, Ubuntu 22.04+ | 32-bit systems |

Claude Code must be able to reach an Anthropic-compatible API with an API key. A Claude Pro login is not an API key and cannot be routed through SMR.

## Install

| Path | Entry point | Use it when |
|---|---|---|
| Plugin marketplace URL | `codex plugin marketplace add https://github.com/acmeai-vn/SMR` or `/plugin marketplace add https://github.com/acmeai-vn/SMR` | You use Codex or Claude Code and want the agent to walk through setup |
| Direct URL installer | `curl -fsSL https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.sh \| sh` or `irm https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.ps1 \| iex` | CI, scripted setup, or a host without plugin support |

Both paths install the same daemon, use the same configuration file, and open the same setup flow. The plugin does not contain routing logic.

## Install from a URL

It installs the `smr` binary, registers a per-user background service, and opens the local setup page. It does not edit Codex or Claude Code configuration until you review and confirm the changes.

### macOS and Linux

    curl -fsSL https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.sh | sh

Install a specific version:

    curl -fsSL https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.sh | sh -s -- --version v0.1.0

Install without opening the setup page:

    curl -fsSL https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.sh | sh -s -- --no-setup

### Windows

Run PowerShell as your normal user. Administrator rights are required only if your organization policy blocks per-user scheduled tasks.

    irm https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.ps1 | iex

Install a specific version:

    $env:SMR_VERSION = "v0.1.0"
    irm https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.ps1 | iex

Install without opening the setup page:

    $env:SMR_NO_SETUP = "1"
    irm https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.ps1 | iex

### Installer behavior

1. Detects OS and CPU architecture.
2. Downloads `smr-release.json` and the platform archive from GitHub Releases.
3. Verifies the archive SHA-256 before extracting or replacing anything.
4. Installs `smr` under your user:
   - Windows: `%LOCALAPPDATA%\AcmeAI\SMR\smr.exe`
   - macOS: `~/Library/Application Support/AcmeAI/SMR/smr`
   - Linux: `~/.local/share/acmeai/smr/smr`
5. On macOS/Linux, creates `~/.local/bin/smr` as a symlink. It never overwrites a regular file with that name.
6. Registers a per-user background service:
   - Windows: Task Scheduler at logon.
   - macOS: LaunchAgent `vn.acmeai.smr`.
   - Linux: `systemd --user` unit `smr.service`.
7. Starts SMR on `127.0.0.1:8787` and its local UI on `127.0.0.1:8788`.
8. Waits for `GET /health` before reporting success.
9. Opens `smr setup` with a one-time token unless `--no-setup` or `--no-service` is set.

The installer never writes provider keys into logs and never sends them to ACME AI. Running it again upgrades the binary and restarts the service without deleting your configuration or backups.

If `~/.local/bin` is not on your `PATH`, add it or invoke the absolute path printed by the installer.

### Manual verification

Every release publishes the archive, `smr-vX.Y.Z-checksums.txt`, and `smr-release.json`. To verify a manual download:

    sha256sum smr-v0.1.0-linux-amd64.tar.gz
    # compare with smr-v0.1.0-checksums.txt

On macOS use `shasum -a 256` instead. The phase 1 release manifest is not cryptographically signed; integrity relies on HTTPS plus the published SHA-256 values. See `docs/RELEASE.md` for the publishing flow.

## Install as an agent plugin

SMR is distributed as a marketplace plugin for Codex and Claude Code. Add the marketplace by URL; you do not need to clone the repository or know a local path. The plugin is a bootstrap and launcher: it registers the SMR setup workflow, and that workflow installs or updates the local daemon after showing the installer URL and getting confirmation. Routing still happens in the local daemon, not inside the agent process.

Installing the plugin by itself does not edit Codex or Claude Code configuration. The setup skill handles detection, diff, confirmation, backup, and restore.

### Codex

    codex plugin marketplace add acmeai-vn/SMR
    codex plugin add smart-model-router@acmeai-smr

Or use the full Git URL:

    codex plugin marketplace add https://github.com/acmeai-vn/SMR
    codex plugin add smart-model-router@acmeai-smr

Then ask Codex to set up Smart Model Router and follow the skill instructions.

### Claude Code

Inside a Claude Code session:

    /plugin marketplace add acmeai-vn/SMR
    /plugin install smart-model-router@acmeai-smr

Or use the full Git URL:

    /plugin marketplace add https://github.com/acmeai-vn/SMR
    /plugin install smart-model-router@acmeai-smr

Then invoke the SMR skill and follow its setup flow. Restart Claude Code after SMR updates the host configuration.

### What the plugin does not do

- It does not contain a second router.
- It does not modify `~/.codex/config.toml` or `~/.claude/settings.json` without confirmation.
- It does not ask you to paste an API key, provider key, OAuth token, or session token into chat.
- It does not set SMR as the default provider silently.

## First run

    smr setup

The setup page asks for:

1. Your ACME AI API key (`acme_sk_...`).
2. Four models for the Codex profile.
3. Four models for the Claude Code profile.
4. Whether to import any existing Codex/Claude Code provider configuration.

The API key is stored in the SMR configuration file with user-only permissions. The full key is shown only once on `llm.acmeai.vn`; SMR cannot recover it later.

The setup token is written to the data directory (`ui-token`) and is valid only for this process. Logs and host-config backups live in the same directory. On Windows and macOS the directory is under the per-user Application/Local data folder; on Linux it follows `XDG_DATA_HOME` and defaults to `~/.local/share/acmeai/smr`.

Useful commands:

    smr version
    smr status
    smr doctor
    smr restart
    smr update
    smr logs --tail 100
    smr uninstall --restore

`smr serve` is the foreground process used by the background service. You normally do not run it directly.

## Host configuration

SMR changes only the blocks it owns. It backs up each file before writing and shows a diff first.

### Codex

Adds or updates:

    [model_providers.smr]
    name = "Smart Model Router"
    base_url = "http://127.0.0.1:8787/v1"
    wire_api = "responses"

Setting `model_provider = "smr"` as the default is a separate confirmation. SMR does not make itself the default silently.

### Claude Code

Adds or updates the following environment entries:

    {
      "env": {
        "ANTHROPIC_BASE_URL": "http://127.0.0.1:8787",
        "ANTHROPIC_AUTH_TOKEN": "<local placeholder generated by SMR>"
      }
    }

The placeholder is not a secret. SMR replaces it with the Claude profile token when forwarding the request.

If you choose not to modify a host, SMR still runs and the other host can use it.

## Configuration

Default locations:

| OS | Config |
|---|---|
| Windows | `%LOCALAPPDATA%\AcmeAI\SMR\config.yaml` |
| macOS | `~/Library/Application Support/AcmeAI/SMR/config.yaml` |
| Linux | `${XDG_DATA_HOME:-~/.local/share}/acmeai/smr/config.yaml` |

`ui-token`, `smr.log` and `backups/` sit beside the config file. The port and token paths can be overridden with `SMR_CONFIG` and `SMR_DATA_DIR`.

A complete, commented example is in `config.example.yaml`. Minimal shape:

    listen: "127.0.0.1:8787"
    ui_listen: "127.0.0.1:8788"

    classifier:
      base_url: "https://api.acmeai.vn"
      api_key: ""                 # or ACMEAI_API_KEY
      timeout_ms: 15000
      max_chars: 12000
      retry: 1
      on_transient_error: fallback
      fallback_tier: upper_medium

    profiles:
      codex:
        api: responses
        auth: bearer
        tiers:
          easy:         { base_url: "https://provider.example/v1", api_key: "...", model: "..." }
          lower_medium: { base_url: "https://provider.example/v1", api_key: "...", model: "..." }
          upper_medium: { base_url: "https://provider.example/v1", api_key: "...", model: "..." }
          hard:         { base_url: "https://provider.example/v1", api_key: "...", model: "..." }

      claude:
        api: messages
        auth: x-api-key
        anthropic_version: "2023-06-01"
        tiers:
          easy:         { base_url: "https://api.anthropic.com", api_key: "...", model: "..." }
          lower_medium: { base_url: "https://api.anthropic.com", api_key: "...", model: "..." }
          upper_medium: { base_url: "https://api.anthropic.com", api_key: "...", model: "..." }
          hard:         { base_url: "https://api.anthropic.com", api_key: "...", model: "..." }

Configuration rules:

- SMR binds only to loopback. It refuses to start on a public interface.
- Config files are readable only by the current user. On Windows SMR resets the ACL with `icacls` and grants the current user because `chmod` does not change Windows ACLs.
- `ACMEAI_API_KEY` overrides `classifier.api_key`.
- `max_chars` must not exceed the server limit returned by `GET /v1/account`.
- `count_tokens` is served by the Claude `easy` tier and does not call the classifier.

Each response includes `X-SMR-Tier`, `X-SMR-Cache`, `X-SMR-Charged-VND`, `X-SMR-Balance-VND` and `X-SMR-Classifier` so a client can verify what happened without exposing prompts.

## Billing behavior

- You are charged per classification, in whole VND.
- A cache hit costs 10% of the original charge, minimum 1 VND.
- The server rounds up; the minimum charge is 1 VND.
- A cache entry lives for 5 minutes.
- If your balance is insufficient, SMR returns 402 and does not call a model. It does not silently fall back to a more expensive model.
- There is no refund policy. A topped-up balance does not expire and is used until it runs out.
- A transfer that arrives after the 30-minute QR window is still credited. Overpayments stay in the balance; underpayments credit the amount received; only a transfer whose order code cannot be matched goes to reconciliation.

Top-up plans: 20,000 / 50,000 / 100,000 VND via TP Bank QR. The QR is shown for 30 minutes; the order code remains usable after that. The 50,000 VND plan is highlighted by default.

## Update and rollback

    smr update
    smr update --check
    smr update --version v0.1.0

`smr update` reads the release manifest, verifies the new archive, and asks before replacing the running binary. The previous binary is kept for rollback. A binary rollback does not touch configuration or host backups.

To reinstall a specific version with the URL installer:

    curl -fsSL https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.sh | sh -s -- --version v0.1.0 --no-setup

## Troubleshooting

    smr doctor
    smr status
    smr logs --tail 100

Common cases:

- **Claude Code returns 401:** the Claude profile needs an Anthropic-compatible API key/token. Claude Pro OAuth is not supported.
- **Codex cannot find the provider:** restart Codex after `smr setup` confirms the change.
- **402 from SMR:** top up at `https://llm.acmeai.vn/topup`. SMR will not call a model without balance.
- **Port already in use:** change `listen` and `ui_listen`, save, then preview and re-apply the host config so its base URL matches the new port. Run `smr restart` afterwards.
- **Installer cannot find a release:** check that a tag and its release assets exist on GitHub Releases, then retry without `--version`.
- **Installer checksum mismatch:** do not run the archive. Delete it and download again; report the release tag and asset name privately.
- **macOS Gatekeeper blocks the binary:** phase 1 releases are not signed or notarized. Verify the checksum and follow the published install instructions. Signing is out of scope for phase 1.
- **Windows scheduled task blocked by policy:** ask your administrator, or run `smr serve` manually from a terminal.

## Uninstall

    smr uninstall --restore

This removes the background service and restores host configuration from the latest backup. Add `--purge` to remove SMR configuration and cached state as well.

## Security and privacy

- SMR sends only one representative text to `api.acmeai.vn`.
- The server stores a hash, tier, token count, price, and timestamps; it does not store the raw text.
- API keys and provider credentials stay on the local machine.
- SMR never logs raw prompts, API keys, or OAuth tokens.
- The local UI binds only to loopback and requires a one-time token.
- On Windows, SMR configuration and token files receive an explicit current-user-only ACL. On macOS and Linux they use `0600`/`0700` permissions.
- Source code is available under Apache-2.0.

## Development

    go test ./...
    go vet ./...
    go build ./cmd/smr

Release packaging is documented in `docs/RELEASE.md`.

## License

Apache-2.0. See `LICENSE`.
