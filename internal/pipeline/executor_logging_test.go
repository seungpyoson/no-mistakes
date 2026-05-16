package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExecutor_LogCallback(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	var logMessages []string
	var mu sync.Mutex

	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			if sctx.Log != nil {
				sctx.Log("hello from review")
			}
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	onEvent := func(e ipc.Event) {
		if e.Type == ipc.EventLogChunk && e.Content != nil {
			mu.Lock()
			logMessages = append(logMessages, *e.Content)
			mu.Unlock()
		}
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, onEvent)
	exec.Execute(context.Background(), run, repo, workDir)

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, msg := range logMessages {
		if strings.TrimSpace(msg) == "hello from review" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected log message 'hello from review' in events, got: %v", logMessages)
	}
}

func TestExecutor_LogVsLogChunk(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	var chunks []string
	var mu sync.Mutex

	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			// Streaming chunk without trailing newline (simulates agent SSE delta).
			sctx.LogChunk("streaming partial")
			// Discrete message after unterminated stream - should get leading \n.
			sctx.Log("after stream")
			// Consecutive discrete message - no leading \n needed.
			sctx.Log("second discrete")
			// Raw streaming chunk should pass through unchanged.
			sctx.LogChunk("raw chunk")
			return &StepOutcome{ExitCode: 0}, nil
		},
	}

	onEvent := func(e ipc.Event) {
		if e.Type == ipc.EventLogChunk && e.Content != nil {
			mu.Lock()
			chunks = append(chunks, *e.Content)
			mu.Unlock()
		}
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, onEvent)
	exec.Execute(context.Background(), run, repo, workDir)

	mu.Lock()
	defer mu.Unlock()

	want := []string{
		"streaming partial",   // raw chunk, no newline
		"\nafter stream\n\n",  // leading \n flushes partial, trailing \n\n separates
		"second discrete\n\n", // no leading \n (previous Log ended with \n)
		"raw chunk",           // raw chunk, unchanged
	}
	if len(chunks) != len(want) {
		t.Fatalf("expected %d chunks, got %d: %q", len(want), len(chunks), chunks)
	}
	for i, w := range want {
		if chunks[i] != w {
			t.Errorf("chunks[%d] = %q, want %q", i, chunks[i], w)
		}
	}
}

func TestExecutor_RunLogDir(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	exec := NewExecutor(database, p, nil, nil, []Step{newPassStep(types.StepReview)}, nil)
	exec.Execute(context.Background(), run, repo, workDir)

	// Verify log dir was created
	logDir := p.RunLogDir(run.ID)
	if !dirExists(logDir) {
		t.Errorf("expected run log dir to exist: %s", logDir)
	}

	// Verify step log_path is set
	dbSteps, _ := database.GetStepsByRun(run.ID)
	if dbSteps[0].LogPath == nil {
		t.Fatal("expected log_path to be set")
	}
	expected := filepath.Join(logDir, "review.log")
	if *dbSteps[0].LogPath != expected {
		t.Errorf("expected log_path %q, got %q", expected, *dbSteps[0].LogPath)
	}
}

func TestExecutor_LogFileWritten(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			sctx.Log("first log line")
			sctx.Log("second log line")
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)
	exec.Execute(context.Background(), run, repo, workDir)

	// Verify log file exists and contains the log messages
	logPath := filepath.Join(p.RunLogDir(run.ID), "review.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("expected log file at %s: %v", logPath, err)
	}
	content := string(data)
	if !strings.Contains(content, "first log line") {
		t.Errorf("expected log file to contain 'first log line', got: %s", content)
	}
	if !strings.Contains(content, "second log line") {
		t.Errorf("expected log file to contain 'second log line', got: %s", content)
	}
}

func TestExecutor_LogFileMultipleSteps(t *testing.T) {
	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	step1 := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			sctx.Log("review message")
			return &StepOutcome{}, nil
		},
	}
	step2 := &adaptiveCallStep{
		name: types.StepTest,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			sctx.Log("test message")
			return &StepOutcome{}, nil
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step1, step2}, nil)
	exec.Execute(context.Background(), run, repo, workDir)

	// Each step should have its own log file
	reviewLog, err := os.ReadFile(filepath.Join(p.RunLogDir(run.ID), "review.log"))
	if err != nil {
		t.Fatalf("expected review log file: %v", err)
	}
	if !strings.Contains(string(reviewLog), "review message") {
		t.Errorf("review log missing message, got: %s", reviewLog)
	}

	testLog, err := os.ReadFile(filepath.Join(p.RunLogDir(run.ID), "test.log"))
	if err != nil {
		t.Fatalf("expected test log file: %v", err)
	}
	if !strings.Contains(string(testLog), "test message") {
		t.Errorf("test log missing message, got: %s", testLog)
	}

	// Review log should NOT contain test message
	if strings.Contains(string(reviewLog), "test message") {
		t.Error("review log should not contain test message")
	}
}

func TestExecutor_LogsAutoFixBlockedAfterUserFix(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(oldLogger)

	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	initGitRepo(t, workDir)

	cfg := &config.Config{AutoFix: config.AutoFix{Review: 3}}

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			switch callCount {
			case 1:
				return &StepOutcome{
					NeedsApproval: true,
					AutoFixable:   true,
					Findings: `{"findings":[
						{"id":"F1","severity":"warning","description":"approval state needs a guard","action":"ask-user"}
					],"summary":"1 issue"}`,
				}, nil
			case 2:
				return &StepOutcome{
					NeedsApproval: true,
					AutoFixable:   true,
					Findings: `{"findings":[
						{"id":"F1","severity":"warning","description":"approval state still needs a guard","action":"ask-user"},
						{"id":"F2","severity":"info","description":"unrelated auto fix","action":"auto-fix"}
					],"summary":"2 issues"}`,
				}, nil
			default:
				t.Fatalf("unexpected extra execution after auto-fix should have been blocked")
				return nil, nil
			}
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatus(t, database, run.ID, types.StepReview, types.StepStatusAwaitingApproval)

	if err := exec.Respond(types.StepReview, types.ActionFix, []string{"F1"}); err != nil {
		t.Fatalf("respond fix: %v", err)
	}

	waitForStepStatusOrDone(t, database, run.ID, types.StepReview, types.StepStatusFixReview, done)

	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatalf("respond approve: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	output := logs.String()
	for _, want := range []string{
		`"msg":"review_auto_fix_blocked_after_user_fix"`,
		`"run_id":"` + run.ID + `"`,
		`"step":"review"`,
		`"round":2`,
		`"last_fix_source":"user"`,
		`"ask_user_count":1`,
		`"auto_fix_count":1`,
		`"auto_fix_attempts":0`,
		`"auto_fix_limit":3`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected log output to contain %s, got:\n%s", want, output)
		}
	}
}

func TestExecutor_LogsAutoFixSelectedAndApprovalRequested(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(oldLogger)

	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	initGitRepo(t, workDir)

	cfg := &config.Config{AutoFix: config.AutoFix{Review: 3}}

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			switch callCount {
			case 1:
				return &StepOutcome{
					AutoFixable: true,
					Findings: `{"findings":[
						{"id":"F1","severity":"warning","description":"auto fix this","action":"auto-fix"}
					],"summary":"1 issue"}`,
				}, nil
			case 2:
				return &StepOutcome{
					NeedsApproval: true,
					Findings: `{"findings":[
						{"id":"F2","severity":"warning","description":"needs review","action":"ask-user"}
					],"summary":"1 issue"}`,
				}, nil
			default:
				t.Fatalf("unexpected extra execution")
				return nil, nil
			}
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	waitForStepStatusOrDone(t, database, run.ID, types.StepReview, types.StepStatusFixReview, done)

	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatalf("respond approve: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	output := logs.String()
	for _, want := range []string{
		`"msg":"review_auto_fix_selected"`,
		`"attempt":1`,
		`"auto_fix_limit":3`,
		`"auto_fix_count":1`,
		`"msg":"review_approval_requested"`,
		`"status":"fix_review"`,
		`"fixing":true`,
		`"ask_user_count":1`,
		`"auto_fix_attempts":1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected log output to contain %s, got:\n%s", want, output)
		}
	}
}

// TestExecutor_ModelFixLoopGatedOnAskUserRemaining is the FR-011 negative case:
// when the auto-fix limit is exhausted AND ask-user findings remain, the
// executor MUST NOT emit review_model_fix_loop. It must fall through to
// approval (review_approval_requested) so the operator can decide.
func TestExecutor_ModelFixLoopGatedOnAskUserRemaining(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(oldLogger)

	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	initGitRepo(t, workDir)

	cfg := &config.Config{AutoFix: config.AutoFix{Review: 1}}

	callCount := 0
	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			callCount++
			// Every round returns BOTH an ask-user finding and an auto-fix
			// finding. Round 1 triggers the (single allowed) auto-fix attempt;
			// round 2 hits the limit with ask-user findings still present.
			return &StepOutcome{
				NeedsApproval: true,
				AutoFixable:   true,
				Findings: `{"findings":[
					{"id":"F1","severity":"warning","description":"needs user","action":"ask-user"},
					{"id":"F2","severity":"info","description":"auto-fixable","action":"auto-fix"}
				],"summary":"2 issues"}`,
			}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)

	done := make(chan error, 1)
	go func() {
		done <- exec.Execute(context.Background(), run, repo, workDir)
	}()

	// After round 1 consumes the single auto-fix attempt, round 2 surfaces
	// the ask-user finding. Because round 1 was an auto-fix, sctx.Fixing is
	// true and approval status becomes FixReview, not AwaitingApproval.
	waitForStepStatusOrDone(t, database, run.ID, types.StepReview, types.StepStatusFixReview, done)
	if err := exec.Respond(types.StepReview, types.ActionAbort, nil); err != nil {
		t.Fatalf("respond abort: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("executor timed out")
	}

	output := logs.String()
	if strings.Contains(output, `"msg":"review_model_fix_loop"`) {
		t.Errorf("review_model_fix_loop MUST NOT fire when ask-user findings remain at the limit; got:\n%s", output)
	}
	if !strings.Contains(output, `"msg":"review_approval_requested"`) {
		t.Errorf("expected review_approval_requested when ask-user remains at limit; got:\n%s", output)
	}
}

func TestExecutor_LogsModelFixLoop(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(oldLogger)

	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()
	initGitRepo(t, workDir)

	cfg := &config.Config{AutoFix: config.AutoFix{Review: 1}}

	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			return &StepOutcome{
				AutoFixable: true,
				Findings: `{"findings":[
					{"id":"F1","severity":"warning","description":"same finding remains","action":"auto-fix"}
				],"summary":"1 issue"}`,
			}, nil
		},
	}

	exec := NewExecutor(database, p, cfg, nil, []Step{step}, nil)

	err := exec.Execute(context.Background(), run, repo, workDir)
	if err == nil {
		t.Fatal("expected model_fix_loop error")
	}

	output := logs.String()
	for _, want := range []string{
		`"msg":"review_model_fix_loop"`,
		`"run_id":"` + run.ID + `"`,
		`"step":"review"`,
		`"auto_fix_count":1`,
		`"auto_fix_attempts":1`,
		`"auto_fix_limit":1`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected log output to contain %s, got:\n%s", want, output)
		}
	}
}

func TestExecutor_LogsRunFailedWithErrorCode(t *testing.T) {
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(oldLogger)

	database, p, run, repo := setupTest(t)
	workDir := t.TempDir()

	step := &adaptiveCallStep{
		name: types.StepReview,
		fn: func(sctx *StepContext) (*StepOutcome, error) {
			return nil, errors.New("agent process crashed")
		},
	}

	exec := NewExecutor(database, p, nil, nil, []Step{step}, nil)

	err := exec.Execute(context.Background(), run, repo, workDir)
	if err == nil {
		t.Fatal("expected run failure")
	}

	output := logs.String()
	for _, want := range []string{
		`"msg":"run_failed_with_error_code"`,
		`"run_id":"` + run.ID + `"`,
		`"status":"failed"`,
		`"error_code":"tool_crash"`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected log output to contain %s, got:\n%s", want, output)
		}
	}
}
