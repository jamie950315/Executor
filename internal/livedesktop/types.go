// Package livedesktop implements owner-authorized, leased WebRTC desktop sessions.
package livedesktop

import (
	"context"
	"time"
)

type Geometry struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Options struct {
	MaxWidth int `json:"maxWidth"`
	FPS      int `json:"fps"`
	Bitrate  int `json:"bitrate"`
}

type Sample struct {
	Data     []byte
	Duration time.Duration
}

type VideoSource interface {
	Read(context.Context) (Sample, error)
	Close() error
}

// InputEvent is carried only by the authenticated peer's ordered data channel.
// Coordinates are normalized to the full primary display, not video pixels.
type InputEvent struct {
	Type    string  `json:"type"`
	Seq     uint64  `json:"seq"`
	X       float64 `json:"x,omitempty"`
	Y       float64 `json:"y,omitempty"`
	Button  int     `json:"button,omitempty"`
	Code    string  `json:"code,omitempty"`
	Down    bool    `json:"down,omitempty"`
	Text    string  `json:"text,omitempty"`
	ScrollX int     `json:"scrollX,omitempty"`
	ScrollY int     `json:"scrollY,omitempty"`
	Enabled bool    `json:"enabled,omitempty"`
}

type Backend interface {
	Geometry(context.Context) (Geometry, error)
	Available(context.Context) bool
	OpenVideo(context.Context, Options) (VideoSource, error)
	Input(context.Context, InputEvent) error
	Release(context.Context) error
}

type Session struct {
	SessionID string `json:"sessionId"`
	Answer    string `json:"answer"`
	Geometry
	LeaseSeconds int `json:"leaseSeconds"`
}

type Status struct {
	RelayConfigured bool     `json:"relayConfigured,omitempty"`
	Supported       bool     `json:"supported"`
	Available       bool     `json:"available"`
	Active          bool     `json:"active"`
	Reason          string   `json:"reason,omitempty"`
	ICEServers      []string `json:"iceServers"`
}
