package agenstra

import "github.com/KHG420/agenstra/internal/contract/agent"

// ConversationCursor resumes an exact original transcript range.
type ConversationCursor = agent.ConversationCursor

// ConversationEntry contains an exact bounded original range.
type ConversationEntry = agent.ConversationEntry

// ConversationRequirement retains a user quote and explicit revisions.
type ConversationRequirement = agent.ConversationRequirement

// ConversationContext is the durable run-owned conversation projection.
type ConversationContext = agent.ConversationContext

// ConversationView is the model-visible summary, requirements and recent originals.
type ConversationView = agent.ConversationView

// ConversationPage carries one original range and its continuation.
type ConversationPage = agent.ConversationPage

// ConversationHistory optionally supplies authenticated history to AgentHost.
type ConversationHistory = agent.ConversationHistory

// ConversationCompactionRequest supplies exact ranges and retained requirements.
type ConversationCompactionRequest = agent.ConversationCompactionRequest

// ConversationSummary is source-validated compaction output.
type ConversationSummary = agent.ConversationSummary

// ConversationCompactor optionally provides semantic compaction with usage receipts.
type ConversationCompactor = agent.ConversationCompactor
