package provider

import (
	"context"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

type wireSecretRepro struct{}

type wireSecretReproArgs struct {
	Name string `pulumi:"name"`
}

type wireSecretReproState struct {
	wireSecretReproArgs
	Secret string `pulumi:"secret"`
}

func (*wireSecretRepro) WireDependencies(f infer.FieldSelector, args *wireSecretReproArgs, state *wireSecretReproState) {
	f.OutputField(&state.Secret).AlwaysSecret()
}

func (*wireSecretRepro) Create(context.Context, infer.CreateRequest[wireSecretReproArgs]) (infer.CreateResponse[wireSecretReproState], error) {
	return infer.CreateResponse[wireSecretReproState]{
		ID:     "id",
		Output: wireSecretReproState{wireSecretReproArgs: wireSecretReproArgs{Name: "created"}, Secret: "created-secret"},
	}, nil
}

func (*wireSecretRepro) Update(context.Context, infer.UpdateRequest[wireSecretReproArgs, wireSecretReproState]) (infer.UpdateResponse[wireSecretReproState], error) {
	return infer.UpdateResponse[wireSecretReproState]{
		Output: wireSecretReproState{wireSecretReproArgs: wireSecretReproArgs{Name: "updated"}, Secret: "updated-secret"},
	}, nil
}

func (*wireSecretRepro) Read(context.Context, infer.ReadRequest[wireSecretReproArgs, wireSecretReproState]) (infer.ReadResponse[wireSecretReproArgs, wireSecretReproState], error) {
	return infer.ReadResponse[wireSecretReproArgs, wireSecretReproState]{
		ID:     "id",
		Inputs: wireSecretReproArgs{Name: "read"},
		State:  wireSecretReproState{wireSecretReproArgs: wireSecretReproArgs{Name: "read"}, Secret: ""},
	}, nil
}

func TestInferWireDependenciesAlwaysSecretRawResponses(t *testing.T) {
	prov, err := infer.NewProviderBuilder().
		WithNamespace("resend").
		WithModuleMap(map[tokens.ModuleName]tokens.ModuleName{"provider": "index"}).
		WithResources(infer.Resource(&wireSecretRepro{})).
		Build()
	require.NoError(t, err)
	s, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(prov))
	require.NoError(t, err)

	resourceURN := urn("wireSecretRepro", "test")
	inputs := property.NewMap(map[string]property.Value{"name": property.New("input")})

	created, err := s.Create(p.CreateRequest{Urn: resourceURN, Properties: inputs})
	require.NoError(t, err)
	require.Equal(t, property.New("created-secret").WithSecret(true), created.Properties.Get("secret"))

	updated, err := s.Update(p.UpdateRequest{Urn: resourceURN, ID: created.ID, State: created.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, property.New("updated-secret").WithSecret(true), updated.Properties.Get("secret"))

	refreshed, err := s.Read(p.ReadRequest{Urn: resourceURN, ID: created.ID, Properties: updated.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, property.New("").WithSecret(true), refreshed.Properties.Get("secret"),
		"Read preserves existing secret markers from prior state")

	imported, err := s.Read(p.ReadRequest{Urn: resourceURN, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, property.New(""), imported.Properties.Get("secret"),
		"pulumi-go-provider/infer v1.6.0 does not apply AlwaysSecret to import-like raw Read responses")
}
