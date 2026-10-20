package daemon

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// EngineSessionState is private recovery metadata, bound to its registered
// engine and version. It contains identity and an optional opaque routing checksum,
// never configuration, endpoint URLs or auth.
// Existing Claude/Codex record fields remain readable for older records.
type EngineSessionState struct {
	Version          int    `json:"version"`
	Engine           string `json:"engine"`
	ID               string `json:"id"`
	RouteFingerprint string `json:"route_fingerprint,omitempty"`
}
type engineSessionDriver interface {
	SetEngineSession(*EngineSessionState, func(EngineSessionState)) error
	SetEngineObserver(func(int, bool))
	SetNativeResumeGate(func(context.Context))
	EngineSessionLoaded() <-chan error
}

var engineRouteFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var nativeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)

func (s EngineSessionState) valid(engine string) bool {
	spec, ok := engineRegistry[engine]
	minVersion := spec.NativeSessionMinVersion
	if minVersion == 0 {
		minVersion = spec.NativeSessionVersion
	}
	return ok && s.Engine == engine && s.Version > 0 && s.Version >= minVersion && s.Version <= spec.NativeSessionVersion && nativeSessionIDPattern.MatchString(s.ID) && (s.RouteFingerprint == "" || engineRouteFingerprintPattern.MatchString(s.RouteFingerprint))
}
func (d *hermesDriver) SetEngineSession(state *EngineSessionState, onUpdate func(EngineSessionState)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if state != nil {
		if !state.valid("hermes") || (state.Version >= 2 && state.RouteFingerprint == "") {
			return errors.New("invalid native engine session identity")
		}
		d.nativeID = state.ID
		d.restoredFingerprint = state.RouteFingerprint
	}
	d.onSessionID = func(id string) {
		d.mu.Lock()
		fingerprint := d.routeFingerprint
		qualified := strings.HasPrefix(d.model, "custom:")
		bound := d.routeBound
		d.mu.Unlock()
		version := engineRegistry["hermes"].NativeSessionMinVersion
		if fingerprint != "" || qualified || bound {
			version = engineRegistry["hermes"].NativeSessionVersion
		}
		s := EngineSessionState{Version: version, Engine: "hermes", ID: id, RouteFingerprint: fingerprint}
		if s.valid("hermes") && onUpdate != nil {
			onUpdate(s)
		}
	}
	return nil
}
func (d *hermesDriver) SetEngineObserver(fn func(int, bool)) {
	d.mu.Lock()
	d.onEngine = fn
	d.mu.Unlock()
}

func (d *hermesDriver) SetNativeResumeGate(fn func(context.Context)) {
	d.mu.Lock()
	d.resumeGate = fn
	d.mu.Unlock()
}
func (d *hermesDriver) EngineSessionLoaded() <-chan error { return d.loaded }
