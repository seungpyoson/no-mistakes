package agent

import (
	"context"
	"fmt"
	"strings"
)

// PreflightAgent optionally validates local provider/tool readiness before a
// pipeline enters an expensive review/fix loop.
type PreflightAgent interface {
	Preflight(context.Context) error
}

// Preflight runs an agent's readiness check when that backend implements one.
func Preflight(ctx context.Context, ag Agent) error {
	if checker, ok := ag.(PreflightAgent); ok {
		return checker.Preflight(ctx)
	}
	return nil
}

// DetectReadinessFailure extracts provider/config readiness failures from
// startup output that tools otherwise bury inside process stderr.
func DetectReadinessFailure(output string) error {
	msg := strings.ToLower(output)
	switch {
	case strings.Contains(msg, "env_key") && strings.Contains(msg, "not set"):
		return fmt.Errorf("provider readiness: missing configured environment variable")
	case strings.Contains(msg, "no models match pattern"):
		return fmt.Errorf("provider readiness: configured model pattern has no match")
	case strings.Contains(msg, "api key") && strings.Contains(msg, "missing"):
		return fmt.Errorf("provider readiness: missing api key")
	case strings.Contains(msg, "authentication") && strings.Contains(msg, "failed"):
		return fmt.Errorf("provider readiness: authentication failed")
	default:
		return nil
	}
}
