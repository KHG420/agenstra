"""Public, domain-neutral contracts shared by a capability pack and the ReAct loop."""

import math
from typing import Annotated, Any, Literal, Protocol
from uuid import UUID

from pydantic import (
    AwareDatetime,
    BaseModel,
    ConfigDict,
    Field,
    JsonValue,
    TypeAdapter,
    model_validator,
)


def _finite_numbers(value: Any) -> None:
    if isinstance(value, float) and not math.isfinite(value):
        raise ValueError("JSON numbers must be finite")
    if isinstance(value, dict):
        for item in value.values():
            _finite_numbers(item)
    elif isinstance(value, list | tuple):
        for item in value:
            _finite_numbers(item)


class StrictModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, allow_inf_nan=False)

    @model_validator(mode="before")
    @classmethod
    def finite_json(cls, value: Any) -> Any:
        # Pydantic JsonValue accepts NaN independently of model allow_inf_nan.
        # Reject it before hashing or saving a decision/result as canonical JSON.
        _finite_numbers(value)
        return value


class RestField(StrictModel):
    type: Literal["string", "integer", "number", "boolean", "object", "array"]
    description: str = Field(min_length=1, max_length=300)
    required: bool = True


class RestCapability(StrictModel):
    name: str = Field(pattern=r"^[a-z][a-z0-9_.-]{1,127}$")
    version: str = Field(min_length=1, max_length=40)
    description: str = Field(min_length=1, max_length=500)
    method: Literal["GET", "POST"] = "POST"
    url_env: str = Field(pattern=r"^[A-Z][A-Z0-9_]*$")
    token_env: str | None = Field(default=None, pattern=r"^[A-Z][A-Z0-9_]*$")
    response_path: tuple[str | int, ...] = Field(default=(), max_length=8)
    inputs: dict[str, RestField]
    outputs: dict[str, RestField] | None = Field(default=None, min_length=1, max_length=32)
    timeout_seconds: float = Field(default=20, gt=0, le=300)
    max_attempts: int = Field(default=1, ge=1, le=3)


class PackManifest(StrictModel):
    schema_: Literal["agenstra.capability-pack.v1"] = Field(alias="schema")
    name: str = Field(min_length=1, max_length=80)
    guidance: str = Field(min_length=1, max_length=8_000)
    capabilities: tuple[RestCapability, ...] = Field(min_length=1)


class Fact(StrictModel):
    fact_id: UUID
    source_capability: str
    source_version: str
    value: dict[str, JsonValue]
    quality: Literal["provider_reported"] = "provider_reported"
    observed_at: AwareDatetime
    reference_scope: Literal["durable", "connection"] = "durable"
    connection_id: str | None = None
    expires_at: AwareDatetime | None = None


class FactView(Fact):
    """A bounded model view; referenced values still resolve against the original Fact."""

    omitted_paths: tuple[tuple[str | int, ...], ...] = ()
    reference_available: bool = True


class Observation(StrictModel):
    call_ref: str
    capability: str
    status: Literal["succeeded", "failed", "rejected"]
    fact_id: UUID | None = None
    error_code: str | None = None
    arguments: dict[str, JsonValue] = Field(default_factory=dict)
    arguments_omitted: bool = False


class ContextPacket(StrictModel):
    schema_: Literal["agenstra.context.v1"] = Field(default="agenstra.context.v1", alias="schema")
    instruction: str
    capabilities: tuple[dict[str, JsonValue], ...]
    facts: tuple[FactView, ...]
    observations: tuple[Observation, ...]
    round_index: int
    rounds_remaining: int
    tool_calls_remaining: int
    skills: tuple[dict[str, JsonValue], ...] = ()
    loaded_skills: dict[str, str] = Field(default_factory=dict)
    inspected_capability: dict[str, JsonValue] | None = None
    inspected_fact: dict[str, JsonValue] | None = None
    followups: tuple[str, ...] = ()
    runtime_features: tuple[str, ...] = ()
    context_omissions: tuple[str, ...] = ()


class DecisionBase(StrictModel):
    schema_: Literal["agenstra.decision.v1"] = Field(default="agenstra.decision.v1", alias="schema")


class ToolCall(StrictModel):
    call_ref: str = Field(pattern=r"^[a-z][a-z0-9-]{0,63}$")
    capability: str = Field(min_length=1, max_length=128)
    arguments: dict[str, JsonValue]
    reason: str = Field(min_length=1, max_length=500)


class ToolBatchDecision(DecisionBase):
    kind: Literal["tool_batch"] = "tool_batch"
    calls: tuple[ToolCall, ...] = Field(min_length=1, max_length=4)


class FinalDecision(DecisionBase):
    kind: Literal["final"] = "final"
    answer_markdown: str = Field(min_length=1, max_length=30_000)
    fact_ids: tuple[UUID, ...] = Field(default=(), max_length=50)


class RequestInputDecision(DecisionBase):
    kind: Literal["request_input"] = "request_input"
    field: str = Field(pattern=r"^[a-z][a-z0-9_]{0,63}$")
    prompt: str = Field(min_length=1, max_length=1_000)


class ReadSkillDecision(DecisionBase):
    kind: Literal["read_skill"] = "read_skill"
    name: str = Field(min_length=1, max_length=128)


class InspectCapabilityDecision(DecisionBase):
    kind: Literal["inspect_capability"] = "inspect_capability"
    name: str = Field(min_length=1, max_length=128)


class InspectFactDecision(DecisionBase):
    kind: Literal["inspect_fact"] = "inspect_fact"
    fact_id: UUID
    path: tuple[str | int, ...] = Field(default=(), max_length=16)


Decision = Annotated[
    ToolBatchDecision
    | FinalDecision
    | RequestInputDecision
    | ReadSkillDecision
    | InspectCapabilityDecision
    | InspectFactDecision,
    Field(discriminator="kind"),
]
DECISION_ADAPTER: TypeAdapter[Decision] = TypeAdapter(Decision)


class DecisionModel(Protocol):
    async def decide(self, *, context: ContextPacket, system_prompt: str) -> Decision: ...


class RunResult(StrictModel):
    status: Literal[
        "completed",
        "needs_input",
        "failed",
        "waiting",
        "needs_approval",
        "cancelled",
        "needs_authorization",
        "needs_reconciliation",
    ]
    answer_markdown: str = ""
    error_code: str | None = None
    input_field: str | None = None
    input_prompt: str | None = None
    facts: tuple[Fact, ...] = ()
    observations: tuple[Observation, ...] = ()
    decisions: tuple[dict[str, JsonValue], ...] = ()
