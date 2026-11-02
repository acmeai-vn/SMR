# Release guide — SMR

This document is the source of truth for publishing SMR binaries, the release manifest, and the URL installers.

## Versioning

- Tags use semantic versioning: `vMAJOR.MINOR.PATCH`.
- The first public tag is `v0.1.0`.
- The installer and marketplace entries use the same version.
- Never reuse or move a published tag. Installations and rollback depend on the asset staying at the same URL.

## Prerequisites

On the release machine or CI runner:

- Go 1.24 or newer (the workflow reads the version from `go.mod`).
- `tar`, `zip`, and `sha256sum` (or `shasum`).
- GitHub CLI authenticated with permission to create releases.
- A clean checkout at the tag commit.
- Codex and Claude Code available for marketplace validation, or CI jobs that run their validators.

## Build and package

From the repository root:

    go test ./...
    go vet ./...
    scripts/package-release.sh v0.1.0 dist

The packager builds six targets with `CGO_ENABLED=0`:

- linux/amd64
- linux/arm64
- darwin/amd64
- darwin/arm64
- windows/amd64
- windows/arm64

It writes these files to `dist/`:

- `smr-v0.1.0-linux-amd64.tar.gz`
- `smr-v0.1.0-linux-arm64.tar.gz`
- `smr-v0.1.0-darwin-amd64.tar.gz`
- `smr-v0.1.0-darwin-arm64.tar.gz`
- `smr-v0.1.0-windows-amd64.zip`
- `smr-v0.1.0-windows-arm64.zip`
- `smr-v0.1.0-checksums.txt`
- `smr-release.json`

Each Unix archive contains `smr` and `LICENSE`. Each Windows archive contains `smr.exe` and `LICENSE`.

## Manifest contract

`smr-release.json` is uploaded with every release and downloaded by both URL installers and `smr update`.

    {
      "schema": 1,
      "version": "v0.1.0",
      "released_at": "2026-11-02T00:00:00Z",
      "assets": {
        "linux-amd64": {
          "file": "smr-v0.1.0-linux-amd64.tar.gz",
          "url": "https://github.com/acmeai-vn/SMR/releases/download/v0.1.0/smr-v0.1.0-linux-amd64.tar.gz",
          "sha256": "64-hex",
          "size": 12345678
        }
      }
    }

Rules:

- `schema` is the manifest format version, not the SMR version.
- `version` must match the tag and every asset filename.
- `sha256` is lowercase hex.
- `size` is bytes.
- The manifest must describe all six platforms.
- The installer must refuse an unknown schema, a version mismatch, a missing asset, or a hash mismatch.
- Do not publish a manifest that references an asset not uploaded in the same release.

Phase 1 uses HTTPS plus SHA-256. The manifest is not cryptographically signed. Do not describe releases as signed until a real signing key and verification path exist.

## Publish with GitHub Actions

Create a tag on the release commit and push it:

    git tag -a v0.1.0 -m "SMR v0.1.0"
    git push origin v0.1.0

The workflow in `.github/workflows/release.yml` runs tests, packages all assets, and creates the GitHub Release. It requires the repository secret or built-in `GITHUB_TOKEN` with `contents: write`.

Pull requests and pushes to `main` also run `.github/workflows/ci.yml`, which checks formatting, vet, tests, and the `smr` build.

If creating the release manually:

    gh release create v0.1.0 dist/* --title "SMR v0.1.0" --generate-notes

Upload all files in one operation. Do not edit an asset after publication; publish a new patch release instead.

## Verify a release before announcing

1. Check the manifest and checksum files exist in the release.
2. Download the Linux amd64 asset twice and compare SHA-256 with the manifest.
3. Run the URL installer in a throwaway home directory with service registration disabled:

       SMR_BASE_URL=file:///path/to/dist HOME=/tmp/smr-smoke SMR_SKIP_SERVICE=1 SMR_SKIP_PATH=1 SMR_NO_SETUP=1 sh install.sh

4. Run the same flow with a deliberately bad checksum; it must stop before writing a binary.
5. Test the Windows installer with `-NoService -NoSetup -NoPath` in a disposable Windows VM.
6. Test install, upgrade, rollback, and `smr uninstall --restore` on Windows 11, macOS 14/15, and Ubuntu 22.04/24.04.
7. Confirm `smr serve` is the command used by the Linux unit, macOS LaunchAgent, and Windows scheduled task.

## Validate marketplace packages

Codex:

    codex plugin marketplace add ./path/to/SMR
    codex plugin list --available --json
    codex plugin add smart-model-router@acmeai-smr

Claude Code:

    claude plugin validate .claude-plugin/marketplace.json
    claude plugin marketplace add ./path/to/SMR
    claude plugin install smart-model-router@acmeai-smr

Run marketplace tests in a temporary `CODEX_HOME` or project scope. Remove the test marketplace and plugin afterwards. A successful JSON parse is not enough; the host must list the plugin and load the `smart-model-router` skill.

## Signing

Phase 1 does not sign the release manifest or notarize macOS binaries.
Phase 1 also does not purchase or apply a Windows Authenticode certificate.

- macOS notarization requires Apple Developer Program membership, Developer ID Application certificate, hardened runtime, `codesign`, `notarytool`, and a macOS runner.
- Windows SmartScreen requires an Authenticode certificate and a signing step.
- When signing is added, update the installer to verify the signature before trusting the manifest, and update this document before changing README claims.

## Rollback

- Roll back a bad release by publishing the previous manifest and assets under a new patch tag, or by having users run the installer with `--version`.
- Keep the previous binary for `smr update` rollback.
- Never delete release assets that users may be rolling back to.
- Host configuration backups are separate from binary rollback and are restored with `smr uninstall --restore`.

## Release checklist

- [ ] Version and tag match.
- [ ] Tests and vet pass on the tag commit.
- [ ] Six archives, checksums, and manifest exist.
- [ ] Manifest has all six platforms and correct hashes.
- [ ] URL installer succeeds on Linux, macOS, and Windows smoke environments.
- [ ] Bad hash is rejected.
- [ ] Marketplace plugin validates and installs in both hosts.
- [ ] README commands match the published assets and CLI.
- [ ] No secrets, local paths, or generated test data are committed.
- [ ] Release notes state the supported OS, the classification-only scope, and the unsupported Claude Pro/ChatGPT Plus sessions.
