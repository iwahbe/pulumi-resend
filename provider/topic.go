package provider

import (
	"context"
	"fmt"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type Topic struct{}

type TopicDefaultSubscription string

const (
	TopicDefaultSubscriptionOptIn  TopicDefaultSubscription = "opt_in"
	TopicDefaultSubscriptionOptOut TopicDefaultSubscription = "opt_out"
)

func (TopicDefaultSubscription) Values() []infer.EnumValue[TopicDefaultSubscription] {
	return []infer.EnumValue[TopicDefaultSubscription]{
		{Value: TopicDefaultSubscriptionOptIn, Description: "Subscribe new contacts to this topic by default."},
		{Value: TopicDefaultSubscriptionOptOut, Description: "Do not subscribe new contacts to this topic by default."},
	}
}

type TopicArgs struct {
	Name                string                   `pulumi:"name"`
	DefaultSubscription TopicDefaultSubscription `pulumi:"defaultSubscription" provider:"replaceOnChanges"`
	Description         *string                  `pulumi:"description,optional"`
}

type TopicState struct {
	TopicArgs
	CreatedAt string `pulumi:"createdAt"`
}

func (t *Topic) Annotate(a infer.Annotator) {
	a.Describe(&t, "A Resend broadcast topic used to manage contact subscription preferences.")
}

func (t *TopicArgs) Annotate(a infer.Annotator) {
	a.Describe(&t.Name, "The topic name.")
	a.Describe(&t.DefaultSubscription, "The default subscription preference for new contacts, either `opt_in` or `opt_out`. Resend does not allow this value to change after creation, so changes replace the topic.")
	a.Describe(&t.Description, "An optional topic description. Non-empty values are sent to Resend on create and update. Because resend-go v4.2.0 omits empty `description` values from update requests, clearing an existing description replaces the topic.")
}

func (t *TopicState) Annotate(a infer.Annotator) {
	a.Describe(&t.CreatedAt, "The timestamp when Resend created the topic, as returned by the Resend API `created_at` string in `YYYY-MM-DD HH:MM:SS.ffffff+00` format (for example, `2023-04-08 00:11:13.110779+00`).")
}

func (*Topic) Create(
	ctx context.Context, req infer.CreateRequest[TopicArgs],
) (infer.CreateResponse[TopicState], error) {
	if req.DryRun {
		return infer.CreateResponse[TopicState]{Output: TopicState{TopicArgs: req.Inputs}}, nil
	}
	client := getClient(ctx)
	resp, err := client.Topics.CreateWithContext(ctx, &resend.CreateTopicRequest{
		Name:                req.Inputs.Name,
		DefaultSubscription: resend.DefaultSubscription(req.Inputs.DefaultSubscription),
		Description:         deref(req.Inputs.Description),
	})
	if err != nil {
		return infer.CreateResponse[TopicState]{}, fmt.Errorf("creating topic %q: %w", req.Inputs.Name, err)
	}
	state := TopicState{TopicArgs: req.Inputs}
	remote, err := client.Topics.GetWithContext(ctx, resp.Id)
	if err != nil {
		return infer.CreateResponse[TopicState]{ID: resp.Id, Output: state},
			infer.ResourceInitFailedError{Reasons: []string{fmt.Sprintf("reading topic after create: %s", err)}}
	}
	refreshTopicState(&state, remote)
	return infer.CreateResponse[TopicState]{ID: resp.Id, Output: state}, nil
}

func (*Topic) Diff(
	ctx context.Context, req infer.DiffRequest[TopicArgs, TopicState],
) (infer.DiffResponse, error) {
	diff := map[string]p.PropertyDiff{}
	if req.State.Name != req.Inputs.Name {
		diff["name"] = p.PropertyDiff{Kind: p.Update, InputDiff: true}
	}
	if req.State.DefaultSubscription != req.Inputs.DefaultSubscription {
		diff["defaultSubscription"] = p.PropertyDiff{Kind: p.UpdateReplace, InputDiff: true}
	}
	oldDescription := deref(req.State.Description)
	newDescription := deref(req.Inputs.Description)
	if oldDescription != newDescription {
		kind := p.Update
		if oldDescription != "" && newDescription == "" {
			kind = p.UpdateReplace
		}
		diff["description"] = p.PropertyDiff{Kind: kind, InputDiff: true}
	}
	return p.DiffResponse{HasChanges: len(diff) > 0, DetailedDiff: diff}, nil
}

func (*Topic) Update(
	ctx context.Context, req infer.UpdateRequest[TopicArgs, TopicState],
) (infer.UpdateResponse[TopicState], error) {
	state := req.State
	state.TopicArgs = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[TopicState]{Output: state}, nil
	}
	client := getClient(ctx)
	_, err := client.Topics.UpdateWithContext(ctx, req.ID, &resend.UpdateTopicRequest{
		Name:        req.Inputs.Name,
		Description: deref(req.Inputs.Description),
	})
	if err != nil {
		return infer.UpdateResponse[TopicState]{}, fmt.Errorf("updating topic %q: %w", req.ID, err)
	}
	remote, err := client.Topics.GetWithContext(ctx, req.ID)
	if err != nil {
		return infer.UpdateResponse[TopicState]{}, fmt.Errorf("reading topic %q after update: %w", req.ID, err)
	}
	refreshTopicState(&state, remote)
	return infer.UpdateResponse[TopicState]{Output: state}, nil
}

func (*Topic) Read(
	ctx context.Context, req infer.ReadRequest[TopicArgs, TopicState],
) (infer.ReadResponse[TopicArgs, TopicState], error) {
	remote, err := getClient(ctx).Topics.GetWithContext(ctx, req.ID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[TopicArgs, TopicState]{}, nil
		}
		return infer.ReadResponse[TopicArgs, TopicState]{}, err
	}
	inputs := req.Inputs
	inputs.Name = remote.Name
	inputs.DefaultSubscription = TopicDefaultSubscription(remote.DefaultSubscription)
	if remote.Description == "" {
		inputs.Description = nil
	} else {
		inputs.Description = &remote.Description
	}
	state := TopicState{TopicArgs: inputs}
	refreshTopicState(&state, remote)
	return infer.ReadResponse[TopicArgs, TopicState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

func (*Topic) Delete(ctx context.Context, req infer.DeleteRequest[TopicState]) (infer.DeleteResponse, error) {
	if _, err := getClient(ctx).Topics.RemoveWithContext(ctx, req.ID); err != nil && !isNotFound(err) {
		return infer.DeleteResponse{}, fmt.Errorf("deleting topic %q: %w", req.ID, err)
	}
	return infer.DeleteResponse{}, nil
}

func refreshTopicState(state *TopicState, remote *resend.Topic) {
	state.Name = remote.Name
	state.DefaultSubscription = TopicDefaultSubscription(remote.DefaultSubscription)
	if remote.Description == "" {
		state.Description = nil
	} else {
		state.Description = &remote.Description
	}
	state.CreatedAt = remote.CreatedAt
}
