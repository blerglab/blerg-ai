package credentials

import (
	"context"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/contracts/engineaccess"
)

var (
	ErrNotManaged        = errors.New("credential is not managed")
	ErrReconnectRequired = errors.New("credential requires reconnect")
	ErrRefreshPending    = errors.New("credential refresh pending")
	ErrLoginPending      = errors.New("login pending")
)

// ManagedToken is encrypted vault state. It must never be serialized by an HTTP handler.
type ManagedToken struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	AccountID    string    `json:"account_id"`
	PlanType     string    `json:"plan_type,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// DeviceGrant is private provider state; only safe metadata belongs in LoginStatus.
type DeviceGrant struct {
	DeviceID        string    `json:"device_id"`
	UserCode        string    `json:"user_code"`
	VerificationURL string    `json:"verification_url"`
	IntervalSeconds int       `json:"interval_seconds"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// Adapter owns one engine/kind's authentication protocol. Registry keys are explicit
// so future static/external/renewable engines do not leak provider rules into the broker.
type Adapter interface {
	Engine() string
	Kind() string
	// AccessMode names the consumer protocol for access-only credentials.
	AccessMode() string
	VerificationOrigin() string
	StartDevice(context.Context) (DeviceGrant, error)
	PollDevice(context.Context, DeviceGrant) (ManagedToken, error)
	Refresh(context.Context, ManagedToken) (ManagedToken, error)
}

// Access is the only managed secret a consumer can obtain. No refresh authority leaves Core.
type Access = engineaccess.Access

// LoginStatus is safe browser metadata, bound to the initiating browser session.
type LoginStatus struct {
	AttemptID       string    `json:"attempt_id"`
	State           string    `json:"state"`
	VerificationURL string    `json:"verification_url,omitempty"`
	UserCode        string    `json:"user_code,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	IntervalSeconds int       `json:"interval_seconds"`
}
