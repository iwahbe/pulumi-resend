// Package provider implements a Pulumi provider for Resend (https://resend.com).
package provider

import (
	"context"
	"errors"
	"os"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/resend/resend-go/v4"
)

const Name = "resend"

// Version is set at build time via -ldflags.
var Version = "0.0.0-dev"

func New() (p.Provider, error) {
	return infer.NewProviderBuilder().
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
}

type Config struct {
	ApiKey string `pulumi:"apiKey,optional" provider:"secret"`

	client *resend.Client
}

func (c *Config) Annotate(a infer.Annotator) {
	a.Describe(&c.ApiKey, "The Resend API key. Defaults to the value of the `RESEND_API_KEY` environment variable.")
}

// newClient is swapped out in tests to inject fake Resend services.
var newClient = resend.NewClient

func (c *Config) Configure(context.Context) error {
	if c.ApiKey == "" {
		// Read the fallback here rather than via an infer default so the key
		// never enters the checked inputs, and so never lands in state.
		c.ApiKey = os.Getenv("RESEND_API_KEY")
	}
	if c.ApiKey == "" {
		return errors.New("no Resend API key: set the `apiKey` provider configuration or the RESEND_API_KEY environment variable")
	}
	c.client = newClient(c.ApiKey)
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
