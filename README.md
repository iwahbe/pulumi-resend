# pulumi-resend

> [!NOTE]
> This provider was authored by an AI agent on @iwahbe's behalf.

A Pulumi provider for managing [Resend](https://resend.com) email infrastructure.

## Resources

- `resend:index:Domain` — a sending/receiving domain. The `records` output holds the
  DNS records to publish.
- `resend:index:DomainVerification` — triggers verification of a domain and waits until
  Resend reports it `verified`. Create it after the domain's DNS records are published.
- `resend:index:ApiKey` — an API key. The `token` output is a secret and is only
  available at creation time (the Resend API never returns it again).
- `resend:index:Webhook` — an event webhook. The `signingSecret` output is a secret.

### Imports and write-only secrets

Resend cannot return an API key token after the key is created, and may omit a
webhook signing secret from later reads. On refresh or update, this provider keeps
any existing secret value already present in Pulumi state. On import, there is no
prior state to preserve, so unrecoverable values are recorded as empty secret
outputs (`ApiKey.token`, and `Webhook.signingSecret` when Resend omits it). If you
need those values after import, use [`pulumi state taint`](https://www.pulumi.com/docs/iac/cli/commands/pulumi_state_taint/) to force resource recreation.

## Configuration

| Key      | Description                                                        |
|----------|--------------------------------------------------------------------|
| `apiKey` | Resend API key (secret). Falls back to `$RESEND_API_KEY`.          |

## Usage

The provider plugin is distributed via GitHub releases and installs automatically
(the schema embeds `pluginDownloadURL: github://api.github.com/iwahbe/pulumi-resend`).
Generate a local SDK for your language with:

```sh
pulumi package add resend --server github://api.github.com/iwahbe/pulumi-resend
```

## Development

```sh
go build ./...       # build the provider
go test ./...        # run tests
go build -o pulumi-resource-resend && pulumi package get-schema ./pulumi-resource-resend  # inspect the schema
```

Releases are cut by pushing a `v*` tag; goreleaser builds the
`pulumi-resource-resend-v<version>-<os>-<arch>.tar.gz` assets Pulumi expects.
