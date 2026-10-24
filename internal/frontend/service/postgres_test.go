package service

import (
	"os"
	"testing"
)

// Reuse the same contract assertions as SQLite; do not replace the framework with a stub.
func TestPostgresWebParity(t *testing.T) {
	if os.Getenv("AGENSTRA_TEST_POSTGRES_DSN") == "" {
		t.Skip("set AGENSTRA_TEST_POSTGRES_DSN to a disposable PostgreSQL database")
	}
	t.Setenv("AGENSTRA_TEST_STORAGE_BACKEND", "postgres")
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"command_deduplication", TestBrowserCommandExecutionAndDeduplication},
		{"lost_ack", TestBrowserLostAckReconciliationAndGenerationFence},
		{"approval_revocation", TestBrowserApprovalAndLiveGrantRevocation},
		{"chat_queue_recovery", TestChatQueuePrebindingIdempotenceAndRecovery},
		{"chat_history", TestConversationContextLoadsHistoryAndInputFromFrameworkCheckpoints},
		{"chat_cancel_race", TestChatCancellationFencesConcurrentUnpublishedCreate},
		{"resume_fences", TestBrowserResumeRefreshesRunBindingAndRequiresFreshObservation},
		{"resume_concurrent", TestBrowserResumeConcurrentRetriesAdvanceOnce},
		{"recovery_unknown", TestBrowserRecoveryPreservesUnknownHistoryAndRequiresStoppedRun},
		{"recovery_concurrent", TestBrowserRecoveryConcurrentRetriesReturnOneReplacement},
		{"reconciliation", TestBrowserReconciliationSettlesStoppedEvidenceWithoutReplay},
		{"reconciliation_grants", TestBrowserReconciliationRechecksGrants},
		{"result_isolation", TestBrowserResultSchemaAndIsolation},
	} {
		t.Run(test.name, test.run)
	}
}
