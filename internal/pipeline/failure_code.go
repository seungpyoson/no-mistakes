package pipeline

import (
	"context"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func failureCodeForStepError(step types.StepName, err error, ctxs ...context.Context) types.FailureCode {
	// Treat supersede the same as an explicit user abort: both are user-driven
	// cancellations and should not be misclassified as tool crashes. Operators
	// can still distinguish by run.status (cancelled) and the error text
	// ('cancelled: superseded by new push' vs 'cancelled: aborted by user').
	if contextWasUserAbort(ctxs...) || textIsUserAbort(errString(err)) ||
		contextWasSuperseded(ctxs...) || textIsSuperseded(errString(err)) {
		return types.FailureUserAbort
	}
	if step == types.StepTest {
		return types.FailureTestFailure
	}
	if step == types.StepCI {
		return types.FailureCIFailure
	}
	return failureCodeForText(errString(err))
}

func failureCodeForRunError(err error, ctxs ...context.Context) types.FailureCode {
	// Treat supersede the same as an explicit user abort: both are user-driven
	// cancellations and should not be misclassified as tool crashes. Operators
	// can still distinguish by run.status (cancelled) and the error text
	// ('cancelled: superseded by new push' vs 'cancelled: aborted by user').
	if contextWasUserAbort(ctxs...) || textIsUserAbort(errString(err)) ||
		contextWasSuperseded(ctxs...) || textIsSuperseded(errString(err)) {
		return types.FailureUserAbort
	}
	msg := strings.ToLower(errString(err))
	switch {
	case strings.Contains(msg, "step test failed"):
		return types.FailureTestFailure
	case strings.Contains(msg, "step ci failed"):
		return types.FailureCIFailure
	default:
		return failureCodeForText(msg)
	}
}

func failureCodeForText(msg string) types.FailureCode {
	msg = strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "model_fix_loop"):
		return types.FailureModelFixLoop
	case agent.IsProviderReadinessFailure(msg):
		return types.FailureProviderUnavailable
	case strings.Contains(msg, "deadline exceeded"),
		strings.Contains(msg, "timed out"),
		strings.Contains(msg, "timeout"):
		return types.FailureModelTimeout
	default:
		return types.FailureToolCrash
	}
}

func contextWasUserAbort(ctxs ...context.Context) bool {
	for _, ctx := range ctxs {
		if cause := context.Cause(ctx); cause != nil && textIsUserAbort(cause.Error()) {
			return true
		}
	}
	return false
}

func contextWasSuperseded(ctxs ...context.Context) bool {
	for _, ctx := range ctxs {
		if cause := context.Cause(ctx); cause != nil && textIsSuperseded(cause.Error()) {
			return true
		}
	}
	return false
}

func textIsUserAbort(msg string) bool {
	msg = strings.ToLower(msg)
	return msg == types.RunCancelReasonAbortedByUser ||
		strings.Contains(msg, "aborted by user")
}

func textIsSuperseded(msg string) bool {
	msg = strings.ToLower(msg)
	return msg == types.RunCancelReasonSuperseded ||
		strings.Contains(msg, "superseded by new push")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
