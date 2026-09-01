package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type ContactProperty struct{}

type ContactPropertyType string

const (
	ContactPropertyTypeString ContactPropertyType = "string"
	ContactPropertyTypeNumber ContactPropertyType = "number"
)

func (ContactPropertyType) Values() []infer.EnumValue[ContactPropertyType] {
	return []infer.EnumValue[ContactPropertyType]{
		{Value: ContactPropertyTypeString, Description: "A string contact property."},
		{Value: ContactPropertyTypeNumber, Description: "A numeric contact property."},
	}
}

type ContactPropertyArgs struct {
	Key           string              `pulumi:"key" provider:"replaceOnChanges"`
	Type          ContactPropertyType `pulumi:"type" provider:"replaceOnChanges"`
	FallbackValue *any                `pulumi:"fallbackValue,optional"`
}

type ContactPropertyState struct {
	ContactPropertyArgs
	CreatedAt string `pulumi:"createdAt"`
}

func (c *ContactProperty) Annotate(a infer.Annotator) {
	a.Describe(&c, "A Resend custom contact property definition.")
}

func (c *ContactPropertyArgs) Annotate(a infer.Annotator) {
	a.Describe(&c.Key, "The contact property key. Resend allows up to 50 alphanumeric or underscore characters. The key cannot be changed after creation, so changes replace the resource.")
	a.Describe(&c.Type, "The contact property type, either `string` or `number`. The type cannot be changed after creation, so changes replace the resource.")
	a.Describe(&c.FallbackValue, "Optional fallback value used when a contact does not have this property set. Values must match `type` (`string` or `number`). Fallback changes, including clearing a previously non-null fallback, update in place.")
}

func (*ContactProperty) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[ContactPropertyArgs], error) {
	inputs, failures, err := infer.DefaultCheck[ContactPropertyArgs](ctx, req.NewInputs)
	resp := infer.CheckResponse[ContactPropertyArgs]{Inputs: inputs, Failures: failures}
	if err != nil || len(failures) != 0 {
		return resp, err
	}
	if req.NewInputs.Get("type").HasComputed() || req.NewInputs.Get("fallbackValue").HasComputed() {
		return resp, nil
	}
	if err := validateContactPropertyFallback(inputs.Type, inputs.FallbackValue); err != nil {
		resp.Failures = append(resp.Failures, p.CheckFailure{Property: "fallbackValue", Reason: err.Error()})
	}
	return resp, nil
}

func (c *ContactPropertyState) Annotate(a infer.Annotator) {
	a.Describe(&c.CreatedAt, "The timestamp when Resend created the contact property, as returned by the Resend API `created_at` string.")
}

func (*ContactProperty) Create(
	ctx context.Context, req infer.CreateRequest[ContactPropertyArgs],
) (infer.CreateResponse[ContactPropertyState], error) {
	if req.DryRun {
		return infer.CreateResponse[ContactPropertyState]{Output: ContactPropertyState{ContactPropertyArgs: req.Inputs}}, nil
	}
	client := getClient(ctx)
	resp, err := client.ContactProperties.CreateWithContext(ctx, &resend.CreateContactPropertyRequest{
		Key:           req.Inputs.Key,
		Type:          string(req.Inputs.Type),
		FallbackValue: deref(req.Inputs.FallbackValue),
	})
	if err != nil {
		return infer.CreateResponse[ContactPropertyState]{}, fmt.Errorf("creating contact property %q: %w", req.Inputs.Key, err)
	}
	state := ContactPropertyState{ContactPropertyArgs: req.Inputs}
	remote, err := client.ContactProperties.GetWithContext(ctx, resp.Id)
	if err != nil {
		return infer.CreateResponse[ContactPropertyState]{ID: resp.Id, Output: state},
			infer.ResourceInitFailedError{Reasons: []string{fmt.Sprintf("reading contact property %q after create: %s", resp.Id, err)}}
	}
	refreshContactPropertyState(&state, &remote)
	return infer.CreateResponse[ContactPropertyState]{ID: resp.Id, Output: state}, nil
}

func (*ContactProperty) Update(
	ctx context.Context, req infer.UpdateRequest[ContactPropertyArgs, ContactPropertyState],
) (infer.UpdateResponse[ContactPropertyState], error) {
	state := req.State
	state.ContactPropertyArgs = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[ContactPropertyState]{Output: state}, nil
	}
	client := getClient(ctx)
	_, err := client.ContactProperties.UpdateWithContext(ctx, &resend.UpdateContactPropertyRequest{
		Id:            req.ID,
		FallbackValue: deref(req.Inputs.FallbackValue),
	})
	if err != nil {
		return infer.UpdateResponse[ContactPropertyState]{}, fmt.Errorf("updating contact property %q fallback value: %w", req.ID, err)
	}
	remote, err := client.ContactProperties.GetWithContext(ctx, req.ID)
	if err != nil {
		return infer.UpdateResponse[ContactPropertyState]{}, fmt.Errorf("reading contact property %q after update: %w", req.ID, err)
	}
	refreshContactPropertyState(&state, &remote)
	return infer.UpdateResponse[ContactPropertyState]{Output: state}, nil
}

func (*ContactProperty) Read(
	ctx context.Context, req infer.ReadRequest[ContactPropertyArgs, ContactPropertyState],
) (infer.ReadResponse[ContactPropertyArgs, ContactPropertyState], error) {
	remote, err := getClient(ctx).ContactProperties.GetWithContext(ctx, req.ID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[ContactPropertyArgs, ContactPropertyState]{}, nil
		}
		return infer.ReadResponse[ContactPropertyArgs, ContactPropertyState]{}, fmt.Errorf("reading contact property %q: %w", req.ID, err)
	}
	inputs := ContactPropertyArgs{Key: remote.Key, Type: ContactPropertyType(remote.Type)}
	if remote.FallbackValue != nil {
		inputs.FallbackValue = &remote.FallbackValue
	}
	state := ContactPropertyState{ContactPropertyArgs: inputs}
	refreshContactPropertyState(&state, &remote)
	return infer.ReadResponse[ContactPropertyArgs, ContactPropertyState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

func (*ContactProperty) Delete(ctx context.Context, req infer.DeleteRequest[ContactPropertyState]) (infer.DeleteResponse, error) {
	if _, err := getClient(ctx).ContactProperties.RemoveWithContext(ctx, req.ID); err != nil && !isNotFound(err) {
		return infer.DeleteResponse{}, fmt.Errorf("deleting contact property %q: %w", req.ID, err)
	}
	return infer.DeleteResponse{}, nil
}

func refreshContactPropertyState(state *ContactPropertyState, remote *resend.ContactProperty) {
	state.Key = remote.Key
	state.Type = ContactPropertyType(remote.Type)
	if remote.FallbackValue == nil {
		state.FallbackValue = nil
	} else {
		state.FallbackValue = &remote.FallbackValue
	}
	state.CreatedAt = remote.CreatedAt
}

func validateContactPropertyFallback(typ ContactPropertyType, fallback *any) error {
	if fallback == nil || *fallback == nil {
		return nil
	}
	switch typ {
	case ContactPropertyTypeString:
		if _, ok := (*fallback).(string); !ok {
			return fmt.Errorf("contact property fallbackValue for type %q must be a string", typ)
		}
	case ContactPropertyTypeNumber:
		if !isJSONNumber(*fallback) {
			return fmt.Errorf("contact property fallbackValue for type %q must be a number", typ)
		}
	}
	return nil
}

func isJSONNumber(v any) bool {
	switch n := v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32:
		return true
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0)
	case json.Number:
		_, err := n.Float64()
		return err == nil
	default:
		return false
	}
}
