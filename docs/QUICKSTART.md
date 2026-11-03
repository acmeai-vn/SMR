# Use SMR with Codex or Claude Code

SMR routes coding-agent requests to models you choose for four difficulty tiers. Use it when you pay for model API usage and want to route routine work to a cheaper model while keeping a stronger model available for harder tasks.

[English README](../README.md) · [Tiếng Việt](../README.vi.md) · [Cost and privacy](COST-AND-PRIVACY.md)

## Before you install

- Use Windows 11, macOS 14+ or Ubuntu 22.04+, on a 64-bit machine.
- Have a provider API key: a Responses-compatible provider for Codex, or an Anthropic-compatible provider for Claude Code. ChatGPT Plus and Claude Pro OAuth sessions cannot be routed through SMR.
- Sign in at [llm.acmeai.vn](https://llm.acmeai.vn/), top up and create an ACME AI API key. Classification uses this prepaid balance; provider tokens are billed separately.
- Enter secrets only in the local setup UI or configuration file. Keep them out of agent chat and GitHub issues.

Choose either the plugin for your host or direct installation below. Both install the same local daemon. SMR shows the host configuration changes for you to apply yourself.

## Codex

Add the marketplace and plugin in a terminal with Codex installed:

```text
codex plugin marketplace add https://github.com/acmeai-vn/SMR
codex plugin add smart-model-router@acmeai-smr
```

Ask Codex: “Set up Smart Model Router.” Follow the setup workflow, review the installer URL and complete the local setup page.

On the **Codex** tab, assign a provider base URL, API key and model to each of `easy`, `lower_medium`, `upper_medium` and `hard`. Use model IDs your provider currently supports.

Copy the provider block shown by setup into `~/.codex/config.toml`. If you choose SMR as the default, put `model_provider = "smr"` at the top level, before any `[table]`. Merge with existing settings so there is only one default provider key. Restart Codex.

The local Responses base URL is `http://127.0.0.1:8787/v1`. See [the full configuration example](../config.example.yaml) and [host configuration](../README.md#host-configuration).

## Claude Code

Run these commands inside Claude Code:

```text
/plugin marketplace add https://github.com/acmeai-vn/SMR
/plugin install smart-model-router@acmeai-smr
```

Invoke the SMR setup skill. On the **Claude Code** tab of the local setup page, configure an Anthropic-compatible base URL, provider key and model for each of the four tiers.

Merge the entries shown by setup into the existing `env` object in `~/.claude/settings.json`:

```json
{
  "ANTHROPIC_BASE_URL": "http://127.0.0.1:8787",
  "ANTHROPIC_AUTH_TOKEN": "smr-local-placeholder"
}
```

This is the content to merge into `env`, not a replacement for the entire settings file. The placeholder is not a provider credential; SMR uses the provider key from your Claude profile. Review any existing authentication settings and restart Claude Code after the edit.

## Direct installation

You can inspect the [shell installer](../install.sh) or [PowerShell installer](../install.ps1) before running it. No administrator rights are needed for normal installation.

macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.ps1 | iex
```

The installer downloads the release for your OS and architecture, checks its SHA-256, registers a per-user background service and opens setup. If the setup page is closed, run `smr setup` again. Apply the host configuration blocks it shows, then restart your host.

For version pinning, manual checksum verification and platform troubleshooting, see [installation details](../README.md#install-from-a-url).

## Verify your first request

1. Run `smr status` and `smr doctor`.
2. Open `smr setup` and go to the final check step.
3. Give your configured agent a small task in a test project, such as explaining a function.
4. Check that the request counter increases and inspect the last tier, charge and cache result. A normal classified request also appears in the [ACME AI dashboard](https://llm.acmeai.vn/).

For HTTP clients, inspect `X-SMR-Tier`, `X-SMR-Cache`, `X-SMR-Charged-VND`, `X-SMR-Balance-VND` and `X-SMR-Classifier`. A `fallback` classifier result means that request used the configured fallback tier; it is not evidence of a successful classification.

SMR does not guarantee a particular saving or output quality. Start with a small task, review the result and [measure total cost](COST-AND-PRIVACY.md#measure-total-cost) before expanding use.

## If something fails

| Symptom | Next step |
|---|---|
| 402 | Check classification balance and [top up](https://llm.acmeai.vn/topup). |
| Provider 401 | Check the key in the matching SMR profile; a subscription login is not a provider API key. |
| No requests in setup | Check the host base URL, restart the host and run `smr doctor`. |
| Port already in use | See [troubleshooting](../README.md#troubleshooting) before changing ports. |

Run `smr logs --tail 100` for diagnostics. [Report an issue](https://github.com/acmeai-vn/SMR/issues/new) with your OS, host, SMR version and redacted error. Never include API keys, setup tokens or private prompts.

To stop using SMR, revert the host configuration entries you added. `smr uninstall` removes the background service; see [uninstall details](../README.md#uninstall).
