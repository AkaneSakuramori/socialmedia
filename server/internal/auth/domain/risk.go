package domain

import "context"

// RiskContext carries the signals available at authentication time for the
// risk-based escalation hook (SECURITY_SPEC.md AUTH-11: new device, unfamiliar
// geography, token-family reuse → escalate, not blanket-block).
type RiskContext struct {
	UserID    int64
	DeviceID  string
	NewDevice bool
	IPAddress *string
	UserAgent *string
	Method    LoginMethod
}

// RiskDecision is the hook's verdict. StepUp escalates the session to require
// re-confirmation (AUTH-9/AUTH-11); Notify surfaces a security event to the
// account holder (e.g. "sign-in from a new device").
type RiskDecision struct {
	StepUp bool
	Notify bool
}

// RiskEvaluator is the risk-based validation hook (AUTH-11, SHOULD). The
// default implementation is permissive; richer scoring (geo, Play Integrity /
// App Attestation, token-family reuse) plugs in here without touching the login
// flow.
type RiskEvaluator interface {
	Evaluate(ctx context.Context, rc RiskContext) (RiskDecision, error)
}

// PermissiveRisk returns the default no-op evaluator: never escalates, never
// notifies. Used until real integrity/geo signals exist (AUTH-11 is advisory).
func PermissiveRisk() RiskEvaluator { return permissiveRisk{} }

type permissiveRisk struct{}

func (permissiveRisk) Evaluate(context.Context, RiskContext) (RiskDecision, error) {
	return RiskDecision{}, nil
}
