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

// ValidateDeviceID checks a client-supplied device id (DEVM-1: validated,
// collision-resistant identity). It returns *ValidationError{Field:
// "device_id"} on failure.
func ValidateDeviceID(id string) error {
	if id == "" {
		return &ValidationError{Field: "device_id", Reason: "required"}
	}
	if len(id) > 64 {
		return &ValidationError{Field: "device_id", Reason: "too_long"}
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return &ValidationError{Field: "device_id", Reason: "invalid_charset"}
		}
	}
	return nil
}

// Session is one device session — the session registry row (DATABASE.md §4.4).
type Session struct {
	ID                       int64
	UserID                   int64
	Device                   DeviceInfo
	PushToken                *string
	RefreshTokenFamily       int64
	RefreshTokenHash         string
	RefreshTokenPreviousHash string
	RefreshExpiresAt         time.Time
	IPAddress                *string
	UserAgent                *string
	LastActiveAt             time.Time
	State                    SessionState
	CreatedAt                time.Time
	UpdatedAt                time.Time
}
