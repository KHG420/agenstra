"""Versioned state for the same ReAct loop in memory and in a durable host."""

from typing import Literal
from uuid import UUID, uuid4

from pydantic import BaseModel, ConfigDict, Field, JsonValue

from agenstra.contracts import Fact, Observation, ToolCall
from agenstra.providers import OperationBinding

RunStatus = Literal[
    "queued",
    "running",
    "completed",
    "needs_input",
    "failed",
    "waiting",
    "needs_approval",
    "cancelled",
    "needs_authorization",
    "needs_reconciliation",
]


class OperationReceipt(BaseModel):
    model_config = ConfigDict(extra="forbid")
    operation_id: str
    binding: OperationBinding
    poll_arguments: dict[str, JsonValue]
    next_poll_at: float
    deadline: float
    polls: int = 0


class Invocation(BaseModel):
    model_config = ConfigDict(extra="forbid")
    invocation_id: str
    call: ToolCall
    original_arguments: dict[str, JsonValue]
    arguments_sha256: str = ""
    status: Literal[
        "prepared", "needs_approval", "in_flight", "succeeded", "failed", "unknown", "waiting"
    ] = "prepared"
    approved_until: float | None = None
    approval_expires_at: float | None = None
    approved_hash: str | None = None
    attempts: int = 0
    error_code: str | None = None
    fact_id: UUID | None = None
    operation: OperationReceipt | None = None
    # A persisted poll uses the same attempt ID after recovery; a new poll gets a new ID.
    poll_in_flight: bool = False


class RuntimeState(BaseModel):
    model_config = ConfigDict(extra="forbid")
    schema_version: Literal[1] = 1
    run_id: str = Field(default_factory=lambda: str(uuid4()))
    instruction: str
    status: RunStatus = "queued"
    facts: list[Fact] = Field(default_factory=list)
    observations: list[Observation] = Field(default_factory=list)
    model_observations: list[Observation] = Field(default_factory=list)
    decisions: list[dict[str, JsonValue]] = Field(default_factory=list)
    used_refs: list[str] = Field(default_factory=list)
    repeated: dict[str, int] = Field(default_factory=dict)
    loaded_skills: list[str] = Field(default_factory=list)
    followups: list[str] = Field(default_factory=list)
    inspected_capability: str | None = None
    inspected_fact: dict[str, JsonValue] | None = None
    rounds_used: int = 0
    tool_calls_used: int = 0
    poll_calls_used: int = 0
    pending: list[Invocation] = Field(default_factory=list)
    answer_markdown: str = ""
    error_code: str | None = None
    input_field: str | None = None
    input_prompt: str | None = None
