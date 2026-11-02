---
name: smart-model-router
description: Use when the user wants to install, configure, diagnose, update, or uninstall Smart Model Router (SMR) for Codex or Claude Code.
---

# Smart Model Router

SMR is a local daemon that classifies each coding request into one of four tiers and forwards it to the model configured for that tier. It is not a model provider and does not call models on the user's behalf.

## Setup flow

1. Check whether `smr` is available:

   ```bash
   smr version
   ```

2. If it is missing, ask the user to confirm before running the installer:
   - macOS/Linux: `curl -fsSL https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.sh | sh`
   - Windows PowerShell: `irm https://raw.githubusercontent.com/acmeai-vn/SMR/main/install.ps1 | iex`

   Do not pipe a remote script into a shell without showing the URL and getting confirmation.

3. Run:

   ```bash
   smr setup
   ```

4. The setup UI opens on `127.0.0.1:8788` with a one-time token. The user must enter the ACME AI API key in the UI, not in chat.

5. Show the detected host configuration diff. Do not approve changes to Codex or Claude Code on the user's behalf. The user must confirm each host.

6. After confirmation, restart the affected host and verify:

   ```bash
   smr status
   smr doctor
   ```

## Rules

- Never ask the user to paste an API key, provider key, OAuth token, or session token into the conversation.
- Do not edit `~/.codex/config.toml` or `~/.claude/settings.json` manually when `smr setup` can do it with a diff and backup.
- Do not set `model_provider = "smr"` as the Codex default unless the user explicitly agrees.
- Claude Code requires an Anthropic-compatible API key/token. Claude Pro OAuth is unsupported.
- Codex requires a provider API key. A ChatGPT Plus session is unsupported.
- A 402 response is a hard stop. Do not work around it or switch to a more expensive model.
- Do not run `smr uninstall --purge` without explicit confirmation; without `--restore`, host configuration is not reverted.
- If the user is on a host that cannot reach the network, stop and report it; do not fabricate a successful setup.

## Troubleshooting

| Symptom | Check |
|---|---|
| `smr: command not found` | Installer completed; reopen the terminal or add the install directory to `PATH` |
| `401` from classifier | API key invalid, revoked, or belongs to another environment |
| `402` from classifier | Balance too low; open `https://llm.acmeai.vn/topup` |
| Codex does not use SMR | Confirm `model_provider = "smr"` was approved, then restart Codex |
| Claude Code returns 401 | Profile needs a valid Anthropic-compatible API key/token |
| Port already in use | Change `listen` or `ui_listen` in `config.yaml`, then `smr restart` |
| Host config was changed unexpectedly | `smr uninstall --restore` restores the latest backup |

Use `smr logs --tail 100` for diagnostics. Never paste raw prompts or API keys into an issue.
