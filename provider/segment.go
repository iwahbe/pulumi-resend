package provider

import (
	"context"
	"fmt"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type Segment struct{}

type SegmentArgs struct {
	Name string `pulumi:"name"`
}

type SegmentState struct {
	SegmentArgs
	CreatedAt string `pulumi:"createdAt"`
}

func (s *Segment) Annotate(a infer.Annotator) {
	a.Describe(&s, "A Resend segment for grouping contacts. This resource models only declarative segment CRUD.")
}

func (s *SegmentArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Name, "The segment name.")
}

func (s *SegmentState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "The timestamp when Resend created the segment.")
}

func (*Segment) Create(
	ctx context.Context, req infer.CreateRequest[SegmentArgs],
) (infer.CreateResponse[SegmentState], error) {
	if req.DryRun {
		return infer.CreateResponse[SegmentState]{Output: SegmentState{SegmentArgs: req.Inputs}}, nil
	}
	client := getClient(ctx)
	resp, err := client.Segments.CreateWithContext(ctx, &resend.CreateSegmentRequest{Name: req.Inputs.Name})
	if err != nil {
		return infer.CreateResponse[SegmentState]{}, fmt.Errorf("creating segment %q: %w", req.Inputs.Name, err)
	}
	state := SegmentState{SegmentArgs: req.Inputs}
	remote, err := client.Segments.GetWithContext(ctx, resp.Id)
	if err != nil {
		return infer.CreateResponse[SegmentState]{ID: resp.Id, Output: state},
			infer.ResourceInitFailedError{Reasons: []string{fmt.Sprintf("reading segment after create: %s", err)}}
	}
	state.Name = remote.Name
	state.CreatedAt = remote.CreatedAt
	return infer.CreateResponse[SegmentState]{ID: resp.Id, Output: state}, nil
}

func (*Segment) Diff(
	ctx context.Context, req infer.DiffRequest[SegmentArgs, SegmentState],
) (infer.DiffResponse, error) {
	diff := map[string]p.PropertyDiff{}
	if req.State.Name != req.Inputs.Name {
		diff["name"] = p.PropertyDiff{Kind: p.Update, InputDiff: true}
	}
	return p.DiffResponse{HasChanges: len(diff) > 0, DetailedDiff: diff}, nil
}

func (*Segment) Update(
	ctx context.Context, req infer.UpdateRequest[SegmentArgs, SegmentState],
) (infer.UpdateResponse[SegmentState], error) {
	state := req.State
	state.SegmentArgs = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[SegmentState]{Output: state}, nil
	}
	client := getClient(ctx)
	if _, err := client.Segments.UpdateWithContext(ctx, req.ID, &resend.UpdateSegmentRequest{Name: req.Inputs.Name}); err != nil {
		return infer.UpdateResponse[SegmentState]{}, fmt.Errorf("updating segment %q: %w", req.ID, err)
	}
	remote, err := client.Segments.GetWithContext(ctx, req.ID)
	if err != nil {
		return infer.UpdateResponse[SegmentState]{}, fmt.Errorf("reading segment %q after update: %w", req.ID, err)
	}
	state.Name = remote.Name
	state.CreatedAt = remote.CreatedAt
	return infer.UpdateResponse[SegmentState]{Output: state}, nil
}

func (*Segment) Read(
	ctx context.Context, req infer.ReadRequest[SegmentArgs, SegmentState],
) (infer.ReadResponse[SegmentArgs, SegmentState], error) {
	remote, err := getClient(ctx).Segments.GetWithContext(ctx, req.ID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[SegmentArgs, SegmentState]{}, nil
		}
		return infer.ReadResponse[SegmentArgs, SegmentState]{}, err
	}
	inputs := SegmentArgs{Name: remote.Name}
	state := SegmentState{SegmentArgs: inputs, CreatedAt: remote.CreatedAt}
	return infer.ReadResponse[SegmentArgs, SegmentState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

func (*Segment) Delete(ctx context.Context, req infer.DeleteRequest[SegmentState]) (infer.DeleteResponse, error) {
	if _, err := getClient(ctx).Segments.RemoveWithContext(ctx, req.ID); err != nil && !isNotFound(err) {
		return infer.DeleteResponse{}, fmt.Errorf("deleting segment %q: %w", req.ID, err)
	}
	return infer.DeleteResponse{}, nil
}
