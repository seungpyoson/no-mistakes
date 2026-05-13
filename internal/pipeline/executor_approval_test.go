package pipeline

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/telemetry"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExecutor_ApprovalFix(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	// Step that needs approval on first call, passes on second
	callCount := 0
	var step Step = &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: `{"issues":["bug"]}`}, nil
			}
			// After fix, re-evaluate passes
			return &StepOutcome{NeedsApproval: false, ExitCode: 0}, nil
		},
	}

	steps := []Step{step, newPassStep(types.StepTest)}
	exec := NewExecutor(database, p, nil, nil, steps, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	// Wait for awaiting_approval
	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	// Send fix action
	exec.Respond(types.StepReview, types.ActionFix, nil)

	// Wait for step to re-execute and complete (it passes on second call)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	// Both steps should be completed
	dbSteps, _ := database.GetStepsByRun(run.ID)
	if dbSteps[0].Status != types.StepStatusCompleted {
		t.Errorf("review: expected %q, got %q", types.StepStatusCompleted, dbSteps[0].Status)
	}
	if dbSteps[1].Status != types.StepStatusCompleted {
		t.Errorf("test: expected %q, got %q", types.StepStatusCompleted, dbSteps[1].Status)
	}

	// Step should have been called twice (initial + after fix)
	if callCount != 2 {
		t.Errorf("expected step to be called 2 times, got %d", callCount)
	}
}

func TestExecutor_ApprovalSkipSkipsCurrentStepAndContinues(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	review := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(_ *StepContext) (*StepOutcome, error) {
			return &StepOutcome{
				NeedsApproval: true,
				Findings:      `{"findings":[{"id":"review-1","severity":"warning","description":"false positive","action":"ask-user"}],"summary":"1 finding"}`,
			}, nil
		},
	}
	testStep := newPassStep(types.StepTest)

	exec := NewExecutor(database, p, nil, nil, []Step{review, testStep}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	if err := exec.Respond(types.StepReview, types.ActionSkip, []string{"review-1"}); err != nil {
		t.Fatalf("respond skip: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	dbSteps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbSteps[0].Status != types.StepStatusSkipped {
		t.Fatalf("review status = %q, want %q", dbSteps[0].Status, types.StepStatusSkipped)
	}
	if dbSteps[1].Status != types.StepStatusCompleted {
		t.Fatalf("test status = %q, want %q", dbSteps[1].Status, types.StepStatusCompleted)
	}
}

func TestExecutor_ApprovalSkipRecordsRejectedFindingRationale(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	review := newApprovalStep(types.StepReview, `{"findings":[{"id":"review-1","severity":"warning","description":"false positive","action":"ask-user"}],"summary":"1 finding"}`)
	exec := NewExecutor(database, p, nil, nil, []Step{review}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	instructions := map[string]string{"review-1": "false positive: generated file is intentionally checked in"}
	added := []types.Finding{{Severity: "warning", Description: "manual follow-up finding", Action: types.ActionAskUser}}
	if err := exec.RespondWithOverrides(types.StepReview, types.ActionSkip, []string{"review-1"}, instructions, added); err != nil {
		t.Fatalf("respond skip: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := database.GetRoundsByStep(steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].SelectedFindingIDs == nil || *rounds[0].SelectedFindingIDs != `["review-1","user-1"]` {
		t.Fatalf("selected_finding_ids = %v, want [review-1,user-1]", rounds[0].SelectedFindingIDs)
	}
	if rounds[0].UserFindingsJSON == nil || !strings.Contains(*rounds[0].UserFindingsJSON, "false positive: generated file") {
		t.Fatalf("user_findings_json = %v, want rejection rationale", rounds[0].UserFindingsJSON)
	}
	if !strings.Contains(*rounds[0].UserFindingsJSON, "manual follow-up finding") {
		t.Fatalf("user_findings_json = %v, want user-added finding", rounds[0].UserFindingsJSON)
	}
}

func TestExecutor_ApprovalAbortRecordsUserAbortCode(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	review := newApprovalStep(types.StepReview, `{"findings":[{"id":"review-1","severity":"error","description":"stop","action":"ask-user"}],"summary":"1 finding"}`)
	exec := NewExecutor(database, p, nil, nil, []Step{review}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	if err := exec.Respond(types.StepReview, types.ActionAbort, nil); err != nil {
		t.Fatalf("respond abort: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected abort error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	updated, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != types.RunFailed {
		t.Fatalf("run status = %q, want %q", updated.Status, types.RunFailed)
	}
	if updated.ErrorCode == nil || *updated.ErrorCode != string(types.FailureUserAbort) {
		t.Fatalf("run error_code = %v, want %q", updated.ErrorCode, types.FailureUserAbort)
	}

	dbSteps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dbSteps[0].ErrorCode == nil || *dbSteps[0].ErrorCode != string(types.FailureUserAbort) {
		t.Fatalf("step error_code = %v, want %q", dbSteps[0].ErrorCode, types.FailureUserAbort)
	}
}

func TestExecutor_ApprovalAbortRecordsRejectedFindingRationale(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	review := newApprovalStep(types.StepReview, `{"findings":[{"id":"review-1","severity":"warning","description":"stop here","action":"ask-user"}],"summary":"1 finding"}`)
	exec := NewExecutor(database, p, nil, nil, []Step{review}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	instructions := map[string]string{"review-1": "abort because the finding needs manual investigation"}
	if err := exec.RespondWithOverrides(types.StepReview, types.ActionAbort, []string{"review-1"}, instructions, nil); err != nil {
		t.Fatalf("respond abort: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected abort error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rounds, err := database.GetRoundsByStep(steps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if rounds[0].SelectedFindingIDs == nil || *rounds[0].SelectedFindingIDs != `["review-1"]` {
		t.Fatalf("selected_finding_ids = %v, want [review-1]", rounds[0].SelectedFindingIDs)
	}
	if rounds[0].UserFindingsJSON == nil || !strings.Contains(*rounds[0].UserFindingsJSON, "manual investigation") {
		t.Fatalf("user_findings_json = %v, want abort rationale", rounds[0].UserFindingsJSON)
	}
}

func TestExecutor_TracksApprovalAndUserFixTelemetry(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	recorder := &telemetryRecorder{}
	restore := telemetry.SetDefaultForTesting(recorder)
	defer restore()

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{NeedsApproval: true, Findings: `{"findings":[{"severity":"error","description":"bug one","action":"auto-fix"},{"severity":"warn","description":"bug two","action":"ask-user"}],"summary":"2 issues"}`}, nil
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	exec := NewExecutor(database, p, &config.Config{Agent: types.AgentClaude}, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	if err := exec.Respond(types.StepReview, types.ActionFix, nil); err != nil {
		t.Fatalf("respond error: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	approvalEvent := recorder.find("approval", "action", "fix")
	if approvalEvent == nil {
		t.Fatal("expected approval telemetry event")
	}
	if got := approvalEvent.fields["step"]; got != string(types.StepReview) {
		t.Fatalf("approval step = %v, want %q", got, types.StepReview)
	}
	if got := approvalEvent.fields["selected_findings_count"]; fmt.Sprint(got) != "2" {
		t.Fatalf("approval selected_findings_count = %v, want 2", got)
	}

	fixEvent := recorder.find("fix", "source", "user")
	if fixEvent == nil {
		t.Fatal("expected user fix telemetry event")
	}
	if got := fixEvent.fields["selected_findings_count"]; fmt.Sprint(got) != "2" {
		t.Fatalf("fix selected_findings_count = %v, want 2", got)
	}

	stepEvent := recorder.find("step", "status", string(types.StepStatusAwaitingApproval))
	if stepEvent == nil {
		t.Fatal("expected awaiting approval step telemetry event")
	}
	if got := stepEvent.fields["findings_count"]; fmt.Sprint(got) != "2" {
		t.Fatalf("step findings_count = %v, want 2", got)
	}
	if got := stepEvent.fields["agent"]; got != string(types.AgentClaude) {
		t.Fatalf("step agent = %v, want %q", got, types.AgentClaude)
	}
}

func TestExecutor_TracksAutoFixTelemetry(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	recorder := &telemetryRecorder{}
	restore := telemetry.SetDefaultForTesting(recorder)
	defer restore()

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			if callCount == 1 {
				return &StepOutcome{
					AutoFixable: true,
					Findings:    `{"findings":[{"severity":"error","description":"fix me","action":"auto-fix"}],"summary":"1 issue"}`,
				}, nil
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	cfg := &config.Config{Agent: types.AgentClaude, AutoFix: config.AutoFix{Review: 1}}
	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)

	if err := exec.Execute(context.Background(), run, repo, workDir); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	fixEvent := recorder.find("fix", "source", "auto")
	if fixEvent == nil {
		t.Fatal("expected auto-fix telemetry event")
	}
	if got := fixEvent.fields["step"]; got != string(types.StepReview) {
		t.Fatalf("fix step = %v, want %q", got, types.StepReview)
	}
	if got := fixEvent.fields["selected_findings_count"]; fmt.Sprint(got) != "1" {
		t.Fatalf("fix selected_findings_count = %v, want 1", got)
	}
	if got := fixEvent.fields["attempt"]; fmt.Sprint(got) != "1" {
		t.Fatalf("fix attempt = %v, want 1", got)
	}
}
