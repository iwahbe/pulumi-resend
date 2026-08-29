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

### Domain verification workflow

`DomainVerification` is an intentional, operator-approved imperative exception in
this provider. Most resources model CRUD operations only, but domain readiness often
needs stronger semantics: create a Resend domain, publish the DNS records Resend
returns, ask Resend to verify the domain, and fail the deployment unless Resend sees
the domain as verified. Use `DomainVerification` when a stack should not complete
until the domain is ready for mail traffic. Omit it when you prefer to manage the
verification click/wait manually outside Pulumi.

Create `DomainVerification` after the DNS records derived from `Domain.records`. On
create, the resource calls Resend's verify endpoint once, then polls the domain until
its `status` output is `verified`. The resource ID is the Resend domain ID. Deleting
or replacing `DomainVerification` only removes/replaces this Pulumi checkpoint; it
does not delete or unverify the Resend domain. Delete the `Domain` resource to remove
the domain from Resend.

#### Complete TypeScript example with DNS records

This example creates a Resend domain, publishes all DNS records through Cloudflare,
and then waits for verification. It assumes you have run `pulumi package add resend
--server github://api.github.com/iwahbe/pulumi-resend` and installed the Cloudflare
provider package.

```ts
import * as pulumi from "@pulumi/pulumi";
import * as cloudflare from "@pulumi/cloudflare";
import * as resend from "@pulumi/resend";

const zoneId = new pulumi.Config("cloudflare").require("zoneId");

const domain = new resend.Domain("example", {
    name: "example.com",
    region: "us-east-1",          // also: "eu-west-1", "sa-east-1", "ap-northeast-1"
    tls: "opportunistic",         // or "enforced"
    capabilities: { sending: "enabled" },
});

const dnsRecords = domain.records.apply(records => records.map((record, index) =>
    new cloudflare.Record(`resend-${index}`, {
        zoneId,
        name: record.name.endsWith(".example.com")
            ? record.name.slice(0, -".example.com".length) || "@"
            : record.name,
        type: record.type,
        value: record.value,
        ttl: Number(record.ttl) || 1,
        priority: record.priority ? Number(record.priority) : undefined,
    })));

const verification = new resend.DomainVerification("example", {
    domainId: domain.id,
    timeoutSeconds: 1800,
}, { dependsOn: dnsRecords });

export const domainStatus = verification.status;
```

For other DNS providers, create one DNS resource for each `Domain.records` element
using its `name`, `type`, `value`, `ttl`, and optional `priority`, then pass those
resources in `dependsOn` for `DomainVerification`. DNS providers differ on whether
record names should be fully-qualified (`send.example.com`) or relative to the zone
(`send`); adjust the `name` field accordingly.

#### Timeout, cancellation, and failures

The default verification timeout is 900 seconds. Set `timeoutSeconds` when DNS for a
zone regularly takes longer to propagate. If the timeout expires, the Pulumi update
fails and no `DomainVerification` state is recorded; fix DNS or increase the timeout
and run `pulumi up` again. If Pulumi is cancelled, the provider stops polling and the
update is cancelled. If Resend reports `failed` or `partially_failed`, the provider
fails immediately with guidance to check DNS records.

A failed or cancelled Pulumi update may still have triggered verification in Resend.
That is safe: rerunning `pulumi up` triggers verification again and resumes polling.
Changing only `timeoutSeconds` after successful creation is a no-op because it only
affects the create-time wait. Changing `domainId` replaces the verification
checkpoint.

#### Troubleshooting pending or failed verification

- Confirm every record in `Domain.records` exists in authoritative DNS with the exact
  type, value, and MX priority Resend returned.
- Check whether your DNS provider expects relative names; double zone suffixes such
  as `send.example.com.example.com` are a common cause of pending verification.
- Wait for DNS TTLs and negative-cache TTLs to expire, especially after correcting a
  missing or incorrect record.
- Use `dig`, `nslookup`, or your DNS provider's query tools against authoritative
  nameservers to verify public DNS, not only the provider dashboard.
- Ensure CNAME records are not proxied or flattened in a way that changes the value
  Resend expects.
- If Resend returns `failed` or `partially_failed`, correct DNS and rerun `pulumi up`;
  the provider will call verify again.

### Imports and write-only secrets

Import `Domain` and `DomainVerification` resources by Resend domain ID, for example
`pulumi import resend:index:DomainVerification verified dom_...`. Importing
`DomainVerification` records the current domain status in Pulumi state; it
does not call the verify endpoint or wait for the domain to become verified.

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
# PR CI compares that schema with the merge-base commit's pulumi-artifacts/schema.json using pulumi/schema-tools v0.8.1.
```

Releases are cut by pushing a `v*` tag; goreleaser builds the
`pulumi-resource-resend-v<version>-<os>-<arch>.tar.gz` assets Pulumi expects.
