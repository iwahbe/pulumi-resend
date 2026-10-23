package provider

import (
	"context"
	"encoding/json"
	"fmt"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// GetDomainId looks up a Resend domain ID by its domain name.
type GetDomainId struct{}

type GetDomainIdArgs struct {
	Name string `pulumi:"name"`
}

type GetDomainIdResult struct {
	DomainId string `pulumi:"domainId"`
}

func (g *GetDomainId) Annotate(a infer.Annotator) {
	a.Describe(&g, "Looks up a Resend domain ID by domain name.")
}

func (g *GetDomainIdArgs) Annotate(a infer.Annotator) {
	a.Describe(&g.Name, "The domain name to look up. It is matched exactly as returned by Resend; no case, whitespace, or trailing-dot normalization is applied.")
}

func (g *GetDomainIdResult) Annotate(a infer.Annotator) {
	a.Describe(&g.DomainId, "The Resend domain ID.")
}

// exposeGetDomainId works around pulumi-go-provider v1.7.0 reserving top-level
// `id` fields in inferred structs. The function remains inferred, while this adapter
// exposes its sole result as `id` in both the schema and invoke response.
func exposeGetDomainId(prov p.Provider) (p.Provider, error) {
	getSchema := prov.GetSchema
	prov.GetSchema = func(ctx context.Context, req p.GetSchemaRequest) (p.GetSchemaResponse, error) {
		resp, err := getSchema(ctx, req)
		if err != nil {
			return p.GetSchemaResponse{}, err
		}
		var schema map[string]any
		if err := json.Unmarshal([]byte(resp.Schema), &schema); err != nil {
			return p.GetSchemaResponse{}, fmt.Errorf("decoding inferred provider schema: %w", err)
		}
		functions, ok := schema["functions"].(map[string]any)
		if !ok {
			return p.GetSchemaResponse{}, fmt.Errorf("inferred provider schema has no functions")
		}
		function, ok := functions["resend:index:getDomainId"].(map[string]any)
		if !ok {
			return p.GetSchemaResponse{}, fmt.Errorf("inferred provider schema is missing resend:index:getDomainId")
		}
		outputs, ok := function["outputs"].(map[string]any)
		if !ok {
			return p.GetSchemaResponse{}, fmt.Errorf("inferred getDomainId schema has no outputs")
		}
		properties, ok := outputs["properties"].(map[string]any)
		if !ok || properties["domainId"] == nil {
			return p.GetSchemaResponse{}, fmt.Errorf("inferred getDomainId schema is missing domainId output")
		}
		properties["id"] = properties["domainId"]
		delete(properties, "domainId")
		outputs["required"] = []string{"id"}
		encoded, err := json.Marshal(schema)
		if err != nil {
			return p.GetSchemaResponse{}, fmt.Errorf("encoding provider schema: %w", err)
		}
		resp.Schema = string(encoded)
		return resp, nil
	}

	invoke := prov.Invoke
	prov.Invoke = func(ctx context.Context, req p.InvokeRequest) (p.InvokeResponse, error) {
		resp, err := invoke(ctx, req)
		if err != nil || req.Token != "resend:index:getDomainId" {
			return resp, err
		}
		resp.Return = resp.Return.Set("id", resp.Return.Get("domainId")).Delete("domainId")
		return resp, nil
	}
	return prov, nil
}

func (*GetDomainId) Invoke(
	ctx context.Context, req infer.FunctionRequest[GetDomainIdArgs],
) (infer.FunctionResponse[GetDomainIdResult], error) {
	resp, err := getClient(ctx).Domains.ListWithContext(ctx)
	if err != nil {
		return infer.FunctionResponse[GetDomainIdResult]{}, fmt.Errorf("listing domains while looking up domain %q: %w", req.Input.Name, err)
	}

	var matches []string
	for _, domain := range resp.Data {
		if domain.Name == req.Input.Name {
			matches = append(matches, domain.Id)
		}
	}
	switch len(matches) {
	case 0:
		return infer.FunctionResponse[GetDomainIdResult]{}, fmt.Errorf("no Resend domain found with name %q", req.Input.Name)
	case 1:
		return infer.FunctionResponse[GetDomainIdResult]{Output: GetDomainIdResult{DomainId: matches[0]}}, nil
	default:
		return infer.FunctionResponse[GetDomainIdResult]{}, fmt.Errorf("multiple Resend domains found with name %q; lookup is ambiguous", req.Input.Name)
	}
}
