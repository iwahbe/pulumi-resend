package provider

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/resend/resend-go/v4"
)

const (
	apiKeyPermissionFullAccess    = "full_access"
	apiKeyPermissionSendingAccess = "sending_access"

	domainRegionUSEast1      = "us-east-1"
	domainRegionEUWest1      = "eu-west-1"
	domainRegionSAEast1      = "sa-east-1"
	domainRegionAPNortheast1 = "ap-northeast-1"

	domainTLSOpportunistic = string(resend.Opportunistic)
	domainTLSEnforced      = string(resend.Enforced)

	domainCapabilityStatusEnabled  = string(resend.DomainCapabilityStatusEnabled)
	domainCapabilityStatusDisabled = string(resend.DomainCapabilityStatusDisabled)
)

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

func enumFailure(property string, value any, allowed string) p.CheckFailure {
	return p.CheckFailure{Property: property, Reason: fmt.Sprintf("invalid value %q; expected one of: %s", value, allowed)}
}

func validStringEnum(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateApiKeyArgs(args ApiKeyArgs) []p.CheckFailure {
	if args.Permission != nil && !validStringEnum(*args.Permission, apiKeyPermissionFullAccess, apiKeyPermissionSendingAccess) {
		return []p.CheckFailure{enumFailure("permission", *args.Permission, "full_access, sending_access")}
	}
	return nil
}

func validateDomainArgs(args DomainArgs) []p.CheckFailure {
	var failures []p.CheckFailure
	if args.Region != nil && !validStringEnum(*args.Region, domainRegionUSEast1, domainRegionEUWest1, domainRegionSAEast1, domainRegionAPNortheast1) {
		failures = append(failures, enumFailure("region", *args.Region, "us-east-1, eu-west-1, sa-east-1, ap-northeast-1"))
	}
	if args.Tls != nil && !validStringEnum(*args.Tls, domainTLSOpportunistic, domainTLSEnforced) {
		failures = append(failures, enumFailure("tls", *args.Tls, "opportunistic, enforced"))
	}
	if args.Capabilities != nil {
		if args.Capabilities.Sending != nil && !validStringEnum(*args.Capabilities.Sending, domainCapabilityStatusEnabled, domainCapabilityStatusDisabled) {
			failures = append(failures, enumFailure("capabilities.sending", *args.Capabilities.Sending, "enabled, disabled"))
		}
		if args.Capabilities.Receiving != nil && !validStringEnum(*args.Capabilities.Receiving, domainCapabilityStatusEnabled, domainCapabilityStatusDisabled) {
			failures = append(failures, enumFailure("capabilities.receiving", *args.Capabilities.Receiving, "enabled, disabled"))
		}
	}
	return failures
}

func validateWebhookArgs(args WebhookArgs) []p.CheckFailure {
	var failures []p.CheckFailure
	for i, event := range args.Events {
		if !validStringEnum(event,
			resend.EventEmailSent,
			resend.EventEmailDelivered,
			resend.EventEmailDeliveryDelayed,
			resend.EventEmailComplained,
			resend.EventEmailBounced,
			resend.EventEmailOpened,
			resend.EventEmailClicked,
			resend.EventEmailReceived,
			resend.EventEmailFailed,
			resend.EventEmailScheduled,
			resend.EventEmailSuppressed,
			resend.EventContactCreated,
			resend.EventContactUpdated,
			resend.EventContactDeleted,
			resend.EventDomainCreated,
			resend.EventDomainUpdated,
			resend.EventDomainDeleted,
		) {
			failures = append(failures, enumFailure(fmt.Sprintf("events[%d]", i), event, "email.sent, email.delivered, email.delivery_delayed, email.complained, email.bounced, email.opened, email.clicked, email.received, email.failed, email.scheduled, email.suppressed, contact.created, contact.updated, contact.deleted, domain.created, domain.updated, domain.deleted"))
		}
	}
	return failures
}
