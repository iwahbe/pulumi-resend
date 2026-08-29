# Contributing

## Commit messages

Commits on `main` (in practice: squash-merge PR titles) use
[Conventional Commits](https://www.conventionalcommits.org) language:

- `chore: ...` — no user-visible change
- `fix: ...` — a bug fix; patch version bump
- `feat: ...` — a new capability; minor version bump
- `fix!: ...` / `feat!: ...`, or a `BREAKING CHANGE:` footer — major version
  bump

[svu](https://github.com/caarlos0/svu) derives the next version from these
messages, so there are no changelog files to maintain. `.svu.yml` sets
`always: true`: a `chore:`-only history still bumps the patch version, keeping
every commit releasable.

## Per-commit artifacts

Every push to `main` runs the `artifacts` workflow. It builds the provider at
`svu next` — the version that commit would be released as — and publishes the
package schema plus generated SDKs to the `pulumi-artifacts` branch via
[pulumi-publish](https://github.com/iwahbe/pulumi-publish). Because the
version is a pure function of the history up to each commit, the artifacts a
commit publishes are exactly what its release would need.

For pull requests, CI treats the merge-base commit's published
`pulumi-artifacts:schema.json` as the baseline provider schema. It builds the
provider binary from the PR, extracts the candidate schema with
`pulumi package get-schema`, and compares it against that generated baseline
with `pulumi/schema-tools`. The workflow intentionally fails if the published
artifacts or schema are missing; do not check baseline schema fixtures into the
development branch.

## Releasing

1. Wait for the `artifacts` workflow to finish on the commit being released.
2. Tag that commit with the version svu computes, and push the tag:

   ```sh
   tag="$(svu next)"
   git tag "$tag"
   git push origin "$tag"
   ```

The `release` workflow then:

- publishes the provider binaries to a GitHub release (goreleaser), which is
  where `pulumi` downloads the plugin from, and
- tags the commit's prebuilt artifacts as `sdk/go/<version>`, making the Go
  SDK installable with
  `go get github.com/iwahbe/pulumi-resend/sdk/go@<version>`.

The release fails if the tagged commit's artifacts embed a different version
than the tag — tag with `svu next`, not by hand.
