package agenstra

import "github.com/KHG420/agenstra/internal/contract/agent"

// WorkflowDefinition declares a versioned DAG of existing capabilities and Agent tasks.
type WorkflowDefinition = agent.WorkflowDefinition

// WorkflowStep binds a capability or Agent task with dependencies and optional condition.
type WorkflowStep = agent.WorkflowStep

// WorkflowAgent declares a bounded Agent task and its allowed tool subset.
type WorkflowAgent = agent.WorkflowAgent

// WorkflowCondition compares a resolved JSON value with a literal value.
type WorkflowCondition = agent.WorkflowCondition

// WorkflowInfo is the authorized workflow catalog entry.
type WorkflowInfo = agent.WorkflowInfo

// WorkflowView contains progress, outputs and any run requiring user action.
type WorkflowView = agent.WorkflowView

// WorkflowStepState contains a persisted node's status and evidence references.
type WorkflowStepState = agent.WorkflowStepState
