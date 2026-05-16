package agent

import (
	"context"
	"errors"
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

// WrapPreflightExit builds a standard preflight error from a subprocess exit.
// When ctx has expired with DeadlineExceeded, the returned error text includes
// "deadline exceeded" so downstream classifiers (preflightFailureCode,
// failureCodeForText) map it to FailureModelTimeout instead of
// FailureToolCrash; without this, exec.CommandContext surfaces only
// "signal: killed" when the deadline fires. Returns nil when err is nil.
func WrapPreflightExit(name string, ctx context.Context, err error, output string) error {
	if err == nil {
		return nil
	}
	output = strings.TrimSpace(output)
	marker := ""
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		marker = "deadline exceeded: "
	}
	if output != "" {
		return fmt.Errorf("%s preflight: %s%w: %s", name, marker, err, output)
	}
	return fmt.Errorf("%s preflight: %s%w", name, marker, err)
}

// providerReadinessPattern pairs a substring predicate with the operator-facing
// description emitted when it matches. Single source of truth shared by
// DetectReadinessFailure (returns the description) and
// IsProviderReadinessFailure (returns only the boolean) so the two cannot
// drift.
type providerReadinessPattern struct {
	matches func(msg string) bool
	desc    string
}

var providerReadinessPatterns = []providerReadinessPattern{
	{
		matches: func(m string) bool { return strings.Contains(m, "env_key") && strings.Contains(m, "not set") },
		desc:    "missing configured environment variable",
	},
	{
		matches: func(m string) bool { return strings.Contains(m, "no models match pattern") },
		desc:    "configured model pattern has no match",
	},
	{
		matches: func(m string) bool { return strings.Contains(m, "api key") && strings.Contains(m, "missing") },
		desc:    "missing api key",
	},
	{
		matches: func(m string) bool { return strings.Contains(m, "authentication") && strings.Contains(m, "failed") },
		desc:    "authentication failed",
	},
}

// IsProviderReadinessFailure reports whether msg looks like a provider/config
// readiness failure (missing env vars, no matching model, missing API key,
// authentication failure). Used by the pipeline classifier to map such errors
// to FailureProviderUnavailable without duplicating the pattern set.
func IsProviderReadinessFailure(msg string) bool {
	msg = strings.ToLower(msg)
	for _, p := range providerReadinessPatterns {
		if p.matches(msg) {
			return true
		}
	}
	return false
}

// DetectReadinessFailure extracts provider/config readiness failures from
// startup output that tools otherwise bury inside process stderr.
func DetectReadinessFailure(output string) error {
	msg := strings.ToLower(output)
	for _, p := range providerReadinessPatterns {
		if p.matches(msg) {
			return fmt.Errorf("provider readiness: %s", p.desc)
		}
	}
	return nil
}
