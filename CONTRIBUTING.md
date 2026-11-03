# Contributing to SMR

Every change to `main` goes through a pull request. Direct pushes, force pushes,
and branch deletion are blocked by branch protection, including for
administrators.

## Workflow

1. Create a branch off `main`.

   ```sh
   git switch main
   git pull --ff-only
   git switch -c fix/short-description
   ```

2. Make the change, then run the same checks CI runs.

   ```sh
   gofmt -l cmd internal    # must print nothing
   go vet ./...
   go test ./...
   go build ./cmd/smr
   ```

   On Windows, also run the installer check CI runs:

   ```powershell
   pwsh ./scripts/check-install.ps1
   ```

3. Commit and push the branch.

   ```sh
   git push -u origin fix/short-description
   ```

4. Open a pull request. Use the PR template if one is present, describe what
   changed and why, and link an issue when there is one.

5. Merge only after the `test` check passes. The branch must be up to date with
   `main` before merging. Merging deletes the head branch automatically.

## What branch protection enforces

| Rule | Setting |
|---|---|
| Pull request required | Yes |
| Required approving reviews | 0 (self-merge allowed, see below) |
| Required status checks | `test`, branch must be up to date |
| Conversation resolution | Required before merge |
| Force pushes | Blocked |
| Branch deletion | Blocked |
| Administrators | Covered by the same rules |

Zero required approvals keeps a single maintainer able to merge their own
work. Raise it once the project has a second regular reviewer:

```sh
gh api --method PATCH repos/acmeai-vn/SMR/branches/main/protection/required_pull_request_reviews \
  -F required_approving_review_count=1
```

## Releases

Releases are cut from a tag, and tag pushes are not covered by the `main`
branch protection. See `docs/RELEASE.md` for the release procedure.

## Reporting security issues

Do not open a public issue for a vulnerability. Email support@acmeai.vn with
the details and a way to reproduce it.