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

func isReplacementDiff(diff p.PropertyDiff) bool {
	return diff.Kind == p.AddReplace || diff.Kind == p.DeleteReplace || diff.Kind == p.UpdateReplace
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

type fakeSegments struct {
	resend.SegmentsSvc
	segments    map[string]*resend.Segment
	notFoundErr error
}

func (f *fakeSegments) CreateWithContext(ctx context.Context, params *resend.CreateSegmentRequest) (resend.CreateSegmentResponse, error) {
	s := &resend.Segment{Id: "seg_1", Name: params.Name, Object: "segment", CreatedAt: "2026-08-25T00:00:00.000Z"}
	f.segments[s.Id] = s
	return resend.CreateSegmentResponse{Id: s.Id, Name: s.Name, Object: s.Object}, nil
}

func (f *fakeSegments) GetWithContext(ctx context.Context, id string) (resend.Segment, error) {
	s, ok := f.segments[id]
	if !ok {
		return resend.Segment{}, f.missingError("Segment not found")
	}
	return *s, nil
}

func (f *fakeSegments) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateSegmentRequest) (resend.UpdateSegmentResponse, error) {
	s, ok := f.segments[id]
	if !ok {
		return resend.UpdateSegmentResponse{}, f.missingError("Segment not found")
	}
	s.Name = params.Name
	return resend.UpdateSegmentResponse{Id: id, Object: "segment"}, nil
}

func (f *fakeSegments) RemoveWithContext(ctx context.Context, id string) (resend.RemoveSegmentResponse, error) {
	if _, ok := f.segments[id]; !ok {
		return resend.RemoveSegmentResponse{}, f.missingError("Segment not found")
	}
	delete(f.segments, id)
	return resend.RemoveSegmentResponse{Id: id, Object: "segment", Deleted: true}, nil
}

func (f *fakeSegments) missingError(msg string) error {
	if f.notFoundErr != nil {
		return f.notFoundErr
	}
	return fmt.Errorf("[ERROR]: %s", msg)
}

func TestSegmentImportReadRefreshesInputsAndOutputs(t *testing.T) {
	fake := &fakeSegments{segments: map[string]*resend.Segment{
		"seg_1": {Id: "seg_1", Name: "imported", Object: "segment", CreatedAt: "2026-08-25T00:00:00.000Z"},
	}}
	s := testServer(t, func(c *resend.Client) { c.Segments = fake })

	read, err := s.Read(p.ReadRequest{Urn: urn("Segment", "imported"), ID: "seg_1"})
	require.NoError(t, err)
	assert.Equal(t, "seg_1", read.ID)
	assert.Equal(t, property.New("imported"), read.Inputs.Get("name"))
	assert.Equal(t, property.New("imported"), read.Properties.Get("name"))
	assert.Equal(t, property.New("2026-08-25T00:00:00.000Z"), read.Properties.Get("createdAt"))

	fake.segments["seg_1"].Name = "renamed-remotely"
	read, err = s.Read(p.ReadRequest{
		Urn: urn("Segment", "refresh"), ID: "seg_1",
		Properties: property.NewMap(map[string]property.Value{
			"name":      property.New("imported"),
			"createdAt": property.New("2026-08-25T00:00:00.000Z"),
		}),
		Inputs: property.NewMap(map[string]property.Value{"name": property.New("imported")}),
	})
	require.NoError(t, err)
	assert.Equal(t, property.New("renamed-remotely"), read.Inputs.Get("name"))
	assert.Equal(t, property.New("renamed-remotely"), read.Properties.Get("name"))
}

func TestSegmentLifecycle(t *testing.T) {
	fake := &fakeSegments{segments: map[string]*resend.Segment{}}
	s := testServer(t, func(c *resend.Client) { c.Segments = fake })

	inputs := property.NewMap(map[string]property.Value{"name": property.New("marketing")})
	created, err := s.Create(p.CreateRequest{Urn: urn("Segment", "test"), Properties: inputs})
	require.NoError(t, err)
	assert.Equal(t, "seg_1", created.ID)
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"name":      property.New("marketing"),
		"createdAt": property.New("2026-08-25T00:00:00.000Z"),
	}), created.Properties)

	updatedInputs := property.NewMap(map[string]property.Value{"name": property.New("customers")})
	diff, err := s.Diff(p.DiffRequest{
		Urn: urn("Segment", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"name": {Kind: p.Update},
	}, diff.DetailedDiff)

	updated, err := s.Update(p.UpdateRequest{
		Urn: urn("Segment", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, "customers", fake.segments["seg_1"].Name)
	assert.Equal(t, property.New("customers"), updated.Properties.Get("name"))
	assert.Equal(t, property.New("2026-08-25T00:00:00.000Z"), updated.Properties.Get("createdAt"))

	read, err := s.Read(p.ReadRequest{
		Urn: urn("Segment", "test"), ID: created.ID,
		Properties: updated.Properties, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, "seg_1", read.ID)
	assert.Equal(t, property.New("customers"), read.Properties.Get("name"))

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("Segment", "test"), ID: created.ID, Properties: updated.Properties}))
	assert.Empty(t, fake.segments)

	read, err = s.Read(p.ReadRequest{Urn: urn("Segment", "test"), ID: created.ID, Properties: updated.Properties, Inputs: updatedInputs})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID)
}

type fakeTopics struct {
	resend.TopicsSvc
	topics      map[string]*resend.Topic
	updates     []resend.UpdateTopicRequest
	nextID      int
	notFoundErr error
}

func (f *fakeTopics) CreateWithContext(ctx context.Context, params *resend.CreateTopicRequest) (*resend.CreateTopicResponse, error) {
	f.nextID++
	id := fmt.Sprintf("topic_%d", f.nextID)
	topic := &resend.Topic{
		Id:                  id,
		Name:                params.Name,
		Description:         params.Description,
		DefaultSubscription: params.DefaultSubscription,
		CreatedAt:           "2023-04-08 00:11:13.110779+00",
	}
	f.topics[id] = topic
	return &resend.CreateTopicResponse{Id: id}, nil
}

func (f *fakeTopics) GetWithContext(ctx context.Context, id string) (*resend.Topic, error) {
	topic, ok := f.topics[id]
	if !ok {
		return nil, f.missingError("Topic not found")
	}
	return topic, nil
}

func (f *fakeTopics) UpdateWithContext(ctx context.Context, id string, params *resend.UpdateTopicRequest) (*resend.UpdateTopicResponse, error) {
	topic, ok := f.topics[id]
	if !ok {
		return nil, f.missingError("Topic not found")
	}
	f.updates = append(f.updates, *params)
	if params.Name != "" {
		topic.Name = params.Name
	}
	if params.Description != "" {
		topic.Description = params.Description
	}
	return &resend.UpdateTopicResponse{Id: id}, nil
}

func (f *fakeTopics) RemoveWithContext(ctx context.Context, id string) (*resend.RemoveTopicResponse, error) {
	if _, ok := f.topics[id]; !ok {
		return nil, f.missingError("Topic not found")
	}
	delete(f.topics, id)
	return &resend.RemoveTopicResponse{Object: "topic", Id: id, Deleted: true}, nil
}

func (f *fakeTopics) missingError(msg string) error {
	if f.notFoundErr != nil {
		return f.notFoundErr
	}
	return fmt.Errorf("[ERROR]: %s", msg)
}

type fakeContactProperties struct {
	resend.ContactPropertiesSvc
	properties  map[string]*resend.ContactProperty
	updates     []resend.UpdateContactPropertyRequest
	nextID      int
	notFoundErr error
}

func (f *fakeContactProperties) CreateWithContext(ctx context.Context, params *resend.CreateContactPropertyRequest) (resend.CreateContactPropertyResponse, error) {
	f.nextID++
	id := fmt.Sprintf("prop_%d", f.nextID)
	fallback := canonicalContactPropertyFallback(params.FallbackValue)
	property := &resend.ContactProperty{
		Id:            id,
		Key:           params.Key,
		Object:        "contact_property",
		CreatedAt:     "2026-04-08 00:11:13.110779+00",
		Type:          params.Type,
		FallbackValue: fallback,
	}
	f.properties[id] = property
	return resend.CreateContactPropertyResponse{Id: id, Object: "contact_property"}, nil
}

func (f *fakeContactProperties) GetWithContext(ctx context.Context, id string) (resend.ContactProperty, error) {
	property, ok := f.properties[id]
	if !ok {
		return resend.ContactProperty{}, f.missingError("Contact property not found")
	}
	return *property, nil
}

func (f *fakeContactProperties) UpdateWithContext(ctx context.Context, params *resend.UpdateContactPropertyRequest) (resend.UpdateContactPropertyResponse, error) {
	property, ok := f.properties[params.Id]
	if !ok {
		return resend.UpdateContactPropertyResponse{}, f.missingError("Contact property not found")
	}
	f.updates = append(f.updates, *params)
	property.FallbackValue = canonicalContactPropertyFallback(params.FallbackValue)
	return resend.UpdateContactPropertyResponse{Id: params.Id, Object: "contact_property"}, nil
}

func (f *fakeContactProperties) RemoveWithContext(ctx context.Context, id string) (resend.RemoveContactPropertyResponse, error) {
	if _, ok := f.properties[id]; !ok {
		return resend.RemoveContactPropertyResponse{}, f.missingError("Contact property not found")
	}
	delete(f.properties, id)
	return resend.RemoveContactPropertyResponse{Object: "contact_property", Id: id, Deleted: true}, nil
}

func (f *fakeContactProperties) missingError(msg string) error {
	if f.notFoundErr != nil {
		return f.notFoundErr
	}
	return fmt.Errorf("[ERROR]: %s", msg)
}

func canonicalContactPropertyFallback(v any) any {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case float32:
		return float64(n)
	default:
		return v
	}
}

func TestContactPropertyLifecycleAndFallbackDiffSemantics(t *testing.T) {
	fake := &fakeContactProperties{properties: map[string]*resend.ContactProperty{}}
	s := testServer(t, func(c *resend.Client) { c.ContactProperties = fake })

	inputs := property.NewMap(map[string]property.Value{
		"key":           property.New("company_name"),
		"type":          property.New("string"),
		"fallbackValue": property.New("Acme Corp"),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("ContactProperty", "company"), Properties: inputs})
	require.NoError(t, err)
	assert.Equal(t, "prop_1", created.ID)
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"key":           property.New("company_name"),
		"type":          property.New("string"),
		"fallbackValue": property.New("Acme Corp"),
		"createdAt":     property.New("2026-04-08 00:11:13.110779+00"),
	}), created.Properties)

	updatedInputs := property.NewMap(map[string]property.Value{
		"key":           property.New("company_name"),
		"type":          property.New("string"),
		"fallbackValue": property.New("Example Company"),
	})
	diff, err := s.Diff(p.DiffRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: updatedInputs})
	require.NoError(t, err)
	assert.True(t, diff.HasChanges)
	assert.False(t, diff.DeleteBeforeReplace)
	require.Contains(t, diff.DetailedDiff, "fallbackValue")
	assert.Equal(t, p.Update, diff.DetailedDiff["fallbackValue"].Kind)
	assert.False(t, isReplacementDiff(diff.DetailedDiff["fallbackValue"]))

	updated, err := s.Update(p.UpdateRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: updatedInputs})
	require.NoError(t, err)
	require.Len(t, fake.updates, 1)
	assert.Equal(t, "Example Company", fake.updates[0].FallbackValue)
	assert.Equal(t, property.New("Example Company"), updated.Properties.Get("fallbackValue"))

	clearInputs := property.NewMap(map[string]property.Value{
		"key":  property.New("company_name"),
		"type": property.New("string"),
	})
	diff, err = s.Diff(p.DiffRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, State: updated.Properties, OldInputs: updatedInputs, Inputs: clearInputs})
	require.NoError(t, err)
	assert.True(t, diff.HasChanges)
	assert.False(t, diff.DeleteBeforeReplace)
	require.Contains(t, diff.DetailedDiff, "fallbackValue")
	assert.Equal(t, p.Delete, diff.DetailedDiff["fallbackValue"].Kind)
	assert.False(t, isReplacementDiff(diff.DetailedDiff["fallbackValue"]))

	cleared, err := s.Update(p.UpdateRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, State: updated.Properties, OldInputs: updatedInputs, Inputs: clearInputs})
	require.NoError(t, err)
	require.Len(t, fake.updates, 2)
	assert.Nil(t, fake.updates[1].FallbackValue)
	_, hasFallback := cleared.Properties.GetOk("fallbackValue")
	assert.False(t, hasFallback)
	assert.Nil(t, fake.properties[created.ID].FallbackValue)

	replaceTypeInputs := property.NewMap(map[string]property.Value{
		"key":           property.New("company_name"),
		"type":          property.New("number"),
		"fallbackValue": property.New(1.0),
	})
	diff, err = s.Diff(p.DiffRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, State: cleared.Properties, OldInputs: clearInputs, Inputs: replaceTypeInputs})
	require.NoError(t, err)
	assert.True(t, diff.HasChanges)
	assert.False(t, diff.DeleteBeforeReplace)
	require.Contains(t, diff.DetailedDiff, "type")
	assert.Equal(t, p.UpdateReplace, diff.DetailedDiff["type"].Kind)

	replaceKeyInputs := property.NewMap(map[string]property.Value{
		"key":  property.New("company_legal_name"),
		"type": property.New("string"),
	})
	diff, err = s.Diff(p.DiffRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, State: cleared.Properties, OldInputs: clearInputs, Inputs: replaceKeyInputs})
	require.NoError(t, err)
	assert.True(t, diff.HasChanges)
	assert.False(t, diff.DeleteBeforeReplace)
	require.Contains(t, diff.DetailedDiff, "key")
	assert.Equal(t, p.UpdateReplace, diff.DetailedDiff["key"].Kind)

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, Properties: cleared.Properties}))
	assert.Empty(t, fake.properties)
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("ContactProperty", "company"), ID: created.ID, Properties: updated.Properties}))
}

func TestContactPropertyNumberCanonicalizationImportAndNotFound(t *testing.T) {
	fake := &fakeContactProperties{properties: map[string]*resend.ContactProperty{
		"prop_1": {Id: "prop_1", Key: "age", Object: "contact_property", CreatedAt: "2026-04-08 00:11:13.110779+00", Type: "number", FallbackValue: float64(0)},
	}}
	s := testServer(t, func(c *resend.Client) { c.ContactProperties = fake })

	read, err := s.Read(p.ReadRequest{Urn: urn("ContactProperty", "age"), ID: "prop_1"})
	require.NoError(t, err)
	assert.Equal(t, "prop_1", read.ID)
	assert.Equal(t, property.New("age"), read.Inputs.Get("key"))
	assert.Equal(t, property.New("number"), read.Inputs.Get("type"))
	assert.Equal(t, property.New(0.0), read.Inputs.Get("fallbackValue"))

	inputs := property.NewMap(map[string]property.Value{"key": property.New("age"), "type": property.New("number"), "fallbackValue": property.New(0.0)})
	diff, err := s.Diff(p.DiffRequest{Urn: urn("ContactProperty", "age"), ID: "prop_1", State: read.Properties, OldInputs: read.Inputs, Inputs: inputs})
	require.NoError(t, err)
	assert.False(t, diff.HasChanges)

	delete(fake.properties, "prop_1")
	read, err = s.Read(p.ReadRequest{Urn: urn("ContactProperty", "age"), ID: "prop_1", Properties: read.Properties, Inputs: read.Inputs})
	require.NoError(t, err)
	assert.Empty(t, read.ID)
}

func TestContactPropertyRawDiffSemantics(t *testing.T) {
	fake := &fakeContactProperties{properties: map[string]*resend.ContactProperty{}}
	s := testServer(t, func(c *resend.Client) { c.ContactProperties = fake })

	base := property.NewMap(map[string]property.Value{
		"key":       property.New("company_name"),
		"type":      property.New("string"),
		"createdAt": property.New("2026-04-08 00:11:13.110779+00"),
	})
	withFallback := base.Set("fallbackValue", property.New("Acme Corp"))

	assertReplacement := func(name string, oldInputs, inputs property.Map, prop string) {
		t.Helper()
		diff, err := s.Diff(p.DiffRequest{Urn: urn("ContactProperty", name), ID: "prop_1", State: oldInputs, OldInputs: oldInputs, Inputs: inputs})
		require.NoError(t, err)
		assert.True(t, diff.HasChanges)
		assert.False(t, diff.DeleteBeforeReplace)
		require.Contains(t, diff.DetailedDiff, prop)
		assert.True(t, isReplacementDiff(diff.DetailedDiff[prop]))
	}
	assertInPlace := func(name string, oldInputs, inputs property.Map, prop string) p.DiffKind {
		t.Helper()
		diff, err := s.Diff(p.DiffRequest{Urn: urn("ContactProperty", name), ID: "prop_1", State: oldInputs, OldInputs: oldInputs, Inputs: inputs})
		require.NoError(t, err)
		assert.True(t, diff.HasChanges)
		assert.False(t, diff.DeleteBeforeReplace)
		require.Contains(t, diff.DetailedDiff, prop)
		assert.False(t, isReplacementDiff(diff.DetailedDiff[prop]))
		return diff.DetailedDiff[prop].Kind
	}

	assertReplacement("key-change", base, base.Set("key", property.New("company_legal_name")), "key")
	assertReplacement("type-change", base, base.Set("type", property.New("number")), "type")
	assertReplacement("empty-key", base, base.Set("key", property.New("")), "key")
	assertReplacement("empty-type", base, base.Set("type", property.New("")), "type")

	assertReplacement("unknown-key", base, base.Set("key", property.New(property.Computed)), "key")
	assertReplacement("unknown-type", base, base.Set("type", property.New(property.Computed)), "type")

	assertInPlace("unknown-fallback-from-absent", base, base.Set("fallbackValue", property.New(property.Computed)), "fallbackValue")
	assertInPlace("unknown-fallback-from-non-null", withFallback, withFallback.Set("fallbackValue", property.New(property.Computed)), "fallbackValue")
	assertInPlace("unknown-fallback-from-zero", base.Set("fallbackValue", property.New(0.0)), base.Set("fallbackValue", property.New(property.Computed)), "fallbackValue")
	assertInPlace("unknown-fallback-from-empty-string", base.Set("fallbackValue", property.New("")), base.Set("fallbackValue", property.New(property.Computed)), "fallbackValue")
	assert.Equal(t, p.Delete, assertInPlace("fallback-omission", withFallback, base, "fallbackValue"))
	assert.Equal(t, p.Update, assertInPlace("fallback-edit", withFallback, withFallback.Set("fallbackValue", property.New("Example Company")), "fallbackValue"))
}

func TestContactPropertyCheckFallbackValidation(t *testing.T) {
	s := testServer(t, func(c *resend.Client) {
		c.ContactProperties = &fakeContactProperties{properties: map[string]*resend.ContactProperty{}}
	})

	check := func(inputs property.Map) p.CheckResponse {
		t.Helper()
		resp, err := s.Check(p.CheckRequest{Urn: urn("ContactProperty", "check"), Inputs: inputs})
		require.NoError(t, err)
		return resp
	}
	known := func(typ string, fallback property.Value) property.Map {
		return property.NewMap(map[string]property.Value{
			"key":           property.New("value"),
			"type":          property.New(typ),
			"fallbackValue": fallback,
		})
	}

	for _, tc := range []struct {
		name   string
		inputs property.Map
		reason string
	}{
		{"number-string", known("number", property.New("not a number")), `contact property fallbackValue for type "number" must be a number`},
		{"string-number", known("string", property.New(1.0)), `contact property fallbackValue for type "string" must be a string`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := check(tc.inputs)
			assert.Equal(t, tc.inputs, resp.Inputs)
			assert.Equal(t, []p.CheckFailure{{Property: "fallbackValue", Reason: tc.reason}}, resp.Failures)
		})
	}

	for _, inputs := range []property.Map{
		known("string", property.New("value")),
		known("number", property.New(1.0)),
		known("custom", property.New("value")),
		known("string", property.New(property.Computed)),
		known("number", property.New(property.Computed)).Set("type", property.New(property.Computed)),
	} {
		resp := check(inputs)
		assert.Equal(t, inputs, resp.Inputs)
		assert.Empty(t, resp.Failures)
	}
}

func TestContactPropertyUnknownFallbackPreviewAccepted(t *testing.T) {
	fake := &fakeContactProperties{properties: map[string]*resend.ContactProperty{}}
	s := testServer(t, func(c *resend.Client) { c.ContactProperties = fake })

	inputs := property.NewMap(map[string]property.Value{
		"key":           property.New("company_name"),
		"type":          property.New("string"),
		"fallbackValue": property.New(property.Computed),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("ContactProperty", "preview"), Properties: inputs, DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, inputs, created.Properties.Delete("createdAt"))
	assert.Empty(t, fake.properties)

	updated, err := s.Update(p.UpdateRequest{
		Urn: urn("ContactProperty", "preview"), ID: "prop_1", DryRun: true,
		State:     property.NewMap(map[string]property.Value{"key": property.New("company_name"), "type": property.New("string"), "createdAt": property.New("2026-04-08 00:11:13.110779+00")}),
		OldInputs: property.NewMap(map[string]property.Value{"key": property.New("company_name"), "type": property.New("string")}),
		Inputs:    inputs,
	})
	require.NoError(t, err)
	assert.Equal(t, inputs, updated.Properties.Delete("createdAt"))
	assert.Empty(t, fake.updates)
}

func TestContactPropertyUnsupportedTypeDoesNotPartiallyEnforceEnum(t *testing.T) {
	fake := &fakeContactProperties{properties: map[string]*resend.ContactProperty{}}
	s := testServer(t, func(c *resend.Client) { c.ContactProperties = fake })

	_, err := s.Diff(p.DiffRequest{
		Urn:   urn("ContactProperty", "custom"),
		ID:    "prop_1",
		State: property.NewMap(map[string]property.Value{"key": property.New("custom"), "type": property.New("custom"), "createdAt": property.New("2026-04-08 00:11:13.110779+00")}),
		Inputs: property.NewMap(map[string]property.Value{
			"key":           property.New("custom"),
			"type":          property.New("custom"),
			"fallbackValue": property.New("value"),
		}),
	})
	require.NoError(t, err)
}

func TestTopicImportReadRefreshesInputsAndOutputs(t *testing.T) {
	fake := &fakeTopics{topics: map[string]*resend.Topic{
		"topic_1": {
			Id: "topic_1", Name: "imported", Description: "Imported description",
			DefaultSubscription: resend.DefaultSubscriptionOptOut, CreatedAt: "2023-04-08 00:11:13.110779+00",
		},
	}}
	s := testServer(t, func(c *resend.Client) { c.Topics = fake })

	read, err := s.Read(p.ReadRequest{Urn: urn("Topic", "imported"), ID: "topic_1"})
	require.NoError(t, err)
	assert.Equal(t, "topic_1", read.ID)
	assert.Equal(t, property.New("imported"), read.Inputs.Get("name"))
	assert.Equal(t, property.New("opt_out"), read.Inputs.Get("defaultSubscription"))
	assert.Equal(t, property.New("Imported description"), read.Inputs.Get("description"))
	assert.Equal(t, property.New("2023-04-08 00:11:13.110779+00"), read.Properties.Get("createdAt"))

	fake.topics["topic_1"].Name = "renamed-remotely"
	fake.topics["topic_1"].Description = "Refreshed description"
	read, err = s.Read(p.ReadRequest{
		Urn: urn("Topic", "refresh"), ID: "topic_1",
		Properties: property.NewMap(map[string]property.Value{
			"name":                property.New("imported"),
			"defaultSubscription": property.New("opt_out"),
			"description":         property.New("Imported description"),
			"createdAt":           property.New("2023-04-08 00:11:13.110779+00"),
		}),
		Inputs: property.NewMap(map[string]property.Value{
			"name":                property.New("imported"),
			"defaultSubscription": property.New("opt_out"),
			"description":         property.New("Imported description"),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, property.New("renamed-remotely"), read.Inputs.Get("name"))
	assert.Equal(t, property.New("Refreshed description"), read.Inputs.Get("description"))
	assert.Equal(t, property.New("Refreshed description"), read.Properties.Get("description"))
}

func TestTopicEmptyRemoteDescriptionCanonicalizesToNil(t *testing.T) {
	fake := &fakeTopics{topics: map[string]*resend.Topic{
		"topic_1": {
			Id: "topic_1", Name: "empty", Description: "",
			DefaultSubscription: resend.DefaultSubscriptionOptIn, CreatedAt: "2023-04-08 00:11:13.110779+00",
		},
	}}
	s := testServer(t, func(c *resend.Client) { c.Topics = fake })

	read, err := s.Read(p.ReadRequest{
		Urn: urn("Topic", "empty"), ID: "topic_1",
		Properties: property.NewMap(map[string]property.Value{
			"name":                property.New("empty"),
			"defaultSubscription": property.New("opt_in"),
			"description":         property.New(""),
			"createdAt":           property.New("2023-04-08 00:11:13.110779+00"),
		}),
		Inputs: property.NewMap(map[string]property.Value{
			"name":                property.New("empty"),
			"defaultSubscription": property.New("opt_in"),
			"description":         property.New(""),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, "topic_1", read.ID)
	_, hasInputDescription := read.Inputs.GetOk("description")
	assert.False(t, hasInputDescription)
	_, hasOutputDescription := read.Properties.GetOk("description")
	assert.False(t, hasOutputDescription)
}

func TestTopicDescriptionDiffSemantics(t *testing.T) {
	fake := &fakeTopics{topics: map[string]*resend.Topic{}}
	s := testServer(t, func(c *resend.Client) { c.Topics = fake })

	inputs := property.NewMap(map[string]property.Value{
		"name":                property.New("Product Updates"),
		"defaultSubscription": property.New("opt_in"),
		"description":         property.New("Weekly product updates"),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("Topic", "description"), Properties: inputs})
	require.NoError(t, err)

	mutatedInputs := property.NewMap(map[string]property.Value{
		"name":                property.New("Product Updates"),
		"defaultSubscription": property.New("opt_in"),
		"description":         property.New("Monthly product updates"),
	})
	diff, err := s.Diff(p.DiffRequest{
		Urn: urn("Topic", "description"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: mutatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"description": {Kind: p.Update, InputDiff: true},
	}, diff.DetailedDiff)

	_, err = s.Update(p.UpdateRequest{
		Urn: urn("Topic", "description"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: mutatedInputs,
	})
	require.NoError(t, err)
	require.Len(t, fake.updates, 1)
	assert.Equal(t, "Monthly product updates", fake.updates[0].Description)

	clearedInputs := property.NewMap(map[string]property.Value{
		"name":                property.New("Product Updates"),
		"defaultSubscription": property.New("opt_in"),
	})
	diff, err = s.Diff(p.DiffRequest{
		Urn: urn("Topic", "description"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: clearedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"description": {Kind: p.UpdateReplace, InputDiff: true},
	}, diff.DetailedDiff)

	emptyInputs := property.NewMap(map[string]property.Value{
		"name":                property.New("Product Updates"),
		"defaultSubscription": property.New("opt_in"),
		"description":         property.New(""),
	})
	diff, err = s.Diff(p.DiffRequest{
		Urn: urn("Topic", "description"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: emptyInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"description": {Kind: p.UpdateReplace, InputDiff: true},
	}, diff.DetailedDiff)
}

func TestTopicLifecycle(t *testing.T) {
	description := "Weekly product updates"
	fake := &fakeTopics{topics: map[string]*resend.Topic{}}
	s := testServer(t, func(c *resend.Client) { c.Topics = fake })

	inputs := property.NewMap(map[string]property.Value{
		"name":                property.New("Product Updates"),
		"defaultSubscription": property.New("opt_in"),
		"description":         property.New(description),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn("Topic", "test"), Properties: inputs})
	require.NoError(t, err)
	assert.Equal(t, "topic_1", created.ID)
	assert.Equal(t, property.NewMap(map[string]property.Value{
		"name":                property.New("Product Updates"),
		"defaultSubscription": property.New("opt_in"),
		"description":         property.New(description),
		"createdAt":           property.New("2023-04-08 00:11:13.110779+00"),
	}), created.Properties)

	updatedInputs := property.NewMap(map[string]property.Value{
		"name":                property.New("Marketing Updates"),
		"defaultSubscription": property.New("opt_in"),
		"description":         property.New("Monthly marketing updates"),
	})
	diff, err := s.Diff(p.DiffRequest{
		Urn: urn("Topic", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]p.PropertyDiff{
		"name":        {Kind: p.Update, InputDiff: true},
		"description": {Kind: p.Update, InputDiff: true},
	}, diff.DetailedDiff)

	replaceInputs := property.NewMap(map[string]property.Value{
		"name":                property.New("Marketing Updates"),
		"defaultSubscription": property.New("opt_out"),
		"description":         property.New("Monthly marketing updates"),
	})
	diff, err = s.Diff(p.DiffRequest{
		Urn: urn("Topic", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: replaceInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, p.UpdateReplace, diff.DetailedDiff["defaultSubscription"].Kind)

	updated, err := s.Update(p.UpdateRequest{
		Urn: urn("Topic", "test"), ID: created.ID,
		State: created.Properties, OldInputs: inputs, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, "Marketing Updates", fake.topics["topic_1"].Name)
	assert.Equal(t, "Monthly marketing updates", fake.topics["topic_1"].Description)
	assert.Equal(t, resend.DefaultSubscriptionOptIn, fake.topics["topic_1"].DefaultSubscription)
	assert.Equal(t, property.New("Marketing Updates"), updated.Properties.Get("name"))
	assert.Equal(t, property.New("Monthly marketing updates"), updated.Properties.Get("description"))

	read, err := s.Read(p.ReadRequest{
		Urn: urn("Topic", "test"), ID: created.ID,
		Properties: updated.Properties, Inputs: updatedInputs,
	})
	require.NoError(t, err)
	assert.Equal(t, "topic_1", read.ID)
	assert.Equal(t, property.New("Marketing Updates"), read.Properties.Get("name"))

	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("Topic", "test"), ID: created.ID, Properties: updated.Properties}))
	assert.Empty(t, fake.topics)

	read, err = s.Read(p.ReadRequest{Urn: urn("Topic", "test"), ID: created.ID, Properties: updated.Properties, Inputs: updatedInputs})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID)
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn("Topic", "test"), ID: created.ID, Properties: updated.Properties}))
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

	segmentServer := testServer(t, func(c *resend.Client) {
		c.Segments = &fakeSegments{segments: map[string]*resend.Segment{}, notFoundErr: notFound}
	})
	read, err := segmentServer.Read(p.ReadRequest{Urn: urn("Segment", "missing"), ID: "seg_missing"})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID, "Segment should be removed from state when Resend returns a typed 404")
	require.NoError(t, segmentServer.Delete(p.DeleteRequest{
		Urn: urn("Segment", "missing"), ID: "seg_missing",
		Properties: property.NewMap(map[string]property.Value{"name": property.New("marketing"), "createdAt": property.New("now")}),
	}))

	topicServer := testServer(t, func(c *resend.Client) {
		c.Topics = &fakeTopics{topics: map[string]*resend.Topic{}, notFoundErr: notFound}
	})
	read, err = topicServer.Read(p.ReadRequest{Urn: urn("Topic", "missing"), ID: "topic_missing"})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID, "Topic should be removed from state when Resend returns a typed 404")
	require.NoError(t, topicServer.Delete(p.DeleteRequest{
		Urn: urn("Topic", "missing"), ID: "topic_missing",
		Properties: property.NewMap(map[string]property.Value{
			"name": property.New("marketing"), "defaultSubscription": property.New("opt_in"), "createdAt": property.New("now"),
		}),
	}))

	webhookServer := testServer(t, func(c *resend.Client) {
		c.Webhooks = &fakeWebhooks{hooks: map[string]*resend.Webhook{}, notFoundErr: notFound}
	})
	read, err = webhookServer.Read(p.ReadRequest{Urn: urn("Webhook", "missing"), ID: "wh_missing"})
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

	segmentServer := testServer(t, func(c *resend.Client) { c.Segments = &fakeSegments{segments: map[string]*resend.Segment{}} })
	read, err = segmentServer.Read(p.ReadRequest{Urn: urn("Segment", "missing"), ID: "seg_missing"})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID, "Segment should be removed from state when Resend reports not found")

	topicServer := testServer(t, func(c *resend.Client) { c.Topics = &fakeTopics{topics: map[string]*resend.Topic{}} })
	read, err = topicServer.Read(p.ReadRequest{Urn: urn("Topic", "missing"), ID: "topic_missing"})
	require.NoError(t, err)
	assert.Equal(t, "", read.ID, "Topic should be removed from state when Resend reports not found")

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
