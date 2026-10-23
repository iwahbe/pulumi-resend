package provider

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi-go-provider/infer"
)

// GetDomainId looks up a Resend domain ID by its domain name.
type GetDomainId struct{}

type GetDomainIdArgs struct {
	Name string `pulumi:"name"`
}

type GetDomainIdResult struct {
	ID string `pulumi:"id"`
}

func (g *GetDomainId) Annotate(a infer.Annotator) {
	a.Describe(&g, "Looks up a Resend domain ID by domain name.")
}

func (g *GetDomainIdArgs) Annotate(a infer.Annotator) {
	a.Describe(&g.Name, "The domain name to look up. It is matched exactly as returned by Resend; no case, whitespace, or trailing-dot normalization is applied.")
}

func (g *GetDomainIdResult) Annotate(a infer.Annotator) {
	a.Describe(&g.ID, "The Resend domain ID.")
}

func (*GetDomainId) Invoke(
	ctx context.Context, req infer.FunctionRequest[GetDomainIdArgs],
) (infer.FunctionResponse[GetDomainIdResult], error) {
	if req.Input.Name == "" {
		return infer.FunctionResponse[GetDomainIdResult]{}, fmt.Errorf("domain name must not be empty")
	}

	resp, err := getClient(ctx).Domains.ListWithContext(ctx)
	if err != nil {
		return infer.FunctionResponse[GetDomainIdResult]{}, fmt.Errorf("listing domains while looking up domain %q: %w", req.Input.Name, err)
	}
	if resp.HasMore {
		return infer.FunctionResponse[GetDomainIdResult]{}, fmt.Errorf("listing domains while looking up domain %q: result is paginated; cannot safely determine a unique domain ID", req.Input.Name)
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
		return infer.FunctionResponse[GetDomainIdResult]{Output: GetDomainIdResult{ID: matches[0]}}, nil
	default:
		return infer.FunctionResponse[GetDomainIdResult]{}, fmt.Errorf("multiple Resend domains found with name %q; lookup is ambiguous", req.Input.Name)
	}
}
