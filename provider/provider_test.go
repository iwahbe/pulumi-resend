package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/resend/resend-go/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testServer(t *testing.T, setup func(*resend.Client)) integration.Server {
	old := newClient
	newClient = func(key string) *resend.Client {
		c := resend.NewClient(key)
		setup(c)
		return c
	}
	t.Cleanup(func() { newClient = old })

	prov, err := New()
	require.NoError(t, err)
	s, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(prov))
	require.NoError(t, err)
	require.NoError(t, s.Configure(p.ConfigureRequest{
		Args: property.NewMap(map[string]property.Value{"apiKey": property.New("re_test")}),
	}))
	return s
}

func urn(typ, name string) presource.URN {
	return presource.NewURN("stack", "proj", "", tokens.Type("resend:index:"+typ), name)
}

type fakeDomains struct {
	resend.DomainsSvc
	domains           map[string]*resend.Domain
	getsUntilVerified int
	verified          bool
}

func (f *fakeDomains) CreateWithContext(ctx context.Context, params *resend.CreateDomainRequest) (resend.CreateDomainResponse, error) {
	d := &resend.Domain{
		Id:                "dom_1",
		Name:              params.Name,
		Status:            resend.DomainStatusNotStarted,
		Region:            params.Region,
		CreatedAt:         "2026-08-25T00:00:00.000Z",
		ClickTracking:     params.ClickTracking != nil && *params.ClickTracking,
		OpenTracking:      params.OpenTracking != nil && *params.OpenTracking,
		TrackingSubdomain: params.TrackingSubdomain,
		Capabilities:      params.Capabilities,
		Records: []resend.Record{{
			Record: resend.RecordTypeSPF, Name: "send", Type: "MX", Ttl: "Auto",
			Status: resend.DomainRecordStatusNotStarted,
			Value:  "feedback-smtp.us-east-1.amazonses.com", Priority: "10",
		}},
	}
	f.domains[d.Id] = d
	return resend.CreateDomainResponse{
		Id: d.Id, Name: d.Name, CreatedAt: d.CreatedAt, Status: d.Status,
		Records: d.Records, Region: d.Region,
	}, nil
}

func (f *fakeDomains) GetWithContext(ctx context.Context, id string) (resend.Domain, error) {
	d, ok := f.domains[id]
	if !ok {
		return resend.Domain{}, fmt.Errorf("[ERROR]: Domain not found")
	}
	if f.verified {
		if f.getsUntilVerified > 0 {
			f.getsUntilVerified--
			d.Status = resend.DomainStatusPending
		} else {
			d.Status = resend.DomainStatusVerified
		}
	}
	return *d, nil
}

func (f *fakeDomains) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateDomainRequest) (resend.Domain, error) {
	d, ok := f.domains[id]
	if !ok {
		return resend.Domain{}, fmt.Errorf("[ERROR]: Domain not found")
	}
	d.ClickTracking = params.ClickTracking
	d.OpenTracking = params.OpenTracking
	if params.TrackingSubdomain != "" {
		d.TrackingSubdomain = params.TrackingSubdomain
	}
	return *d, nil
}

func (f *fakeDomains) VerifyWithContext(ctx context.Context, id string) (bool, error) {
	if _, ok := f.domains[id]; !ok {
		return false, fmt.Errorf("[ERROR]: Domain not found")
	}
	f.verified = true
	return true, nil
}

func (f *fakeDomains) RemoveWithContext(ctx context.Context, id string) (bool, error) {
	delete(f.domains, id)
	return true, nil
}

func TestDomainLifecycle(t *testing.T) {
	fake := &fakeDomains{domains: map[string]*resend.Domain{}}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })

	inputs := property.NewMap(map[string]property.Value{
		"name":          property.New("example.com"),
		"clickTracking": property.New(true),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("Domain", "test"), Properties: inputs})
	require.NoError(t, err)
	assert.Equal(t, "dom_1", created.ID)
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"name":          property.New("example.com"),
		"clickTracking": property.New(true),
		"status":        property.New("not_started"),
		"createdAt":     property.New("2026-08-25T00:00:00.000Z"),
		"records": property.New([]property.Value{property.New(map[string]property.Value{
			"record":   property.New("SPF"),
			"name":     property.New("send"),
			"type":     property.New("MX"),
			"ttl":      property.New("Auto"),
			"status":   property.New("not_started"),
			"value":    property.New("feedback-smtp.us-east-1.amazonses.com"),
			"priority": property.New("10"),
		})}),
	}), created.Properties)

	// Changing the name must replace; changing tracking must update in place.
	diff, err := s.Diff(p.DiffRequest{
		Urn: urn("Domain", "test"), ID: created.ID,
		State:     created.Properties,
		OldInputs: inputs,
		Inputs: property.NewMap(map[string]property.Value{
			"name":          property.New("other.com"),
			"clickTracking": property.New(false),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"name":          {Kind: p.UpdateReplace, InputDiff: true},
		"clickTracking": {Kind: p.Update, InputDiff: true},
	}, diff.DetailedDiff)

	updatedInputs := property.NewMap(map[string]property.Value{
		"name":          property.New("example.com"),
		"clickTracking": property.New(false),
	})
	updated, err := s.Update(p.UpdateRequest{
		Urn: urn("Domain", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, property.New(false), updated.Properties.Get("clickTracking"))
	assert.False(t, fake.domains["dom_1"].ClickTracking)

	read, err := s.Read(p.ReadRequest{
		Urn: urn("Domain", "test"), ID: created.ID,
		Properties: updated.Properties, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, "dom_1", read.ID)
	assert.Equal(t, property.New("not_started"), read.Properties.Get("status"))

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("Domain", "test"), ID: created.ID, Properties: updated.Properties}))
	assert.Empty(t, fake.domains)

	// A deleted domain reads back as missing (empty ID).
	read, err = s.Read(p.ReadRequest{
		Urn: urn("Domain", "test"), ID: created.ID,
		Properties: updated.Properties, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID)
}

func TestDomainVerificationWaits(t *testing.T) {
	old := verifyPollInterval
	verifyPollInterval = time.Millisecond
	t.Cleanup(func() { verifyPollInterval = old })

	fake := &fakeDomains{
		domains:           map[string]*resend.Domain{"dom_1": {Id: "dom_1", Name: "example.com", Status: resend.DomainStatusNotStarted}},
		getsUntilVerified: 2,
	}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })

	created, err := s.Create(p.CreateRequest{
		Urn: urn("DomainVerification", "test"),
		Properties: property.NewMap(map[string]property.Value{
			"domainId": property.New("dom_1"),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"domainId": property.New("dom_1"),
		"status":   property.New("verified"),
	}), created.Properties)
	assert.True(t, fake.verified)
	assert.Equal(t, 0, fake.getsUntilVerified)
}

type fakeApiKeys struct {
	resend.ApiKeysSvc
	keys map[string]*resend.ApiKey
}

func (f *fakeApiKeys) CreateWithContext(ctx context.Context, params *resend.CreateApiKeyRequest) (resend.CreateApiKeyResponse, error) {
	k := &resend.ApiKey{Id: "key_1", Name: params.Name}
	f.keys[k.Id] = k
	return resend.CreateApiKeyResponse{Id: k.Id, Token: "re_secret_token"}, nil
}

func (f *fakeApiKeys) ListWithOptions(ctx context.Context, options *resend.ListOptions) (resend.ListApiKeysResponse, error) {
	resp := resend.ListApiKeysResponse{Object: "list"}
	for _, k := range f.keys {
		resp.Data = append(resp.Data, *k)
	}
	return resp, nil
}

func (f *fakeApiKeys) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateApiKeyRequest) (resend.UpdateApiKeyResponse, error) {
	k, ok := f.keys[id]
	if !ok {
		return resend.UpdateApiKeyResponse{}, fmt.Errorf("[ERROR]: API key not found")
	}
	k.Name = params.Name
	return resend.UpdateApiKeyResponse{Object: "api_key", Id: id}, nil
}

func (f *fakeApiKeys) RemoveWithContext(ctx context.Context, id string) (bool, error) {
	delete(f.keys, id)
	return true, nil
}

func TestApiKeyLifecycle(t *testing.T) {
	fake := &fakeApiKeys{keys: map[string]*resend.ApiKey{}}
	s := testServer(t, func(c *resend.Client) { c.ApiKeys = fake })

	inputs := property.NewMap(map[string]property.Value{
		"name":       property.New("ci"),
		"permission": property.New("sending_access"),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("ApiKey", "test"), Properties: inputs})
	require.NoError(t, err)
	assert.Equal(t, "key_1", created.ID)
	// Secretness of `token` is carried by the schema (asserted in TestSchema), not
	// by markers on the raw gRPC response.
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"name":       property.New("ci"),
		"permission": property.New("sending_access"),
		"token":      property.New("re_secret_token"),
	}), created.Properties)

	diff, err := s.Diff(p.DiffRequest{
		Urn: urn("ApiKey", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs,
		Inputs: property.NewMap(map[string]property.Value{
			"name":       property.New("ci-renamed"),
			"permission": property.New("full_access"),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"name":       {Kind: p.Update, InputDiff: true},
		"permission": {Kind: p.UpdateReplace, InputDiff: true},
	}, diff.DetailedDiff)

	updated, err := s.Update(p.UpdateRequest{
		Urn: urn("ApiKey", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs,
		Inputs: property.NewMap(map[string]property.Value{
			"name":       property.New("ci-renamed"),
			"permission": property.New("sending_access"),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, "ci-renamed", fake.keys["key_1"].Name)
	// The token survives updates even though the API never returns it again.
	assert.Equal(t, property.New("re_secret_token"), updated.Properties.Get("token"))

	read, err := s.Read(p.ReadRequest{
		Urn: urn("ApiKey", "test"), ID: created.ID,
		Properties: updated.Properties,
		Inputs: property.NewMap(map[string]property.Value{
			"name":       property.New("ci-renamed"),
			"permission": property.New("sending_access"),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, "key_1", read.ID)

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("ApiKey", "test"), ID: created.ID, Properties: updated.Properties}))
	assert.Empty(t, fake.keys)
}

type fakeWebhooks struct {
	resend.WebhooksSvc
	hooks map[string]*resend.Webhook
}

func (f *fakeWebhooks) CreateWithContext(ctx context.Context, params *resend.CreateWebhookRequest) (*resend.CreateWebhookResponse, error) {
	w := &resend.Webhook{
		Id: "wh_1", Endpoint: params.Endpoint, Events: params.Events,
		Status: "enabled", CreatedAt: "2026-08-25T00:00:00.000Z", SigningSecret: "whsec_123",
	}
	f.hooks[w.Id] = w
	return &resend.CreateWebhookResponse{Object: "webhook", Id: w.Id, SigningSecret: w.SigningSecret}, nil
}

func (f *fakeWebhooks) GetWithContext(ctx context.Context, id string) (*resend.Webhook, error) {
	w, ok := f.hooks[id]
	if !ok {
		return nil, fmt.Errorf("[ERROR]: Webhook not found")
	}
	return w, nil
}

func (f *fakeWebhooks) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateWebhookRequest) (*resend.UpdateWebhookResponse, error) {
	w, ok := f.hooks[id]
	if !ok {
		return nil, fmt.Errorf("[ERROR]: Webhook not found")
	}
	if params.Endpoint != nil {
		w.Endpoint = *params.Endpoint
	}
	if params.Events != nil {
		w.Events = params.Events
	}
	return &resend.UpdateWebhookResponse{Object: "webhook", Id: id}, nil
}

func (f *fakeWebhooks) RemoveWithContext(ctx context.Context, id string) (*resend.DeleteWebhookResponse, error) {
	delete(f.hooks, id)
	return &resend.DeleteWebhookResponse{}, nil
}

func TestWebhookLifecycle(t *testing.T) {
	fake := &fakeWebhooks{hooks: map[string]*resend.Webhook{}}
	s := testServer(t, func(c *resend.Client) { c.Webhooks = fake })

	created, err := s.Create(p.CreateRequest{
		Urn: urn("Webhook", "test"),
		Properties: property.NewMap(map[string]property.Value{
			"endpoint": property.New("https://example.com/hook"),
			"events":   property.New([]property.Value{property.New("email.sent")}),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"endpoint":      property.New("https://example.com/hook"),
		"events":        property.New([]property.Value{property.New("email.sent")}),
		"signingSecret": property.New("whsec_123"),
		"status":        property.New("enabled"),
		"createdAt":     property.New("2026-08-25T00:00:00.000Z"),
	}), created.Properties)

	updated, err := s.Update(p.UpdateRequest{
		Urn: urn("Webhook", "test"), ID: created.ID,
		State: created.Properties,
		Inputs: property.NewMap(map[string]property.Value{
			"endpoint": property.New("https://example.com/hook2"),
			"events":   property.New([]property.Value{property.New("email.sent"), property.New("email.bounced")}),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/hook2", fake.hooks["wh_1"].Endpoint)
	assert.Equal(t, []string{"email.sent", "email.bounced"}, fake.hooks["wh_1"].Events)
	assert.Equal(t, property.New("whsec_123"), updated.Properties.Get("signingSecret"))

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("Webhook", "test"), ID: created.ID, Properties: updated.Properties}))
	assert.Empty(t, fake.hooks)
}

func TestConfigureRequiresApiKey(t *testing.T) {
	prov, err := New()
	require.NoError(t, err)
	s, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(prov))
	require.NoError(t, err)
	t.Setenv("RESEND_API_KEY", "")
	err = s.Configure(p.ConfigureRequest{})
	assert.ErrorContains(t, err, "RESEND_API_KEY")
}

func TestSchema(t *testing.T) {
	fake := &fakeDomains{domains: map[string]*resend.Domain{}}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })
	resp, err := s.GetSchema(p.GetSchemaRequest{})
	require.NoError(t, err)
	var schema struct {
		Config struct {
			Variables map[string]struct {
				Secret bool `json:"secret"`
			} `json:"variables"`
		} `json:"config"`
		Resources map[string]struct {
			Properties map[string]struct {
				Secret bool `json:"secret"`
			} `json:"properties"`
		} `json:"resources"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Schema), &schema))

	for _, token := range []string{
		"resend:index:Domain",
		"resend:index:DomainVerification",
		"resend:index:ApiKey",
		"resend:index:Webhook",
	} {
		_, ok := schema.Resources[token]
		assert.True(t, ok, "missing resource %s", token)
	}
	assert.True(t, schema.Config.Variables["apiKey"].Secret)
	assert.True(t, schema.Resources["resend:index:ApiKey"].Properties["token"].Secret)
	assert.True(t, schema.Resources["resend:index:Webhook"].Properties["signingSecret"].Secret)
}
