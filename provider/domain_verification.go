package provider

import (
	"context"
	"fmt"
	"time"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type DomainVerification struct{}

type DomainVerificationArgs struct {
	DomainId       string `pulumi:"domainId"`
	TimeoutSeconds *int   `pulumi:"timeoutSeconds,optional"`
}

type DomainVerificationState struct {
	DomainVerificationArgs
	Status string `pulumi:"status"`
}

func (d *DomainVerification) Annotate(a infer.Annotator) {
	a.Describe(&d, "Triggers verification of a Resend domain and waits until the domain's DNS records "+
		"have been verified. Create this resource after the domain's DNS records (from the `Domain` "+
		"resource's `records` output) have been published.")
}

func (d *DomainVerificationArgs) Annotate(a infer.Annotator) {
	a.Describe(&d.DomainId, "The ID of the domain to verify.")
	a.Describe(&d.TimeoutSeconds, "How long to wait for verification before failing. Defaults to 900 seconds.")
}

func (d *DomainVerificationState) Annotate(a infer.Annotator) {
	a.Describe(&d.Status, "The verification status of the domain.")
}

// verifyPollInterval is a variable so tests can poll quickly.
var verifyPollInterval = 10 * time.Second

const defaultVerifyTimeout = 900 * time.Second

func (*DomainVerification) Create(
	ctx context.Context, req infer.CreateRequest[DomainVerificationArgs],
) (infer.CreateResponse[DomainVerificationState], error) {
	state := DomainVerificationState{DomainVerificationArgs: req.Inputs}
	if req.DryRun {
		return infer.CreateResponse[DomainVerificationState]{Output: state}, nil
	}
	client := getClient(ctx)
	if _, err := client.Domains.VerifyWithContext(ctx, req.Inputs.DomainId); err != nil {
		return infer.CreateResponse[DomainVerificationState]{},
			fmt.Errorf("triggering verification of domain %q: %w", req.Inputs.DomainId, err)
	}

	timeout := defaultVerifyTimeout
	if req.Inputs.TimeoutSeconds != nil {
		timeout = time.Duration(*req.Inputs.TimeoutSeconds) * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		domain, err := client.Domains.GetWithContext(ctx, req.Inputs.DomainId)
		if err != nil {
			return infer.CreateResponse[DomainVerificationState]{},
				fmt.Errorf("reading domain %q while waiting for verification: %w", req.Inputs.DomainId, err)
		}
		state.Status = domain.Status
		switch domain.Status {
		case resend.DomainStatusVerified:
			return infer.CreateResponse[DomainVerificationState]{ID: req.Inputs.DomainId, Output: state}, nil
		case resend.DomainStatusFailed, resend.DomainStatusPartiallyFailed:
			return infer.CreateResponse[DomainVerificationState]{},
				fmt.Errorf("verification of domain %q failed with status %q: check that the domain's DNS records are published",
					req.Inputs.DomainId, domain.Status)
		}
		if time.Now().After(deadline) {
			return infer.CreateResponse[DomainVerificationState]{},
				fmt.Errorf("timed out after %s waiting for domain %q to verify (status %q)",
					timeout, req.Inputs.DomainId, domain.Status)
		}
		select {
		case <-ctx.Done():
			return infer.CreateResponse[DomainVerificationState]{}, ctx.Err()
		case <-time.After(verifyPollInterval):
		}
	}
}

func (*DomainVerification) Diff(
	ctx context.Context, req infer.DiffRequest[DomainVerificationArgs, DomainVerificationState],
) (infer.DiffResponse, error) {
	// timeoutSeconds only affects the create-time wait; changing it is not a change.
	if req.State.DomainId != req.Inputs.DomainId {
		return p.DiffResponse{
			HasChanges: true,
			DetailedDiff: map[string]p.PropertyDiff{
				"domainId": {Kind: p.UpdateReplace, InputDiff: true},
			},
		}, nil
	}
	return p.DiffResponse{}, nil
}

func (*DomainVerification) Read(
	ctx context.Context, req infer.ReadRequest[DomainVerificationArgs, DomainVerificationState],
) (infer.ReadResponse[DomainVerificationArgs, DomainVerificationState], error) {
	domain, err := getClient(ctx).Domains.GetWithContext(ctx, req.ID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[DomainVerificationArgs, DomainVerificationState]{}, nil
		}
		return infer.ReadResponse[DomainVerificationArgs, DomainVerificationState]{}, err
	}
	inputs := req.Inputs
	inputs.DomainId = domain.Id
	return infer.ReadResponse[DomainVerificationArgs, DomainVerificationState]{
		ID:     req.ID,
		Inputs: inputs,
		State:  DomainVerificationState{DomainVerificationArgs: inputs, Status: domain.Status},
	}, nil
}
