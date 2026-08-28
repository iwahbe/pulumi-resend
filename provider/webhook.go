package provider

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type Webhook struct{}

type WebhookEvent string

func (WebhookEvent) Values() []infer.EnumValue[WebhookEvent] {
	return []infer.EnumValue[WebhookEvent]{
		{Value: resend.EventEmailSent},
		{Value: resend.EventEmailDelivered},
		{Value: resend.EventEmailDeliveryDelayed},
		{Value: resend.EventEmailComplained},
		{Value: resend.EventEmailBounced},
		{Value: resend.EventEmailOpened},
		{Value: resend.EventEmailClicked},
		{Value: resend.EventEmailReceived},
		{Value: resend.EventEmailFailed},
		{Value: resend.EventEmailScheduled},
		{Value: resend.EventEmailSuppressed},
		{Value: resend.EventContactCreated},
		{Value: resend.EventContactUpdated},
		{Value: resend.EventContactDeleted},
		{Value: resend.EventDomainCreated},
		{Value: resend.EventDomainUpdated},
		{Value: resend.EventDomainDeleted},
	}
}

type WebhookArgs struct {
	Endpoint string         `pulumi:"endpoint"`
	Events   []WebhookEvent `pulumi:"events"`
}

type WebhookState struct {
	WebhookArgs
	SigningSecret string `pulumi:"signingSecret" provider:"secret"`
	Status        string `pulumi:"status"`
	CreatedAt     string `pulumi:"createdAt"`
}

func (w *Webhook) Annotate(a infer.Annotator) {
	a.Describe(&w, "A Resend webhook that delivers events to an HTTPS endpoint. If Resend omits the `signingSecret` during import or read, the provider returns an empty secret for imports and preserves any existing state value during refreshes.")
}

func (w *WebhookArgs) Annotate(a infer.Annotator) {
	a.Describe(&w.Endpoint, "The HTTPS URL events are delivered to.")
	a.Describe(&w.Events, "The event types to subscribe to, e.g. `email.sent` or `domain.updated`.")
}

func (w *WebhookState) Annotate(a infer.Annotator) {
	a.Describe(&w.SigningSecret, "The secret used to verify webhook payload signatures; may be empty after import if Resend does not return it.")
}

func (*Webhook) WireDependencies(f infer.FieldSelector, args *WebhookArgs, state *WebhookState) {
	f.OutputField(&state.SigningSecret).AlwaysSecret()
}

func (*Webhook) Create(
	ctx context.Context, req infer.CreateRequest[WebhookArgs],
) (infer.CreateResponse[WebhookState], error) {
	if req.DryRun {
		return infer.CreateResponse[WebhookState]{Output: WebhookState{WebhookArgs: req.Inputs}}, nil
	}
	client := getClient(ctx)
	resp, err := client.Webhooks.CreateWithContext(ctx, &resend.CreateWebhookRequest{
		Endpoint: req.Inputs.Endpoint,
		Events:   stringSliceAs[WebhookEvent, string](req.Inputs.Events),
	})
	if err != nil {
		return infer.CreateResponse[WebhookState]{}, fmt.Errorf("creating webhook for %q: %w", req.Inputs.Endpoint, err)
	}
	state := WebhookState{WebhookArgs: req.Inputs, SigningSecret: resp.SigningSecret}
	remote, err := client.Webhooks.GetWithContext(ctx, resp.Id)
	if err != nil {
		return infer.CreateResponse[WebhookState]{ID: resp.Id, Output: state},
			infer.ResourceInitFailedError{Reasons: []string{fmt.Sprintf("reading webhook after create: %s", err)}}
	}
	state.Status = remote.Status
	state.CreatedAt = remote.CreatedAt
	return infer.CreateResponse[WebhookState]{ID: resp.Id, Output: state}, nil
}

func (*Webhook) Update(
	ctx context.Context, req infer.UpdateRequest[WebhookArgs, WebhookState],
) (infer.UpdateResponse[WebhookState], error) {
	state := req.State
	state.WebhookArgs = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[WebhookState]{Output: state}, nil
	}
	client := getClient(ctx)
	_, err := client.Webhooks.UpdateWithContext(ctx, req.ID, &resend.UpdateWebhookRequest{
		Endpoint: &req.Inputs.Endpoint,
		Events:   stringSliceAs[WebhookEvent, string](req.Inputs.Events),
	})
	if err != nil {
		return infer.UpdateResponse[WebhookState]{}, fmt.Errorf("updating webhook %q: %w", req.ID, err)
	}
	remote, err := client.Webhooks.GetWithContext(ctx, req.ID)
	if err != nil {
		return infer.UpdateResponse[WebhookState]{}, fmt.Errorf("reading webhook %q after update: %w", req.ID, err)
	}
	state.Status = remote.Status
	state.CreatedAt = remote.CreatedAt
	return infer.UpdateResponse[WebhookState]{Output: state}, nil
}

func (*Webhook) Read(
	ctx context.Context, req infer.ReadRequest[WebhookArgs, WebhookState],
) (infer.ReadResponse[WebhookArgs, WebhookState], error) {
	remote, err := getClient(ctx).Webhooks.GetWithContext(ctx, req.ID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[WebhookArgs, WebhookState]{}, nil
		}
		return infer.ReadResponse[WebhookArgs, WebhookState]{}, err
	}
	inputs := WebhookArgs{Endpoint: remote.Endpoint, Events: stringSliceAs[string, WebhookEvent](remote.Events)}
	state := WebhookState{
		WebhookArgs:   inputs,
		SigningSecret: remote.SigningSecret,
		Status:        remote.Status,
		CreatedAt:     remote.CreatedAt,
	}
	if state.SigningSecret == "" {
		state.SigningSecret = req.State.SigningSecret
	}
	return infer.ReadResponse[WebhookArgs, WebhookState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

func (*Webhook) Delete(ctx context.Context, req infer.DeleteRequest[WebhookState]) (infer.DeleteResponse, error) {
	if _, err := getClient(ctx).Webhooks.RemoveWithContext(ctx, req.ID); err != nil && !isNotFound(err) {
		return infer.DeleteResponse{}, fmt.Errorf("deleting webhook %q: %w", req.ID, err)
	}
	return infer.DeleteResponse{}, nil
}
