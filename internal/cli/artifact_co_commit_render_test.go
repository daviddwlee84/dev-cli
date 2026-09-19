package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/daviddwlee84/dev-cli/internal/artifact"
)

func testQueueAck() *artifact.CoCommitQueueAck {
	return &artifact.CoCommitQueueAck{
		Kind: "agent_history_queue_ack", SchemaVersion: 2,
		RequestID:       "9bd5ed58-1d35-47c8-b3b7-20445e6d7489",
		RequestPath:     "/fixture/.git/agent-history-hygiene/runs/9bd5ed58-1d35-47c8-b3b7-20445e6d7489/request.json",
		RequestRevision: strings.Repeat("a", 64), HelperRevision: strings.Repeat("b", 64),
	}
}

func TestCoCommitHumanOutputRetainsCanonicalQueueAck(t *testing.T) {
	var out bytes.Buffer
	app := &App{Out: &out}
	ack := testQueueAck()
	result := &artifact.CoCommitResult{Kind: "co_commit_handoff", SchemaVersion: 1, Status: "queued", RequestID: ack.RequestID, QueueAck: ack, NextAction: "report_finalization_queued_then_exit_agent"}
	if err := renderCoCommitResult(app, result, nil, false); err != nil {
		t.Fatal(err)
	}
	line, rest, found := strings.Cut(out.String(), "\n")
	var decoded artifact.CoCommitQueueAck
	if !found || json.Unmarshal([]byte(line), &decoded) != nil || decoded != *ack {
		t.Fatal("human output did not retain one exact typed queue acknowledgement")
	}
	if !strings.Contains(rest, "finalization queued") || !strings.Contains(rest, "do not perform more repository/index operations") {
		t.Fatalf("missing post-queue boundary: %s", rest)
	}
}

func TestCoCommitJSONRetainsPartialAckAndSanitizesError(t *testing.T) {
	const privateDiagnostic = "synthetic-private-native-diagnostic"
	var out bytes.Buffer
	app := &App{Out: &out}
	ack := testQueueAck()
	result := &artifact.CoCommitResult{Kind: "co_commit_handoff", SchemaVersion: 1, Status: "queued_but_not_bound", RequestID: ack.RequestID, QueueAck: ack, NextAction: "exit_agent_then_repair_exact_binding"}
	err := renderCoCommitResult(app, result, errors.New(privateDiagnostic), true)
	if err == nil || strings.Contains(err.Error(), privateDiagnostic) || strings.Contains(out.String(), privateDiagnostic) {
		t.Fatal("private diagnostic leaked or failure disappeared")
	}
	var decoded struct {
		Kind     string                     `json:"kind"`
		Status   string                     `json:"status"`
		QueueAck *artifact.CoCommitQueueAck `json:"queue_ack"`
		Error    *artifact.CoCommitError    `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != "co_commit_handoff" || decoded.Status != "queued_but_not_bound" || decoded.QueueAck == nil || *decoded.QueueAck != *ack || decoded.Error == nil || decoded.Error.Code != "observation_unavailable" {
		t.Fatalf("partial result lost: %+v", decoded)
	}
}

func TestCoCommitJSONFailureBeforeQueueDoesNotInventAck(t *testing.T) {
	var out bytes.Buffer
	app := &App{Out: &out}
	err := renderCoCommitResult(app, nil, &artifact.CoCommitError{Code: "requires_wrapper", NextAction: "start_real_wrapper_with_explicit_approval"}, true)
	if err == nil {
		t.Fatal("missing wrapper should fail")
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["queue_ack"]; exists || decoded["status"] != "blocked" {
		t.Fatalf("pre-queue failure invented a receipt: %+v", decoded)
	}
}

func TestCoCommitNativeRevisionErrorUsesFinitePublicCode(t *testing.T) {
	var out bytes.Buffer
	app := &App{Out: &out}
	err := renderCoCommitResult(app, nil, artifact.ErrStaleRevision, true)
	var public *artifact.CoCommitError
	if !errors.As(err, &public) || public.Code != "stale_native_revision" || !strings.Contains(out.String(), "stale_native_revision") {
		t.Fatalf("stale authority error=%v output=%s", err, out.String())
	}
}
