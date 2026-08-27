package provider

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/resend/resend-go/v4"
)

type ApiKeyPermission string

const (
	ApiKeyPermissionFullAccess    ApiKeyPermission = "full_access"
	ApiKeyPermissionSendingAccess ApiKeyPermission = "sending_access"
)

func (ApiKeyPermission) Values() []infer.EnumValue[ApiKeyPermission] {
	return []infer.EnumValue[ApiKeyPermission]{
		{Value: ApiKeyPermissionFullAccess, Description: "Full access to the Resend API."},
		{Value: ApiKeyPermissionSendingAccess, Description: "Permission to send emails."},
	}
}

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

type httpStatusCoder interface {
	HTTPStatusCode() int
}

type statusCoder interface {
	StatusCode() int
}

type statuser interface {
	Status() int
}

// isNotFound reports whether err is a Resend not-found API error. resend-go/v4
// currently returns opaque errors for non-2xx API responses, so prefer typed
// status access if present and retain a narrow message fallback for the current
// SDK.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var httpStatus httpStatusCoder
	if errors.As(err, &httpStatus) && httpStatus.HTTPStatusCode() == http.StatusNotFound {
		return true
	}
	var statusCode statusCoder
	if errors.As(err, &statusCode) && statusCode.StatusCode() == http.StatusNotFound {
		return true
	}
	var status statuser
	if errors.As(err, &status) && status.Status() == http.StatusNotFound {
		return true
	}
	msg := strings.TrimSpace(strings.TrimPrefix(err.Error(), "[ERROR]:"))
	return strings.EqualFold(msg, "not found") || strings.HasSuffix(strings.ToLower(msg), " not found")
}

func ptrStringValue[T ~string](v *T) string {
	if v == nil {
		return ""
	}
	return string(*v)
}

func ptrString[T ~string](v string) *T {
	out := T(v)
	return &out
}

func webhookEventsToStrings(events []WebhookEvent) []string {
	out := make([]string, len(events))
	for i, event := range events {
		out[i] = string(event)
	}
	return out
}

func webhookEventsFromStrings(events []string) []WebhookEvent {
	out := make([]WebhookEvent, len(events))
	for i, event := range events {
		out[i] = WebhookEvent(event)
	}
	return out
}

func enumFailure(property string, value any, allowed string) p.CheckFailure {
	return p.CheckFailure{Property: property, Reason: fmt.Sprintf("invalid value %q; expected one of: %s", value, allowed)}
}

func validEnum[T infer.EnumKind, E interface{ Values() []infer.EnumValue[T] }](value T, enum E) bool {
	for _, candidate := range enum.Values() {
		if value == candidate.Value {
			return true
		}
	}
	return false
}

func enumValuesText[T infer.EnumKind, E interface{ Values() []infer.EnumValue[T] }](enum E) string {
	values := enum.Values()
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprint(value.Value)
	}
	return strings.Join(parts, ", ")
}

func validateEnumPtr[T infer.EnumKind, E interface{ Values() []infer.EnumValue[T] }](failures *[]p.CheckFailure, property string, value *T, enum E) {
	if value != nil && !validEnum(*value, enum) {
		*failures = append(*failures, enumFailure(property, *value, enumValuesText(enum)))
	}
}

// infer enum Values() produce typed schema/SDKs, but infer.DefaultCheck does
// not reject invalid raw strings for enum aliases; these validators provide the
// runtime Check failures while deriving allowed values from the enum methods.
func validateApiKeyArgs(args ApiKeyArgs) []p.CheckFailure {
	var failures []p.CheckFailure
	validateEnumPtr(&failures, "permission", args.Permission, ApiKeyPermission(""))
	return failures
}

func validateDomainArgs(args DomainArgs) []p.CheckFailure {
	var failures []p.CheckFailure
	validateEnumPtr(&failures, "region", args.Region, DomainRegion(""))
	validateEnumPtr(&failures, "tls", args.Tls, DomainTLS(""))
	if args.Capabilities != nil {
		validateEnumPtr(&failures, "capabilities.sending", args.Capabilities.Sending, DomainCapabilityStatus(""))
		validateEnumPtr(&failures, "capabilities.receiving", args.Capabilities.Receiving, DomainCapabilityStatus(""))
	}
	return failures
}

func validateWebhookArgs(args WebhookArgs) []p.CheckFailure {
	var failures []p.CheckFailure
	for i, event := range args.Events {
		if !validEnum(event, WebhookEvent("")) {
			failures = append(failures, enumFailure(fmt.Sprintf("events[%d]", i), event, enumValuesText(WebhookEvent(""))))
		}
	}
	return failures
}
