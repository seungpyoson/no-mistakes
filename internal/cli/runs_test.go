package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestPrintRunLineShowsErrorCode(t *testing.T) {
	code := string(types.FailureModelFixLoop)
	run := &db.Run{
		Branch:    "feature/review-loop",
		HeadSHA:   "abcdef123456",
		Status:    types.RunFailed,
		ErrorCode: &code,
		CreatedAt: 1700000000,
	}

	var out bytes.Buffer
	printRunLine(&out, run)

	got := out.String()
	if !strings.Contains(got, "error_code=model_fix_loop") {
		t.Fatalf("expected error code in run line, got: %q", got)
	}
}

func TestRunsHelpMentionsErrorCode(t *testing.T) {
	out, err := executeCmd("runs", "--help")
	if err != nil {
		t.Fatalf("runs --help error = %v", err)
	}
	if !strings.Contains(out, "error_code") {
		t.Fatalf("runs --help should mention error_code, got:\n%s", out)
	}
}
