package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	updates           []resend.UpdateDomainRequest
	getsUntilVerified int
	verified          bool
	statusAfterVerify string
	notFoundErr       error
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
		return resend.Domain{}, f.missingError("Domain not found")
	}
	if f.verified {
		if f.getsUntilVerified > 0 {
			f.getsUntilVerified--
			d.Status = resend.DomainStatusPending
		} else if f.statusAfterVerify != "" {
			d.Status = f.statusAfterVerify
		} else {
			d.Status = resend.DomainStatusVerified
		}
	}
	return *d, nil
}

func (f *fakeDomains) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateDomainRequest) (resend.Domain, error) {
	d, ok := f.domains[id]
	if !ok {
		return resend.Domain{}, f.missingError("Domain not found")
	}
	f.updates = append(f.updates, *params)
	d.ClickTracking = params.ClickTracking
	d.OpenTracking = params.OpenTracking
	if params.TrackingSubdomain != "" {
		d.TrackingSubdomain = params.TrackingSubdomain
	}
	return *d, nil
}

func (f *fakeDomains) VerifyWithContext(ctx context.Context, id string) (bool, error) {
	if _, ok := f.domains[id]; !ok {
		return false, f.missingError("Domain not found")
	}
	f.verified = true
	return true, nil
}

func (f *fakeDomains) RemoveWithContext(ctx context.Context, id string) (bool, error) {
	if _, ok := f.domains[id]; !ok {
		return false, f.missingError("Domain not found")
	}
	delete(f.domains, id)
	return true, nil
}

func (f *fakeDomains) missingError(msg string) error {
	if f.notFoundErr != nil {
		return f.notFoundErr
	}
	return fmt.Errorf("[ERROR]: %s", msg)
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

func TestDomainSetToUnsetUpdateSemantics(t *testing.T) {
	fake := &fakeDomains{domains: map[string]*resend.Domain{}}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })

	inputs := property.NewMap(map[string]property.Value{
		"name":          property.New("example.com"),
		"openTracking":  property.New(true),
		"clickTracking": property.New(true),
		"tls":           property.New("enforced"),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("Domain", "unset"), Properties: inputs})
	require.NoError(t, err)
	fake.updates = nil // create sets tls via the update API; only inspect the subsequent update.

	unsetInputs := property.NewMap(map[string]property.Value{"name": property.New("example.com")})
	diff, err := s.Diff(p.DiffRequest{
		Urn: urn("Domain", "unset"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: unsetInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"openTracking":  {Kind: p.Update, InputDiff: true},
		"clickTracking": {Kind: p.Update, InputDiff: true},
		"tls":           {Kind: p.Update, InputDiff: true},
	}, diff.DetailedDiff)

	updated, err := s.Update(p.UpdateRequest{
		Urn: urn("Domain", "unset"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: unsetInputs,
	})
	require.NoError(t, err)
	require.Len(t, fake.updates, 1)
	payload := map[string]any{}
	require.NoError(t, json.Unmarshal(mustMarshalJSON(t, fake.updates[0]), &payload))
	assert.Equal(t, map[string]any{
		"open_tracking":  false,
		"click_tracking": false,
		"tls":            "opportunistic",
	}, payload)
	for _, key := range []string{"openTracking", "clickTracking", "tls"} {
		_, ok := updated.Properties.GetOk(key)
		assert.False(t, ok, "%s should remain unset in provider inputs/state", key)
	}
}

func TestDomainTrackingSubdomainSetToUnsetRequiresReplacement(t *testing.T) {
	fake := &fakeDomains{domains: map[string]*resend.Domain{}}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })

	inputs := property.NewMap(map[string]property.Value{
		"name":              property.New("example.com"),
		"trackingSubdomain": property.New("links"),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("Domain", "tracking"), Properties: inputs})
	require.NoError(t, err)
	unsetInputs := property.NewMap(map[string]property.Value{"name": property.New("example.com")})

	diff, err := s.Diff(p.DiffRequest{
		Urn: urn("Domain", "tracking"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: unsetInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"trackingSubdomain": {Kind: p.UpdateReplace, InputDiff: true},
	}, diff.DetailedDiff)

	_, err = s.Update(p.UpdateRequest{
		Urn: urn("Domain", "tracking"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: unsetInputs,
	})
	assert.ErrorContains(t, err, "clearing trackingSubdomain requires replacement")
}

func TestDomainImportReadPopulatesRequiredInputsAndOutputs(t *testing.T) {
	fake := &fakeDomains{domains: map[string]*resend.Domain{
		"dom_1": {
			Id: "dom_1", Name: "example.com", Status: resend.DomainStatusVerified,
			Region: "eu-west-1", OpenTracking: true, ClickTracking: true, TrackingSubdomain: "links",
			CreatedAt: "2026-08-25T00:00:00.000Z",
			Records: []resend.Record{{
				Record: resend.RecordTypeSPF, Name: "send", Type: "MX", Ttl: "Auto",
				Status: resend.DomainRecordStatusVerified, Value: "feedback-smtp.eu-west-1.amazonses.com", Priority: "10",
			}},
		},
	}}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })

	read, err := s.Read(p.ReadRequest{Urn: urn("Domain", "imported"), ID: "dom_1"})
	require.NoError(t, err)
	assert.Equal(t, "dom_1", read.ID)
	assert.Equal(t, property.New("example.com"), read.Inputs.Get("name"))
	for _, key := range []string{"region", "openTracking", "clickTracking", "trackingSubdomain"} {
		_, ok := read.Inputs.GetOk(key)
		assert.False(t, ok, "%s should stay unset during import-like reads", key)
	}
	assert.Equal(t, property.New("verified"), read.Properties.Get("status"))
	assert.Equal(t, property.New("2026-08-25T00:00:00.000Z"), read.Properties.Get("createdAt"))
	assert.Equal(t, property.New([]property.Value{property.New(map[string]property.Value{
		"record":   property.New("SPF"),
		"name":     property.New("send"),
		"type":     property.New("MX"),
		"ttl":      property.New("Auto"),
		"status":   property.New("verified"),
		"value":    property.New("feedback-smtp.eu-west-1.amazonses.com"),
		"priority": property.New("10"),
	})}), read.Properties.Get("records"))
}

func TestDomainReadPreservesUnsetOptionalInputs(t *testing.T) {
	fake := &fakeDomains{domains: map[string]*resend.Domain{
		"dom_1": {
			Id: "dom_1", Name: "example.com", Status: resend.DomainStatusVerified,
			Region: "us-east-1", OpenTracking: true, ClickTracking: true, TrackingSubdomain: "links",
		},
	}}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })
	inputs := property.NewMap(map[string]property.Value{"name": property.New("example.com")})

	read, err := s.Read(p.ReadRequest{
		Urn: urn("Domain", "read"), ID: "dom_1",
		Properties: inputs.Set("status", property.New("verified")), Inputs: inputs,
	})
	require.NoError(t, err)
	for _, key := range []string{"region", "openTracking", "clickTracking", "trackingSubdomain"} {
		_, ok := read.Inputs.GetOk(key)
		assert.False(t, ok, "%s should not be populated from remote defaults", key)
		_, ok = read.Properties.GetOk(key)
		assert.False(t, ok, "%s should not be populated into state", key)
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestDomainVerificationFailsOnTerminalFailureStatus(t *testing.T) {
	fake := &fakeDomains{
		domains:           map[string]*resend.Domain{"dom_1": {Id: "dom_1", Name: "example.com", Status: resend.DomainStatusNotStarted}},
		statusAfterVerify: resend.DomainStatusFailed,
	}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })

	_, err := s.Create(p.CreateRequest{
		Urn: urn("DomainVerification", "failed"),
		Properties: property.NewMap(map[string]property.Value{
			"domainId": property.New("dom_1"),
		}),
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "failed with status \"failed\"")
}

func TestDomainVerificationTimesOutWithoutSleeping(t *testing.T) {
	fake := &fakeDomains{
		domains:           map[string]*resend.Domain{"dom_1": {Id: "dom_1", Name: "example.com", Status: resend.DomainStatusNotStarted}},
		getsUntilVerified: 1,
	}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })

	_, err := s.Create(p.CreateRequest{
		Urn: urn("DomainVerification", "timeout"),
		Properties: property.NewMap(map[string]property.Value{
			"domainId":       property.New("dom_1"),
			"timeoutSeconds": property.New(0.0),
		}),
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "timed out after 0s")
	assert.ErrorContains(t, err, "status \"pending\"")
}

func TestDomainVerificationHonorsCancellation(t *testing.T) {
	old := verifyPollInterval
	verifyPollInterval = time.Hour
	t.Cleanup(func() { verifyPollInterval = old })

	ctx, cancel := context.WithCancel(t.Context())
	fake := &fakeDomains{
		domains:           map[string]*resend.Domain{"dom_1": {Id: "dom_1", Name: "example.com", Status: resend.DomainStatusNotStarted}},
		getsUntilVerified: 1,
	}
	oldClient := newClient
	newClient = func(key string) *resend.Client {
		c := resend.NewClient(key)
		c.Domains = fake
		return c
	}
	t.Cleanup(func() { newClient = oldClient })
	prov, err := New()
	require.NoError(t, err)
	s, err := integration.NewServer(ctx, Name, semver.MustParse("0.0.1"), integration.WithProvider(prov))
	require.NoError(t, err)
	require.NoError(t, s.Configure(p.ConfigureRequest{
		Args: property.NewMap(map[string]property.Value{"apiKey": property.New("re_test")}),
	}))
	cancel()

	_, err = s.Create(p.CreateRequest{
		Urn: urn("DomainVerification", "cancelled"),
		Properties: property.NewMap(map[string]property.Value{
			"domainId": property.New("dom_1"),
		}),
	})
	require.ErrorIs(t, err, context.Canceled)
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
	keys        map[string]*resend.ApiKey
	pages       [][]resend.ApiKey
	listAfters  []string
	notFoundErr error
}

func (f *fakeApiKeys) CreateWithContext(ctx context.Context, params *resend.CreateApiKeyRequest) (resend.CreateApiKeyResponse, error) {
	k := &resend.ApiKey{Id: "key_1", Name: params.Name}
	f.keys[k.Id] = k
	return resend.CreateApiKeyResponse{Id: k.Id, Token: "re_secret_token"}, nil
}

func (f *fakeApiKeys) ListWithOptions(ctx context.Context, options *resend.ListOptions) (resend.ListApiKeysResponse, error) {
	after := deref(options.After)
	f.listAfters = append(f.listAfters, after)
	if f.pages != nil {
		page := 0
		if after != "" {
			page = len(f.pages)
			for i, keys := range f.pages {
				if len(keys) > 0 && keys[len(keys)-1].Id == after {
					page = i + 1
					break
				}
			}
		}
		if page >= len(f.pages) {
			return resend.ListApiKeysResponse{Object: "list"}, nil
		}
		return resend.ListApiKeysResponse{Object: "list", Data: f.pages[page], HasMore: page < len(f.pages)-1}, nil
	}
	resp := resend.ListApiKeysResponse{Object: "list"}
	for _, k := range f.keys {
		resp.Data = append(resp.Data, *k)
	}
	return resp, nil
}

func (f *fakeApiKeys) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateApiKeyRequest) (resend.UpdateApiKeyResponse, error) {
	k, ok := f.keys[id]
	if !ok {
		return resend.UpdateApiKeyResponse{}, f.missingError("API key not found")
	}
	k.Name = params.Name
	return resend.UpdateApiKeyResponse{Object: "api_key", Id: id}, nil
}

func (f *fakeApiKeys) RemoveWithContext(ctx context.Context, id string) (bool, error) {
	if _, ok := f.keys[id]; !ok {
		return false, f.missingError("API key not found")
	}
	delete(f.keys, id)
	return true, nil
}

func (f *fakeApiKeys) missingError(msg string) error {
	if f.notFoundErr != nil {
		return f.notFoundErr
	}
	return fmt.Errorf("[ERROR]: %s", msg)
}

func TestApiKeyImportHasEmptySecretToken(t *testing.T) {
	fake := &fakeApiKeys{keys: map[string]*resend.ApiKey{
		"key_1": {Id: "key_1", Name: "imported"},
	}}
	s := testServer(t, func(c *resend.Client) { c.ApiKeys = fake })

	read, err := s.Read(p.ReadRequest{Urn: urn("ApiKey", "imported"), ID: "key_1"})
	require.NoError(t, err)
	assert.Equal(t, "key_1", read.ID)
	assert.Equal(t, property.New("imported"), read.Inputs.Get("name"))
	assert.Equal(t, property.New("").WithSecret(true), read.Properties.Get("token"))
}

func TestApiKeyReadFindsKeyAcrossPaginatedList(t *testing.T) {
	fake := &fakeApiKeys{pages: [][]resend.ApiKey{
		{{Id: "key_1", Name: "first-page"}},
		{{Id: "key_2", Name: "second-page"}},
	}}
	s := testServer(t, func(c *resend.Client) { c.ApiKeys = fake })

	read, err := s.Read(p.ReadRequest{Urn: urn("ApiKey", "paged"), ID: "key_2"})
	require.NoError(t, err)
	assert.Equal(t, "key_2", read.ID)
	assert.Equal(t, property.New("second-page"), read.Inputs.Get("name"))
	assert.Equal(t, []string{"", "key_1"}, fake.listAfters)
}

func TestApiKeyReadPreservesExistingSecretToken(t *testing.T) {
	fake := &fakeApiKeys{keys: map[string]*resend.ApiKey{
		"key_1": {Id: "key_1", Name: "renamed-in-resend"},
	}}
	s := testServer(t, func(c *resend.Client) { c.ApiKeys = fake })

	read, err := s.Read(p.ReadRequest{
		Urn: urn("ApiKey", "refresh"), ID: "key_1",
		Properties: property.NewMap(map[string]property.Value{
			"name":  property.New("old"),
			"token": property.New("re_existing").WithSecret(true),
		}),
		Inputs: property.NewMap(map[string]property.Value{"name": property.New("old")}),
	})
	require.NoError(t, err)
	assert.Equal(t, property.New("renamed-in-resend"), read.Properties.Get("name"))
	assert.Equal(t, property.New("re_existing").WithSecret(true), read.Properties.Get("token"))
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
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"name":       property.New("ci"),
		"permission": property.New("sending_access"),
		"token":      property.New("re_secret_token").WithSecret(true),
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
	assert.Equal(t, property.New("re_secret_token").WithSecret(true), updated.Properties.Get("token"))

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
	assert.Equal(t, property.New("re_secret_token").WithSecret(true), read.Properties.Get("token"))

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("ApiKey", "test"), ID: created.ID, Properties: updated.Properties}))
	assert.Empty(t, fake.keys)
}

type fakeWebhooks struct {
	resend.WebhooksSvc
	hooks       map[string]*resend.Webhook
	notFoundErr error
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
		return nil, f.missingError("Webhook not found")
	}
	return w, nil
}

func (f *fakeWebhooks) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateWebhookRequest) (*resend.UpdateWebhookResponse, error) {
	w, ok := f.hooks[id]
	if !ok {
		return nil, f.missingError("Webhook not found")
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
	if _, ok := f.hooks[id]; !ok {
		return nil, f.missingError("Webhook not found")
	}
	delete(f.hooks, id)
	return &resend.DeleteWebhookResponse{}, nil
}

func (f *fakeWebhooks) missingError(msg string) error {
	if f.notFoundErr != nil {
		return f.notFoundErr
	}
	return fmt.Errorf("[ERROR]: %s", msg)
}

func TestWebhookImportHasEmptySecretWhenResendOmitsSigningSecret(t *testing.T) {
	fake := &fakeWebhooks{hooks: map[string]*resend.Webhook{
		"wh_1": {
			Id: "wh_1", Endpoint: "https://example.com/imported", Events: []string{"email.sent"},
			Status: "enabled", CreatedAt: "2026-08-25T00:00:00.000Z",
		},
	}}
	s := testServer(t, func(c *resend.Client) { c.Webhooks = fake })

	read, err := s.Read(p.ReadRequest{Urn: urn("Webhook", "imported"), ID: "wh_1"})
	require.NoError(t, err)
	assert.Equal(t, "wh_1", read.ID)
	assert.Equal(t, property.New("https://example.com/imported"), read.Inputs.Get("endpoint"))
	assert.Equal(t, property.New("").WithSecret(true), read.Properties.Get("signingSecret"))
}

func TestWebhookReadPreservesExistingSecretWhenResendOmitsSigningSecret(t *testing.T) {
	fake := &fakeWebhooks{hooks: map[string]*resend.Webhook{
		"wh_1": {
			Id: "wh_1", Endpoint: "https://example.com/hook", Events: []string{"email.sent"},
			Status: "enabled", CreatedAt: "2026-08-25T00:00:00.000Z",
		},
	}}
	s := testServer(t, func(c *resend.Client) { c.Webhooks = fake })

	read, err := s.Read(p.ReadRequest{
		Urn: urn("Webhook", "refresh"), ID: "wh_1",
		Properties: property.NewMap(map[string]property.Value{
			"endpoint":      property.New("https://example.com/hook"),
			"events":        property.New([]property.Value{property.New("email.sent")}),
			"signingSecret": property.New("whsec_existing").WithSecret(true),
		}),
		Inputs: property.NewMap(map[string]property.Value{
			"endpoint": property.New("https://example.com/hook"),
			"events":   property.New([]property.Value{property.New("email.sent")}),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, property.New("whsec_existing").WithSecret(true), read.Properties.Get("signingSecret"))
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
		"signingSecret": property.New("whsec_123").WithSecret(true),
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
	assert.Equal(t, property.New("whsec_123").WithSecret(true), updated.Properties.Get("signingSecret"))

	read, err := s.Read(p.ReadRequest{
		Urn: urn("Webhook", "test"), ID: created.ID,
		Properties: updated.Properties,
		Inputs: property.NewMap(map[string]property.Value{
			"endpoint": property.New("https://example.com/hook2"),
			"events":   property.New([]property.Value{property.New("email.sent"), property.New("email.bounced")}),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, property.New("whsec_123").WithSecret(true), read.Properties.Get("signingSecret"))

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("Webhook", "test"), ID: created.ID, Properties: updated.Properties}))
	assert.Empty(t, fake.hooks)
}

type statusCodeError int

func (e statusCodeError) Error() string   { return fmt.Sprintf("HTTP %d", int(e)) }
func (e statusCodeError) StatusCode() int { return int(e) }

func TestTypedNotFoundRemovesResourcesAndDeletesIdempotently(t *testing.T) {
	notFound := statusCodeError(404)

	fakeDomains := &fakeDomains{domains: map[string]*resend.Domain{}, notFoundErr: notFound}
	domainServer := testServer(t, func(c *resend.Client) { c.Domains = fakeDomains })
	for typ, id := range map[string]string{"Domain": "dom_missing", "DomainVerification": "dom_missing"} {
		read, err := domainServer.Read(p.ReadRequest{Urn: urn(typ, "missing"), ID: id})
		require.NoError(t, err)
		assert.Equal(t, "", read.ID, "%s should be removed from state when Resend returns a typed 404", typ)
	}
	require.NoError(t, domainServer.Delete(p.DeleteRequest{
		Urn: urn("Domain", "missing"), ID: "dom_missing",
		Properties: property.NewMap(map[string]property.Value{
			"name": property.New("example.com"), "status": property.New("verified"), "createdAt": property.New("now"),
			"records": property.New([]property.Value{}),
		}),
	}))

	apiKeyServer := testServer(t, func(c *resend.Client) {
		c.ApiKeys = &fakeApiKeys{keys: map[string]*resend.ApiKey{}, notFoundErr: notFound}
	})
	require.NoError(t, apiKeyServer.Delete(p.DeleteRequest{
		Urn: urn("ApiKey", "missing"), ID: "key_missing",
		Properties: property.NewMap(map[string]property.Value{"name": property.New("ci"), "token": property.New("").WithSecret(true)}),
	}))

	webhookServer := testServer(t, func(c *resend.Client) {
		c.Webhooks = &fakeWebhooks{hooks: map[string]*resend.Webhook{}, notFoundErr: notFound}
	})
	read, err := webhookServer.Read(p.ReadRequest{Urn: urn("Webhook", "missing"), ID: "wh_missing"})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID, "Webhook should be removed from state when Resend returns a typed 404")
	require.NoError(t, webhookServer.Delete(p.DeleteRequest{
		Urn: urn("Webhook", "missing"), ID: "wh_missing",
		Properties: property.NewMap(map[string]property.Value{
			"endpoint": property.New("https://example.com/hook"), "events": property.New([]property.Value{property.New("email.sent")}),
			"signingSecret": property.New("").WithSecret(true), "status": property.New("enabled"), "createdAt": property.New("now"),
		}),
	}))
}

func TestReadRemovesMissingResources(t *testing.T) {
	fakeDomains := &fakeDomains{domains: map[string]*resend.Domain{}}
	domainServer := testServer(t, func(c *resend.Client) { c.Domains = fakeDomains })
	for typ, id := range map[string]string{"Domain": "dom_missing", "DomainVerification": "dom_missing"} {
		read, err := domainServer.Read(p.ReadRequest{Urn: urn(typ, "missing"), ID: id})
		require.NoError(t, err)
		assert.Equal(t, "", read.ID, "%s should be removed from state when Resend reports not found", typ)
	}

	apiKeyServer := testServer(t, func(c *resend.Client) { c.ApiKeys = &fakeApiKeys{keys: map[string]*resend.ApiKey{}} })
	read, err := apiKeyServer.Read(p.ReadRequest{Urn: urn("ApiKey", "missing"), ID: "key_missing"})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID, "ApiKey should be removed from state when omitted from the API listing")

	webhookServer := testServer(t, func(c *resend.Client) { c.Webhooks = &fakeWebhooks{hooks: map[string]*resend.Webhook{}} })
	read, err = webhookServer.Read(p.ReadRequest{Urn: urn("Webhook", "missing"), ID: "wh_missing"})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID, "Webhook should be removed from state when Resend reports not found")
}

func TestConfigureRequiresApiKey(t *testing.T) {
	prov, err := New()
	require.NoError(t, err)
	s, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(prov))
	require.NoError(t, err)
	t.Setenv("RESEND_API_KEY", "")
	err = s.Configure(p.ConfigureRequest{})
	assert.ErrorContains(t, err, "RESEND_API_KEY")

	// The environment variable is read at configure time, never through the
	// checked inputs, so it can't end up in state.
	t.Setenv("RESEND_API_KEY", "re_from_env")
	prov, err = New()
	require.NoError(t, err)
	s, err = integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(prov))
	require.NoError(t, err)
	assert.NoError(t, s.Configure(p.ConfigureRequest{}))
}

// Regression test for REPORT.md: with `apiKey` unset (env-var flow), CheckConfig
// must not materialize the field into the checked provider inputs. When it does,
// DiffConfig sees `apiKey` as added relative to the bare `{version}` inputs the
// CLI records on `pulumi import` (and on version bumps), and infer marks any
// non-version config change as a provider *replacement* — cascading a
// delete-replace into every resource, including production domains.
func TestUnsetApiKeyDoesNotReplaceProvider(t *testing.T) {
	prov, err := New()
	require.NoError(t, err)
	s, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.2"), integration.WithProvider(prov))
	require.NoError(t, err)
	purn := presource.NewURN("stack", "proj", "", "pulumi:providers:resend", "default_0_0_2")

	check, err := s.CheckConfig(p.CheckRequest{
		Urn:    purn,
		Inputs: property.NewMap(map[string]property.Value{"version": property.New("0.0.2")}),
	})
	require.NoError(t, err)
	_, materialized := check.Inputs.GetOk("apiKey")
	assert.False(t, materialized, "unset apiKey must not appear in checked provider inputs")

	// The engine diffs the stored provider inputs (bare {version}, as written by
	// `pulumi import`) against the freshly checked inputs plus the new version.
	oldInputs := property.NewMap(map[string]property.Value{"version": property.New("0.0.1")})
	diff, err := s.DiffConfig(p.DiffRequest{
		Urn:       purn,
		State:     oldInputs,
		OldInputs: oldInputs,
		Inputs:    check.Inputs.Set("version", property.New("0.0.2")),
	})
	require.NoError(t, err)
	for field, d := range diff.DetailedDiff {
		assert.NotContains(t, string(d.Kind), "replace", "config field %q must not force provider replacement", field)
	}
	assert.False(t, diff.HasChanges)
}

func TestSchema(t *testing.T) {
	schemaBytes := currentSchema(t)
	var schema struct {
		LogoURL string `json:"logoUrl"`
		Config  struct {
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
	require.NoError(t, json.Unmarshal(schemaBytes, &schema))

	for _, token := range []string{
		"resend:index:Domain",
		"resend:index:DomainVerification",
		"resend:index:ApiKey",
		"resend:index:Webhook",
	} {
		_, ok := schema.Resources[token]
		assert.True(t, ok, "missing resource %s", token)
	}
	assert.Equal(t, "https://raw.githubusercontent.com/iwahbe/pulumi-resend/main/assets/resend-icon-black.svg", schema.LogoURL)
	assert.True(t, schema.Config.Variables["apiKey"].Secret)
	assert.True(t, schema.Resources["resend:index:ApiKey"].Properties["token"].Secret)
	assert.True(t, schema.Resources["resend:index:Webhook"].Properties["signingSecret"].Secret)
}

func TestSchemaSnapshotCompatible(t *testing.T) {
	currentBytes := currentSchema(t)
	if os.Getenv("UPDATE_SCHEMA_SNAPSHOT") == "1" {
		writeSchemaSnapshot(t, currentBytes)
	}
	baseline := decodeSchema(t, readSchemaSnapshot(t))
	current := decodeSchema(t, currentBytes)

	assertCompatibleSchema(t, baseline, current)
}

type schemaSnapshot struct {
	Config    schemaObject              `json:"config"`
	Provider  schemaObject              `json:"provider"`
	Resources map[string]schemaResource `json:"resources"`
	Types     map[string]schemaObject   `json:"types"`
}

type schemaObject struct {
	Required        []string                  `json:"required"`
	InputProperties map[string]schemaProperty `json:"inputProperties"`
	Properties      map[string]schemaProperty `json:"properties"`
	Variables       map[string]schemaProperty `json:"variables"`
}

type schemaResource struct {
	RequiredInputs  []string                  `json:"requiredInputs"`
	InputProperties map[string]schemaProperty `json:"inputProperties"`
	Properties      map[string]schemaProperty `json:"properties"`
}

type schemaProperty struct {
	Ref                  string           `json:"$ref"`
	Type                 any              `json:"type"`
	Items                *schemaProperty  `json:"items"`
	AdditionalProperties *schemaProperty  `json:"additionalProperties"`
	OneOf                []schemaProperty `json:"oneOf"`
	Secret               bool             `json:"secret"`
}

func currentSchema(t *testing.T) []byte {
	t.Helper()
	fake := &fakeDomains{domains: map[string]*resend.Domain{}}
	s := testServer(t, func(c *resend.Client) { c.Domains = fake })
	resp, err := s.GetSchema(p.GetSchemaRequest{})
	require.NoError(t, err)
	return []byte(resp.Schema)
}

func readSchemaSnapshot(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/schema.json")
	require.NoError(t, err)
	return b
}

func writeSchemaSnapshot(t *testing.T, data []byte) {
	t.Helper()
	var schema any
	require.NoError(t, json.Unmarshal(data, &schema))
	b, err := json.MarshalIndent(schema, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("testdata/schema.json", append(b, '\n'), 0o644))
}

func decodeSchema(t *testing.T, data []byte) schemaSnapshot {
	t.Helper()
	var schema schemaSnapshot
	require.NoError(t, json.Unmarshal(data, &schema))
	return schema
}

func assertCompatibleSchema(t *testing.T, baseline, current schemaSnapshot) {
	t.Helper()
	assertObjectCompatible(t, "config", baseline.Config, current.Config, true)
	assertObjectCompatible(t, "provider", baseline.Provider, current.Provider, false)
	for token, base := range baseline.Resources {
		cur, ok := current.Resources[token]
		require.True(t, ok, "resource %s was removed", token)
		assertNoRemoved(t, "resource "+token+" input", base.InputProperties, cur.InputProperties)
		assertNoRemoved(t, "resource "+token+" property", base.Properties, cur.Properties)
		assertNoNewRequired(t, "resource "+token+" required input", base.RequiredInputs, cur.RequiredInputs)
	}
	for token, base := range baseline.Types {
		cur, ok := current.Types[token]
		require.True(t, ok, "type %s was removed", token)
		assertObjectCompatible(t, "type "+token, base, cur, false)
	}
}

func assertObjectCompatible(t *testing.T, path string, baseline, current schemaObject, variables bool) {
	t.Helper()
	if variables {
		assertNoRemoved(t, path+" variable", baseline.Variables, current.Variables)
	} else {
		assertNoRemoved(t, path+" input property", baseline.InputProperties, current.InputProperties)
		assertNoRemoved(t, path+" property", baseline.Properties, current.Properties)
	}
	assertNoNewRequired(t, path+" required property", baseline.Required, current.Required)
}

func assertNoRemoved[T any](t *testing.T, path string, baseline, current map[string]T) {
	t.Helper()
	for name := range baseline {
		_, ok := current[name]
		assert.True(t, ok, "%s %s was removed", path, name)
	}
}

func assertNoNewRequired(t *testing.T, path string, baseline, current []string) {
	t.Helper()
	base := map[string]bool{}
	for _, name := range baseline {
		base[name] = true
	}
	for _, name := range current {
		assert.True(t, base[name], "%s %s was added", path, name)
	}
}
