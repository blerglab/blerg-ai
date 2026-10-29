package protocol

import "github.com/blerglab/blerg-ai/contracts/desktopauth"

// DesktopEnroll is private to one authenticated daemon socket; never a browser,
// session, event or log payload.
type DesktopEnroll struct {
	Type                 string              `json:"type"`
	RequestID            string              `json:"request_id"`
	ConnectionGeneration string              `json:"connection_generation"`
	Binding              desktopauth.Binding `json:"binding"`
	Token                string              `json:"token"`
}
type DesktopEnrollAck struct {
	Type                 string `json:"type"`
	RequestID            string `json:"request_id"`
	ConnectionGeneration string `json:"connection_generation"`
}
