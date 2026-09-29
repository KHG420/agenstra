"""A transport-neutral ReAct state machine, shared by transient and durable execution."""

import asyncio
import hashlib
import json
from collections.abc import Awaitable, Callable, Mapping
from copy import deepcopy
from datetime import UTC, datetime
from typing import cast
from uuid import NAMESPACE_URL, UUID, uuid4, uuid5

from pydantic import JsonValue, ValidationError

from agenstra.broker import CallOutcome, ToolBroker
from agenstra.context import fact_view
from agenstra.contracts import (
    DECISION_ADAPTER,
    ContextPacket,
    DecisionModel,
    Fact,
    FinalDecision,
    InspectCapabilityDecision,
    InspectFactDecision,
    Observation,
    ReadSkillDecision,
    RequestInputDecision,
    RunResult,
    ToolBatchDecision,
    ToolCall,
)
from agenstra.model import ModelDecisionError
from agenstra.providers import CapabilityProvider, InvocationContext
from agenstra.state import Invocation, RuntimeState


class FactReferenceError(ValueError):
    pass


def reference_available(fact: Fact, connection_id: str | None = None) -> bool:
    if fact.expires_at is not None and fact.expires_at <= datetime.now(UTC):
        return False
    return fact.reference_scope != "connection" or fact.connection_id == connection_id


def resolve_argument(
    value: JsonValue,
    facts: Mapping[UUID, Fact],
    *,
    connection_id: str | None = None,
    check_availability: bool = True,
) -> JsonValue:
    """Resolve references against authorized run results; never reinterpret returned data."""
    if isinstance(value, list):
        return [
            resolve_argument(
                item, facts, connection_id=connection_id, check_availability=check_availability
            )
            for item in value
        ]
    if not isinstance(value, dict):
        return value
    if not ({"$fact_id", "$fact_value"} & value.keys()):
        return {
            key: resolve_argument(
                item, facts, connection_id=connection_id, check_availability=check_availability
            )
            for key, item in value.items()
        }
    if len(value) != 1:
        raise FactReferenceError("fact_reference_invalid")
    if "$fact_id" in value:
        raw_id = value["$fact_id"]
        path: list[JsonValue] | None = None
    else:
        reference = value["$fact_value"]
        if not isinstance(reference, dict) or set(reference) != {"fact_id", "path"}:
            raise FactReferenceError("fact_reference_invalid")
        raw_id, raw_path = reference["fact_id"], reference["path"]
        if not isinstance(raw_path, list) or len(raw_path) > 16:
            raise FactReferenceError("fact_reference_invalid")
        path = raw_path
    if not isinstance(raw_id, str):
        raise FactReferenceError("fact_reference_invalid")
    try:
        fact_id = UUID(raw_id)
    except ValueError as exc:
        raise FactReferenceError("fact_reference_invalid") from exc
    fact = facts.get(fact_id)
    if fact is None:
        raise FactReferenceError("fact_reference_not_available")
    if check_availability and not reference_available(fact, connection_id):
        raise FactReferenceError("fact_reference_expired")
    if path is None:
        return str(fact_id)
    selected: JsonValue = fact.value
    for step in path:
        if isinstance(selected, dict) and isinstance(step, str) and step in selected:
            selected = selected[step]
            continue
        if isinstance(selected, list) and type(step) is int and 0 <= step < len(selected):
            selected = selected[step]
            continue
        raise FactReferenceError("fact_reference_path_invalid")
    return deepcopy(selected)


def arguments_digest(call: ToolCall) -> str:
    payload = json.dumps(
        {"capability": call.capability, "arguments": call.arguments},
        sort_keys=True,
        ensure_ascii=False,
        separators=(",", ":"),
        allow_nan=False,
    )
    return hashlib.sha256(payload.encode()).hexdigest()


class AgentRuntime:
    def __init__(
        self,
        *,
        pack: CapabilityProvider,
        model: DecisionModel,
        granted_capabilities: frozenset[str] = frozenset(),
        max_model_rounds: int = 20,
        max_tool_calls: int = 40,
        max_repeated_call: int = 2,
        max_context_characters: int = 80_000,
        connection_id: str | None = None,
        durable: bool = False,
    ) -> None:
        if min(max_model_rounds, max_tool_calls, max_repeated_call, max_context_characters) < 1:
            raise ValueError("runtime budgets must be positive")
        self.pack = pack
        self.model = model
        self.grants = granted_capabilities
        self.connection_id = connection_id or str(uuid4())
        self.durable = durable
        self.max_model_rounds = max_model_rounds
        self.max_tool_calls = max_tool_calls
        self.max_repeated_call = max_repeated_call
        self.max_context_characters = max_context_characters

    @staticmethod
    def new_state(instruction: str, *, run_id: str | None = None) -> RuntimeState:
        if not instruction.strip():
            raise ValueError("instruction must not be empty")
        return RuntimeState(instruction=instruction, run_id=run_id or str(uuid4()))

    def context(self, state: RuntimeState) -> ContextPacket:
        inspected = self.pack.capabilities.get(state.inspected_capability or "")
        fact_budget = min(
            6000, max(0, self.max_context_characters // 3 // max(1, len(state.facts)))
        )
        omissions: list[str] = []
        observations = []
        for item in state.model_observations[-12:]:
            if len(json.dumps(item.arguments, ensure_ascii=False)) > 2000:
                item = item.model_copy(update={"arguments": {}, "arguments_omitted": True})
            observations.append(item)
        if len(state.model_observations) > 12:
            omissions.append(f"observations: {len(state.model_observations) - 12} older entries")
        packet = ContextPacket(
            instruction=state.instruction,
            capabilities=tuple(
                item.model_view()
                | {
                    "authorized": item.effect == "read" or item.name in self.grants,
                }
                for item in self.pack.capabilities.values()
            ),
            facts=tuple(
                fact_view(fact, max_characters=fact_budget).model_copy(
                    update={
                        "reference_available": reference_available(fact, self.connection_id),
                    }
                )
                for fact in state.facts
            ),
            observations=tuple(observations),
            round_index=state.rounds_used,
            rounds_remaining=self.max_model_rounds - state.rounds_used,
            tool_calls_remaining=self.max_tool_calls - state.tool_calls_used,
            skills=tuple(
                skill.description.model_dump(mode="json") for skill in self.pack.skills.values()
            ),
            loaded_skills={
                name: self.pack.skills[name].content
                for name in state.loaded_skills
                if name in self.pack.skills
            },
            inspected_capability=inspected.model_dump(mode="json") if inspected else None,
            inspected_fact=state.inspected_fact,
            followups=tuple(state.followups),
            runtime_features=("durable_execution",) if self.durable else (),
            context_omissions=tuple(omissions),
        )
        # Keep all fact identities but reduce previews, then evict the oldest loaded skills.
        # Complete results and observations stay in the durable state and artifact store.
        available = self.max_context_characters - len(self.pack.system_prompt())
        if len(packet.model_dump_json()) > available:
            packet = packet.model_copy(
                update={
                    "facts": tuple(
                        fact_view(fact, max_characters=0).model_copy(
                            update={
                                "reference_available": reference_available(
                                    fact, self.connection_id
                                ),
                            }
                        )
                        for fact in state.facts
                    )
                }
            )
        loaded = dict(packet.loaded_skills)
        while loaded and len(packet.model_dump_json()) > available:
            name = next(iter(loaded))
            del loaded[name]
            omissions.append(f"skill: {name}; read_skill to load again")
            packet = packet.model_copy(
                update={"loaded_skills": dict(loaded), "context_omissions": tuple(omissions)}
            )
        return packet

    @staticmethod
    def reject(
        state: RuntimeState,
        call_ref: str,
        capability: str,
        code: str,
        arguments: dict[str, JsonValue] | None = None,
    ) -> None:
        observation = Observation(
            call_ref=call_ref,
            capability=capability,
            status="rejected",
            error_code=code,
            arguments=arguments or {},
        )
        state.observations.append(observation)
        state.model_observations.append(observation)

    async def step(
        self, state: RuntimeState, *, before_model: Callable[[], Awaitable[None]] | None = None
    ) -> None:
        """One model decision. Caller checkpoints pending calls before performing any IO."""
        if state.pending or state.status not in {"queued", "running"}:
            return
        state.status = "running"
        if state.rounds_used >= self.max_model_rounds:
            state.status, state.error_code = "failed", "model_round_budget_exhausted"
            return
        packet = self.context(state)
        prompt = self.pack.system_prompt()
        if len(prompt) + len(packet.model_dump_json()) > self.max_context_characters:
            state.status, state.error_code = "failed", "context_too_large"
            return
        # Charge before invoking the model; durable caller saves the resulting state or a failure.
        state.rounds_used += 1
        if before_model is not None:
            await before_model()
        try:
            decision = DECISION_ADAPTER.validate_python(
                await self.model.decide(context=packet, system_prompt=prompt)
            )
        except ModelDecisionError as exc:
            state.status, state.error_code = "failed", exc.code
            return
        except ValidationError:
            state.status, state.error_code = "failed", "model_decision_invalid"
            return
        state.decisions.append(
            cast(dict[str, JsonValue], decision.model_dump(mode="json", by_alias=True))
        )
        facts = {fact.fact_id: fact for fact in state.facts}
        if isinstance(decision, InspectFactDecision):
            try:
                selected = resolve_argument(
                    {
                        "$fact_value": {
                            "fact_id": str(decision.fact_id),
                            "path": list(decision.path),
                        }
                    },
                    facts,
                    connection_id=self.connection_id,
                    check_availability=False,
                )
                preview = fact_view(
                    facts[decision.fact_id].model_copy(update={"value": {"value": selected}})
                )
                state.inspected_fact = {
                    "fact_id": str(decision.fact_id),
                    "path": list(decision.path),
                    "preview": preview.value,
                    "omitted_paths": [list(path) for path in preview.omitted_paths],
                }
            except FactReferenceError as exc:
                state.inspected_fact = {"error_code": str(exc)}
        elif isinstance(decision, InspectCapabilityDecision):
            state.inspected_capability = decision.name
            if decision.name not in self.pack.capabilities:
                self.reject(state, "inspect", "agent.inspect_capability", "capability_unknown")
        elif isinstance(decision, ReadSkillDecision):
            if decision.name not in self.pack.skills:
                self.reject(state, "skill", "agent.read_skill", "skill_unknown")
            else:
                if decision.name in state.loaded_skills:
                    state.loaded_skills.remove(decision.name)
                state.loaded_skills.append(decision.name)
        elif isinstance(decision, FinalDecision):
            cited = set(decision.fact_ids)
            if (facts and not cited) or not cited.issubset(facts):
                self.reject(state, "final", "agent.final", "final_fact_citations_invalid")
            else:
                state.status, state.answer_markdown = "completed", decision.answer_markdown
        elif isinstance(decision, RequestInputDecision):
            state.status = "needs_input"
            state.input_field, state.input_prompt = decision.field, decision.prompt
        elif isinstance(decision, ToolBatchDecision):
            for call in decision.calls:
                if call.call_ref in state.used_refs:
                    self.reject(
                        state,
                        call.call_ref,
                        call.capability,
                        "tool_call_ref_reused",
                        call.arguments,
                    )
                    continue
                state.used_refs.append(call.call_ref)
                try:
                    arguments = {
                        name: resolve_argument(value, facts, connection_id=self.connection_id)
                        for name, value in call.arguments.items()
                    }
                except FactReferenceError as exc:
                    self.reject(state, call.call_ref, call.capability, str(exc), call.arguments)
                    continue
                resolved = call.model_copy(update={"arguments": arguments})
                key = arguments_digest(resolved)
                if state.repeated.get(key, 0) >= self.max_repeated_call:
                    self.reject(
                        state,
                        call.call_ref,
                        call.capability,
                        "repeated_equivalent_call",
                        call.arguments,
                    )
                    continue
                state.repeated[key] = state.repeated.get(key, 0) + 1
                state.pending.append(
                    Invocation(
                        invocation_id=str(uuid5(NAMESPACE_URL, f"{state.run_id}:{call.call_ref}")),
                        call=resolved,
                        original_arguments=deepcopy(call.arguments),
                    )
                )
            if state.tool_calls_used + len(state.pending) > self.max_tool_calls:
                state.pending.clear()
                state.status, state.error_code = "failed", "tool_call_budget_exhausted"
            else:
                state.tool_calls_used += len(state.pending)

    @staticmethod
    def observe(state: RuntimeState, invocation: Invocation, outcome: CallOutcome) -> None:
        if outcome.fact is not None:
            state.facts.append(outcome.fact)
            invocation.fact_id = outcome.fact.fact_id
        observation = Observation(
            call_ref=invocation.call.call_ref,
            capability=invocation.call.capability,
            status="succeeded" if outcome.fact is not None else "failed",
            fact_id=outcome.fact.fact_id if outcome.fact else None,
            error_code=outcome.error_code,
            arguments=invocation.call.arguments,
        )
        state.observations.append(observation)
        state.model_observations.append(
            observation.model_copy(update={"arguments": invocation.original_arguments})
        )
        invocation.status = "succeeded" if outcome.fact is not None else "failed"
        invocation.error_code = outcome.error_code

    @staticmethod
    def result(state: RuntimeState) -> RunResult:
        status = state.status if state.status not in {"running", "queued"} else "failed"
        return RunResult(
            status=status,
            answer_markdown=state.answer_markdown,
            error_code=state.error_code,
            input_field=state.input_field,
            input_prompt=state.input_prompt,
            facts=tuple(state.facts),
            observations=tuple(state.observations),
            decisions=tuple(state.decisions),
        )

    async def run(self, instruction: str) -> RunResult:
        """In-memory execution; AgentHost persists and resumes this same state machine."""
        state = self.new_state(instruction)
        broker = ToolBroker(self.pack, granted_capabilities=self.grants)
        while state.status in {"queued", "running"}:
            await self.step(state)
            if state.pending:
                pending = tuple(state.pending)
                if any(
                    self.pack.capabilities[item.call.capability].approval_required
                    for item in pending
                    if item.call.capability in self.pack.capabilities
                ):
                    state.status, state.error_code = "needs_approval", "durable_host_required"
                    break
                outcomes = await asyncio.gather(
                    *(
                        broker.execute(
                            item.call,
                            context=InvocationContext(
                                run_id=state.run_id,
                                invocation_id=item.invocation_id,
                                idempotency_key=item.invocation_id,
                                owner_id="transient",
                                connection_id=self.connection_id,
                            ),
                        )
                        for item in pending
                    )
                )
                for invocation, outcome in zip(pending, outcomes, strict=True):
                    self.observe(state, invocation, outcome)
                    capability = self.pack.capabilities.get(invocation.call.capability)
                    if outcome.error_code == "provider_outcome_unknown" or (
                        outcome.error_code in {"upstream_response_invalid", "upstream_unavailable"}
                        and capability is not None
                        and capability.effect != "read"
                    ):
                        state.status = "needs_reconciliation"
                        state.error_code = "provider_outcome_unknown"
                state.pending.clear()
        return self.result(state)
