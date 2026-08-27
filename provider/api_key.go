package provider

import (
	"context"
	"fmt"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type ApiKey struct{}

type ApiKeyArgs struct {
	Name       string  `pulumi:"name"`
	Permission *string `pulumi:"permission,optional"`
	DomainId   *string `pulumi:"domainId,optional"`
}

type ApiKeyState struct {
	ApiKeyArgs
	Token string `pulumi:"token" provider:"secret"`
}

func (k *ApiKey) Annotate(a infer.Annotator) {
	a.Describe(&k, "A Resend API key. The `token` output is only available from the Resend API at "+
		"creation time; it cannot be recovered later.")
}

func (k *ApiKeyArgs) Annotate(a infer.Annotator) {
	a.Describe(&k.Name, "The API key name. Maximum 50 characters.")
	a.Describe(&k.Permission, "`full_access` or `sending_access`.")
	a.Describe(&k.DomainId, "Restrict a `sending_access` key to a single domain.")
}

func (k *ApiKeyState) Annotate(a infer.Annotator) {
	a.Describe(&k.Token, "The API key credential. Only returned by Resend at creation time; empty after import.")
}

func (*ApiKey) WireDependencies(f infer.FieldSelector, args *ApiKeyArgs, state *ApiKeyState) {
	f.OutputField(&state.Token).AlwaysSecret()
}

func (*ApiKey) Create(
	ctx context.Context, req infer.CreateRequest[ApiKeyArgs],
) (infer.CreateResponse[ApiKeyState], error) {
	if req.DryRun {
		return infer.CreateResponse[ApiKeyState]{Output: ApiKeyState{ApiKeyArgs: req.Inputs}}, nil
	}
	resp, err := getClient(ctx).ApiKeys.CreateWithContext(ctx, &resend.CreateApiKeyRequest{
		Name:       req.Inputs.Name,
		Permission: deref(req.Inputs.Permission),
		DomainId:   deref(req.Inputs.DomainId),
	})
	if err != nil {
		return infer.CreateResponse[ApiKeyState]{}, fmt.Errorf("creating API key %q: %w", req.Inputs.Name, err)
	}
	return infer.CreateResponse[ApiKeyState]{
		ID:     resp.Id,
		Output: ApiKeyState{ApiKeyArgs: req.Inputs, Token: resp.Token},
	}, nil
}

func (*ApiKey) Diff(
	ctx context.Context, req infer.DiffRequest[ApiKeyArgs, ApiKeyState],
) (infer.DiffResponse, error) {
	diff := map[string]p.PropertyDiff{}
	if req.State.Name != req.Inputs.Name {
		diff["name"] = p.PropertyDiff{Kind: p.Update, InputDiff: true}
	}
	// Only the name can be changed via the update API.
	if !ptrEq(req.State.Permission, req.Inputs.Permission) {
		diff["permission"] = p.PropertyDiff{Kind: p.UpdateReplace, InputDiff: true}
	}
	if !ptrEq(req.State.DomainId, req.Inputs.DomainId) {
		diff["domainId"] = p.PropertyDiff{Kind: p.UpdateReplace, InputDiff: true}
	}
	return p.DiffResponse{HasChanges: len(diff) > 0, DetailedDiff: diff}, nil
}

func (*ApiKey) Update(
	ctx context.Context, req infer.UpdateRequest[ApiKeyArgs, ApiKeyState],
) (infer.UpdateResponse[ApiKeyState], error) {
	state := req.State
	state.ApiKeyArgs = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[ApiKeyState]{Output: state}, nil
	}
	_, err := getClient(ctx).ApiKeys.UpdateWithContext(ctx, req.ID, &resend.UpdateApiKeyRequest{
		Name: req.Inputs.Name,
	})
	if err != nil {
		return infer.UpdateResponse[ApiKeyState]{}, fmt.Errorf("updating API key %q: %w", req.ID, err)
	}
	return infer.UpdateResponse[ApiKeyState]{Output: state}, nil
}

func (*ApiKey) Read(
	ctx context.Context, req infer.ReadRequest[ApiKeyArgs, ApiKeyState],
) (infer.ReadResponse[ApiKeyArgs, ApiKeyState], error) {
	client := getClient(ctx)
	var after *string
	for {
		resp, err := client.ApiKeys.ListWithOptions(ctx, &resend.ListOptions{After: after})
		if err != nil {
			return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{}, fmt.Errorf("listing API keys: %w", err)
		}
		for _, key := range resp.Data {
			if key.Id != req.ID {
				continue
			}
			inputs := req.Inputs
			inputs.Name = key.Name
			state := req.State
			state.ApiKeyArgs = inputs
			return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{ID: req.ID, Inputs: inputs, State: state}, nil
		}
		if !resp.HasMore || len(resp.Data) == 0 {
			// Not present: the key was deleted.
			return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{}, nil
		}
		after = &resp.Data[len(resp.Data)-1].Id
	}
}

func (*ApiKey) Delete(ctx context.Context, req infer.DeleteRequest[ApiKeyState]) (infer.DeleteResponse, error) {
	if _, err := getClient(ctx).ApiKeys.RemoveWithContext(ctx, req.ID); err != nil && !isNotFound(err) {
		return infer.DeleteResponse{}, fmt.Errorf("deleting API key %q: %w", req.ID, err)
	}
	return infer.DeleteResponse{}, nil
}
