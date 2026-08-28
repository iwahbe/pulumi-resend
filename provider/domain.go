package provider

import (
	"context"
	"fmt"
	"reflect"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type Domain struct{}

type DomainRegion string

const (
	DomainRegionUSEast1      DomainRegion = "us-east-1"
	DomainRegionEUWest1      DomainRegion = "eu-west-1"
	DomainRegionSAEast1      DomainRegion = "sa-east-1"
	DomainRegionAPNortheast1 DomainRegion = "ap-northeast-1"
)

func (DomainRegion) Values() []infer.EnumValue[DomainRegion] {
	return []infer.EnumValue[DomainRegion]{
		{Value: DomainRegionUSEast1},
		{Value: DomainRegionEUWest1},
		{Value: DomainRegionSAEast1},
		{Value: DomainRegionAPNortheast1},
	}
}

type DomainTLS string

const (
	DomainTLSEnforced      DomainTLS = "enforced"
	DomainTLSOpportunistic DomainTLS = "opportunistic"
)

func (DomainTLS) Values() []infer.EnumValue[DomainTLS] {
	return []infer.EnumValue[DomainTLS]{
		{Value: DomainTLSEnforced},
		{Value: DomainTLSOpportunistic},
	}
}

type DomainCapabilityStatus string

const (
	DomainCapabilityStatusEnabled  DomainCapabilityStatus = "enabled"
	DomainCapabilityStatusDisabled DomainCapabilityStatus = "disabled"
)

func (DomainCapabilityStatus) Values() []infer.EnumValue[DomainCapabilityStatus] {
	return []infer.EnumValue[DomainCapabilityStatus]{
		{Value: DomainCapabilityStatusEnabled},
		{Value: DomainCapabilityStatusDisabled},
	}
}

type DomainCapabilities struct {
	Sending   *DomainCapabilityStatus `pulumi:"sending,optional"`
	Receiving *DomainCapabilityStatus `pulumi:"receiving,optional"`
}

type DomainArgs struct {
	Name              string              `pulumi:"name"`
	Region            *DomainRegion       `pulumi:"region,optional"`
	CustomReturnPath  *string             `pulumi:"customReturnPath,optional"`
	OpenTracking      *bool               `pulumi:"openTracking,optional"`
	ClickTracking     *bool               `pulumi:"clickTracking,optional"`
	TrackingSubdomain *string             `pulumi:"trackingSubdomain,optional"`
	Tls               *DomainTLS          `pulumi:"tls,optional"`
	Capabilities      *DomainCapabilities `pulumi:"capabilities,optional"`
}

type DomainRecord struct {
	Record   string  `pulumi:"record"`
	Name     string  `pulumi:"name"`
	Type     string  `pulumi:"type"`
	Ttl      string  `pulumi:"ttl"`
	Status   string  `pulumi:"status"`
	Value    string  `pulumi:"value"`
	Priority *string `pulumi:"priority,optional"`
}

type DomainState struct {
	DomainArgs
	Status    string         `pulumi:"status"`
	CreatedAt string         `pulumi:"createdAt"`
	Records   []DomainRecord `pulumi:"records"`
}

func (d *Domain) Annotate(a infer.Annotator) {
	a.Describe(&d, "A domain registered with Resend for sending (and optionally receiving) email. "+
		"The `records` output contains the DNS records that must be created before the domain can be verified.")
}

func (d *DomainArgs) Annotate(a infer.Annotator) {
	a.Describe(&d.Name, "The domain name, e.g. `example.com`.")
	a.Describe(&d.Region, "The region emails are sent from: `us-east-1` (default), `eu-west-1`, `sa-east-1`, or `ap-northeast-1`.")
	a.Describe(&d.CustomReturnPath, "The subdomain used for the Return-Path address. Defaults to `send`.")
	a.Describe(&d.OpenTracking, "Track email open rates. Requires a verified tracking subdomain.")
	a.Describe(&d.ClickTracking, "Track link clicks in HTML emails. Requires a verified tracking subdomain.")
	a.Describe(&d.TrackingSubdomain, "Custom subdomain used for open/click tracking links.")
	a.Describe(&d.Tls, "Connection encryption: `opportunistic` (default) or `enforced`.")
	a.Describe(&d.Capabilities, "Enable `sending` and/or `receiving` for this domain.")
}

func (d *DomainState) Annotate(a infer.Annotator) {
	a.Describe(&d.Status, "The verification status of the domain.")
	a.Describe(&d.Records, "The DNS records to create for this domain.")
}

func (*Domain) Create(
	ctx context.Context, req infer.CreateRequest[DomainArgs],
) (infer.CreateResponse[DomainState], error) {
	if req.DryRun {
		return infer.CreateResponse[DomainState]{Output: DomainState{DomainArgs: req.Inputs}}, nil
	}
	client := getClient(ctx)
	resp, err := client.Domains.CreateWithContext(ctx, &resend.CreateDomainRequest{
		Name:              req.Inputs.Name,
		Region:            string(deref(req.Inputs.Region)),
		CustomReturnPath:  deref(req.Inputs.CustomReturnPath),
		TrackingSubdomain: deref(req.Inputs.TrackingSubdomain),
		OpenTracking:      req.Inputs.OpenTracking,
		ClickTracking:     req.Inputs.ClickTracking,
		Capabilities:      capabilitiesToAPI(req.Inputs.Capabilities),
	})
	if err != nil {
		return infer.CreateResponse[DomainState]{}, fmt.Errorf("creating domain %q: %w", req.Inputs.Name, err)
	}
	state := DomainState{
		DomainArgs: req.Inputs,
		Status:     resp.Status,
		CreatedAt:  resp.CreatedAt,
		Records:    recordsFromAPI(resp.Records),
	}
	// The create API does not accept tls; it can only be set via update.
	if req.Inputs.Tls != nil {
		if _, err := client.Domains.UpdateWithContext(ctx, resp.Id, &resend.UpdateDomainRequest{
			Tls: resend.TlsOption(*req.Inputs.Tls),
		}); err != nil {
			return infer.CreateResponse[DomainState]{ID: resp.Id, Output: state},
				infer.ResourceInitFailedError{Reasons: []string{fmt.Sprintf("setting tls: %s", err)}}
		}
	}
	return infer.CreateResponse[DomainState]{ID: resp.Id, Output: state}, nil
}

func (*Domain) Diff(
	ctx context.Context, req infer.DiffRequest[DomainArgs, DomainState],
) (infer.DiffResponse, error) {
	diff := map[string]p.PropertyDiff{}
	old, new := req.State.DomainArgs, req.Inputs
	replace := func(name string) { diff[name] = p.PropertyDiff{Kind: p.UpdateReplace, InputDiff: true} }
	update := func(name string) { diff[name] = p.PropertyDiff{Kind: p.Update, InputDiff: true} }

	// These fields cannot be changed via the update API.
	if old.Name != new.Name {
		replace("name")
	}
	if !ptrEq(old.Region, new.Region) {
		replace("region")
	}
	if !ptrEq(old.CustomReturnPath, new.CustomReturnPath) {
		replace("customReturnPath")
	}
	if !reflect.DeepEqual(old.Capabilities, new.Capabilities) {
		replace("capabilities")
	}

	if !ptrEq(old.OpenTracking, new.OpenTracking) {
		update("openTracking")
	}
	if !ptrEq(old.ClickTracking, new.ClickTracking) {
		update("clickTracking")
	}
	if !ptrEq(old.TrackingSubdomain, new.TrackingSubdomain) {
		if old.TrackingSubdomain != nil && new.TrackingSubdomain == nil {
			replace("trackingSubdomain")
		} else {
			update("trackingSubdomain")
		}
	}
	if !ptrEq(old.Tls, new.Tls) {
		update("tls")
	}
	return p.DiffResponse{HasChanges: len(diff) > 0, DetailedDiff: diff}, nil
}

func (*Domain) Update(
	ctx context.Context, req infer.UpdateRequest[DomainArgs, DomainState],
) (infer.UpdateResponse[DomainState], error) {
	state := req.State
	state.DomainArgs = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[DomainState]{Output: state}, nil
	}
	if req.State.TrackingSubdomain != nil && req.Inputs.TrackingSubdomain == nil {
		return infer.UpdateResponse[DomainState]{}, fmt.Errorf("updating domain %q: clearing trackingSubdomain requires replacement", req.ID)
	}
	client := getClient(ctx)
	upd := &resend.UpdateDomainRequest{
		TrackingSubdomain: deref(req.Inputs.TrackingSubdomain),
	}
	if req.Inputs.Tls != nil {
		upd.Tls = resend.TlsOption(*req.Inputs.Tls)
	} else if req.State.Tls != nil {
		upd.Tls = resend.Opportunistic
	}
	if req.Inputs.OpenTracking != nil {
		upd.SetOpenTracking(*req.Inputs.OpenTracking)
	} else if req.State.OpenTracking != nil {
		upd.SetOpenTracking(false)
	}
	if req.Inputs.ClickTracking != nil {
		upd.SetClickTracking(*req.Inputs.ClickTracking)
	} else if req.State.ClickTracking != nil {
		upd.SetClickTracking(false)
	}
	if _, err := client.Domains.UpdateWithContext(ctx, req.ID, upd); err != nil {
		return infer.UpdateResponse[DomainState]{}, fmt.Errorf("updating domain %q: %w", req.ID, err)
	}
	remote, err := client.Domains.GetWithContext(ctx, req.ID)
	if err != nil {
		return infer.UpdateResponse[DomainState]{}, fmt.Errorf("reading domain %q after update: %w", req.ID, err)
	}
	state.Status = remote.Status
	state.CreatedAt = remote.CreatedAt
	state.Records = recordsFromAPI(remote.Records)
	return infer.UpdateResponse[DomainState]{Output: state}, nil
}

func (*Domain) Read(
	ctx context.Context, req infer.ReadRequest[DomainArgs, DomainState],
) (infer.ReadResponse[DomainArgs, DomainState], error) {
	remote, err := getClient(ctx).Domains.GetWithContext(ctx, req.ID)
	if err != nil {
		if isNotFound(err) {
			return infer.ReadResponse[DomainArgs, DomainState]{}, nil
		}
		return infer.ReadResponse[DomainArgs, DomainState]{}, err
	}
	inputs := req.Inputs
	inputs.Name = remote.Name
	keepFresh(&inputs.Region, DomainRegion(remote.Region))
	keepFresh(&inputs.OpenTracking, remote.OpenTracking)
	keepFresh(&inputs.ClickTracking, remote.ClickTracking)
	keepFresh(&inputs.TrackingSubdomain, remote.TrackingSubdomain)
	if inputs.Capabilities != nil && remote.Capabilities != nil {
		inputs.Capabilities = capabilitiesFromAPI(remote.Capabilities)
	}
	// customReturnPath and tls are not returned by the API; keep the recorded inputs.
	state := DomainState{
		DomainArgs: inputs,
		Status:     remote.Status,
		CreatedAt:  remote.CreatedAt,
		Records:    recordsFromAPI(remote.Records),
	}
	return infer.ReadResponse[DomainArgs, DomainState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

func (*Domain) Delete(ctx context.Context, req infer.DeleteRequest[DomainState]) (infer.DeleteResponse, error) {
	if _, err := getClient(ctx).Domains.RemoveWithContext(ctx, req.ID); err != nil && !isNotFound(err) {
		return infer.DeleteResponse{}, fmt.Errorf("deleting domain %q: %w", req.ID, err)
	}
	return infer.DeleteResponse{}, nil
}

func capabilitiesToAPI(c *DomainCapabilities) *resend.DomainCapabilities {
	if c == nil {
		return nil
	}
	return &resend.DomainCapabilities{
		Sending:   resend.DomainCapabilityStatus(deref(c.Sending)),
		Receiving: resend.DomainCapabilityStatus(deref(c.Receiving)),
	}
}

func capabilitiesFromAPI(c *resend.DomainCapabilities) *DomainCapabilities {
	out := &DomainCapabilities{}
	if c.Sending != "" {
		sending := DomainCapabilityStatus(c.Sending)
		out.Sending = &sending
	}
	if c.Receiving != "" {
		receiving := DomainCapabilityStatus(c.Receiving)
		out.Receiving = &receiving
	}
	return out
}

func recordsFromAPI(records []resend.Record) []DomainRecord {
	out := make([]DomainRecord, len(records))
	for i, r := range records {
		out[i] = DomainRecord{
			Record: r.Record,
			Name:   r.Name,
			Type:   r.Type,
			Ttl:    r.Ttl,
			Status: r.Status,
			Value:  r.Value,
		}
		if r.Priority != "" {
			priority := string(r.Priority)
			out[i].Priority = &priority
		}
	}
	return out
}
