package domain

import (
	"time"
)

// SessionState is the lifecycle state of a session (DATABASE.md §4.4,
// ARCHITECTURE.md §11.2).
type SessionState string

const (
	SessionActive    SessionState = "active"
	SessionRevoked   SessionState = "revoked"
	SessionExpired   SessionState = "expired"
	SessionSuspended SessionState = "suspended"
)

// Platform is the client platform (DATABASE.md §4.4).
type Platform string

const (
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
	PlatformWeb     Platform = "web"
)

// DeviceInfo is the validated device identity bound to a session
// (DATABASE.md §4.4, SECURITY_SPEC.md DEVM-1).
type DeviceInfo struct {
	DeviceID   string
	DeviceName *string
	Platform   *string
	AppVersion *string
}

// Session is one device session — the session registry row (DATABASE.md §4.4).
type Session struct {
	ID                 int64
	UserID             int64
	Device             DeviceInfo
	PushToken          *string
	RefreshTokenFamily int64
	RefreshTokenHash   string
	IPAddress          *string
	UserAgent          *string
	LastActiveAt       time.Time
	State              SessionState
	CreatedAt          time.Time
	UpdatedAt          time.Time
}
