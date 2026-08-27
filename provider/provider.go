// Package provider implements a Pulumi provider for Resend (https://resend.com).
package provider

import (
	"cmp"
	"context"
	"errors"
	"os"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/resend/resend-go/v4"
)

const Name = "resend"

// Version is set at build time via -ldflags.
var Version = "0.0.0-dev"

func New() (p.Provider, error) {
	prov, err := infer.NewProviderBuilder().
		WithDisplayName("Resend").
		WithDescription("A Pulumi provider for managing Resend email infrastructure.").
		WithNamespace("iwahbe").
		WithRepository("https://github.com/iwahbe/pulumi-resend").
		WithLicense("Apache-2.0").
		WithPluginDownloadURL("github://api.github.com/iwahbe/pulumi-resend").
		WithGoImportPath("github.com/iwahbe/pulumi-resend/sdk/go/resend").
		WithConfig(infer.Config(&Config{})).
		WithModuleMap(map[tokens.ModuleName]tokens.ModuleName{"provider": "index"}).
		WithResources(
			infer.Resource(&Domain{}),
			infer.Resource(&DomainVerification{}),
			infer.Resource(&ApiKey{}),
			infer.Resource(&Webhook{}),
		).
		Build()
	if err != nil {
		return p.Provider{}, err
	}

	// infer applies dependency-based secret annotations on create/update. Read
	// preserves existing secret markers from state, but import-like reads with no
	// prior state do not get AlwaysSecret markers, so apply them here to keep
	// write-only outputs secret in all raw Read responses.
	read := prov.Read
	prov.Read = func(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
		resp, err := read(ctx, req)
		if err != nil || resp.ID == "" {
			return resp, err
		}
		switch req.Urn.Type() {
		case "resend:index:ApiKey":
			resp.Properties = markSecretProperty(resp.Properties, "token")
		case "resend:index:Webhook":
			resp.Properties = markSecretProperty(resp.Properties, "signingSecret")
		}
		return resp, nil
	}
	return prov, nil
}

func markSecretProperty(m property.Map, key string) property.Map {
	if v, ok := m.GetOk(key); ok {
		m = m.Set(key, v.WithSecret(true))
	}
	return m
}

type Config struct {
	ApiKey *string `pulumi:"apiKey,optional" provider:"secret"`

	client *resend.Client
}

func (c *Config) Annotate(a infer.Annotator) {
	a.Describe(&c.ApiKey, "The Resend API key. Defaults to the value of the `RESEND_API_KEY` environment variable.")
}

// newClient is swapped out in tests to inject fake Resend services.
var newClient = resend.NewClient

func (c *Config) Configure(context.Context) error {
	apiKey := cmp.Or(deref(c.ApiKey), os.Getenv("RESEND_API_KEY"))
	if apiKey == "" {
		return errors.New("no Resend API key: set the `apiKey` provider configuration or the RESEND_API_KEY environment variable")
	}
	c.client = newClient(apiKey)
	return nil
}

func getClient(ctx context.Context) *resend.Client {
	return infer.GetConfig[Config](ctx).client
}

// isNotFound reports whether err is a Resend "not found" API error.
//
// ponytail: resend-go collapses non-2xx responses into opaque string errors, so
// message matching is the only signal available; switch to a typed check if the
// SDK ever grows one.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

func deref[T any](v *T) T {
	if v == nil {
		var zero T
		return zero
	}
	return *v
}

func ptrEq[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// keepFresh overwrites *dst with the remote value, but only if the input was
// previously set: unset optional inputs must stay unset so that Diff does not
// see a phantom change against server-side defaults.
func keepFresh[T comparable](dst **T, remote T) {
	if *dst != nil {
		*dst = &remote
	}
}
