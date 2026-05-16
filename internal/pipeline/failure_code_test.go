package pipeline

import (
	"context"
	"fmt"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestFailureCodeForStepErrorClassifiesNoMistakesFailures(t *testing.T) {
	tests := []struct {
		name string
		step types.StepName
		err  error
		want types.FailureCode
	}{
		{
			name: "model timeout",
			step: types.StepReview,
			err:  fmt.Errorf("agent review: context deadline exceeded"),
			want: types.FailureModelTimeout,
		},
		{
			name: "tool crash",
			step: types.StepReview,
			err:  fmt.Errorf("agent fix: pi exited: signal: killed"),
			want: types.FailureToolCrash,
		},
		{
			name: "test failure",
			step: types.StepTest,
			err:  fmt.Errorf("tests failed"),
			want: types.FailureTestFailure,
		},
		{
			name: "ci failure",
			step: types.StepCI,
			err:  fmt.Errorf("ci failed"),
			want: types.FailureCIFailure,
		},
		{
			name: "provider readiness",
			step: types.StepReview,
			err:  fmt.Errorf("env_key POOLSIDE_API_KEY is configured but the environment variable is not set"),
			want: types.FailureProviderUnavailable,
		},
		{
			name: "model fix loop",
			step: types.StepReview,
			err:  fmt.Errorf("model_fix_loop: selected findings made no progress"),
			want: types.FailureModelFixLoop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureCodeForStepError(tt.step, tt.err); got != tt.want {
				t.Fatalf("failureCodeForStepError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFailureCodeForStepErrorClassifiesContextAbort(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(fmt.Errorf(types.RunCancelReasonAbortedByUser))

	if got := failureCodeForStepError(types.StepReview, context.Canceled, ctx); got != types.FailureUserAbort {
		t.Fatalf("failureCodeForStepError() = %q, want %q", got, types.FailureUserAbort)
	}
}

func TestFailureCodeForRunErrorClassifiesWrappedStepFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want types.FailureCode
	}{
		{
			name: "wrapped test step",
			err:  fmt.Errorf("step test failed: exit status 1"),
			want: types.FailureTestFailure,
		},
		{
			name: "wrapped ci step",
			err:  fmt.Errorf("step ci failed: required check failed"),
			want: types.FailureCIFailure,
		},
		{
			name: "user abort text",
			err:  fmt.Errorf(types.RunCancelReasonAbortedByUser),
			want: types.FailureUserAbort,
		},
		{
			name: "superseded is user abort",
			err:  fmt.Errorf(types.RunCancelReasonSuperseded),
			want: types.FailureUserAbort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureCodeForRunError(tt.err); got != tt.want {
				t.Fatalf("failureCodeForRunError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFailureCodeForRunErrorClassifiesContextAbort(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(fmt.Errorf(types.RunCancelReasonAbortedByUser))

	if got := failureCodeForRunError(context.Canceled, ctx); got != types.FailureUserAbort {
		t.Fatalf("failureCodeForRunError() = %q, want %q", got, types.FailureUserAbort)
	}
}
