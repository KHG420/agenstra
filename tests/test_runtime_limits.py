import json
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace
from uuid import uuid4

import pytest
from pydantic import ValidationError

from agenstra.context import fact_view
from agenstra.contracts import (
    DECISION_ADAPTER,
    Fact,
    FinalDecision,
    InspectFactDecision,
    Observation,
    ToolBatchDecision,
    ToolCall,
)
from agenstra.providers import (
    CapabilityDescription,
    CapabilityResult,
    Skill,
    SkillDescription,
)
from agenstra.runtime import AgentRuntime, arguments_digest, resolve_argument


def test_fact_preview_preserves_later_array_statuses_under_large_audit_fields():
    fact = Fact(
        fact_id=uuid4(),
        source_capability="monitor.batch",
        source_version="1",
        observed_at=datetime.now(UTC),
        value={
            "data": {
                "columns": ["column" * 50] * 100,
                "results": [
                    {"audit": {"trace": "x" * 12000}, "status": "partial", "flags": ["weather"]},
                    {"audit": {}, "status": "skipped", "flags": ["cross_voyage"]},
                ],
            }
        },
    )
    preview = fact_view(fact)
    assert [item["status"] for item in preview.value["data"]["results"]] == [
        "partial",
        "skipped",
    ]
    assert preview.omitted_paths
    assert len(json.dumps(preview.value, ensure_ascii=False, separators=(",", ":"))) <= 6_000


def test_context_reports_full_length_of_omitted_result_array():
    fact = Fact(
        fact_id=uuid4(),
        source_capability="monitor.batch",
        source_version="1",
        observed_at=datetime.now(UTC),
        value={
            "data": {
                "results": [
                    {"status": "partial", "audit": {"trace": "x" * 12000}} for _ in range(40)
                ]
            }
        },
    )
    pack = SimpleNamespace(capabilities={}, skills={}, system_prompt=lambda: "Use evidence")
    runtime = AgentRuntime(pack=pack, model=None)
    state = runtime.new_state("Summarize all intervals")
    state.facts.append(fact)
    packet = runtime.context(state)
    assert len(packet.facts[0].value["data"]["results"]) == 32
    assert any(
        'array at ["data", "results"] has 40 items' in item for item in packet.context_omissions
    )


def test_fact_preview_shows_status_of_nineteen_large_result_items():
    fact = Fact(
        fact_id=uuid4(),
        source_capability="calculate",
        source_version="1",
        observed_at=datetime.now(UTC),
        value={
            "data": {
                "results": [
                    {
                        "status": "skipped" if index == 18 else "partial",
                        "audit": {"trace": "x" * 12000},
                    }
                    for index in range(19)
                ]
            }
        },
    )
    preview = fact_view(fact)
    assert [item["status"] for item in preview.value["data"]["results"]] == [
        *(["partial"] * 18),
        "skipped",
    ]


def test_context_reports_array_length_after_fallback_hides_entire_fact():
    fact = Fact(
        fact_id=uuid4(),
        source_capability="monitor.batch",
        source_version="1",
        observed_at=datetime.now(UTC),
        value={"data": {"results": list(range(100))}},
    )
    pack = SimpleNamespace(capabilities={}, skills={}, system_prompt=lambda: "Use evidence")
    runtime = AgentRuntime(pack=pack, model=None, max_context_characters=3_000)
    state = runtime.new_state("x" * 2_150)
    state.facts.append(fact)
    packet = runtime.context(state)
    assert packet.facts[0].value == {}
    assert any(
        'array at ["data", "results"] has 100 items' in item for item in packet.context_omissions
    )
    assert len(pack.system_prompt()) + len(packet.model_dump_json()) <= 3_000


@pytest.mark.asyncio
async def test_inspected_array_reports_full_length_when_preview_is_truncated():
    fact = Fact(
        fact_id=uuid4(),
        source_capability="monitor.batch",
        source_version="1",
        observed_at=datetime.now(UTC),
        value={
            "data": {
                "results": [
                    {"status": "partial", "audit": {"trace": "x" * 12000}} for _ in range(40)
                ]
            }
        },
    )

    class Model:
        def __init__(self):
            self.prompts = []

        async def decide(self, *, context, system_prompt):
            self.prompts.append(system_prompt)
            if len(self.prompts) == 1:
                return InspectFactDecision(fact_id=fact.fact_id, path=("data", "results"))
            if len(self.prompts) == 2:
                return InspectFactDecision(
                    fact_id=fact.fact_id, path=("data", "results", 39, "status")
                )
            return FinalDecision(answer_markdown="Inspected", fact_ids=(fact.fact_id,))

    model = Model()
    pack = SimpleNamespace(capabilities={}, skills={}, system_prompt=lambda: "Use evidence")
    runtime = AgentRuntime(pack=pack, model=model)
    state = runtime.new_state("Count every interval")
    state.facts.append(fact)
    await runtime.step(state)
    assert state.inspected_fact is not None
    assert state.inspected_fact["array_length"] == 40
    assert len(state.inspected_fact["preview"]["value"]) == 32
    await runtime.step(state)
    assert state.inspected_fact is not None
    assert state.inspected_fact["parent_array_length"] == 40
    assert state.inspected_fact["inspected_index"] == 39
    await runtime.step(state)
    assert state.status == "completed"
    assert "has 40 items, but its preview shows only 32" in model.prompts[1]
    assert "Report the total as 40" in model.prompts[2]


def test_context_bounds_previews_and_history_without_losing_full_facts():
    pack = SimpleNamespace(capabilities={}, skills={}, system_prompt=lambda: "Use evidence")
    runtime = AgentRuntime(pack=pack, model=None, max_context_characters=30000)
    state = runtime.new_state("Inspect a long series")
    for index in range(40):
        state.facts.append(
            Fact(
                fact_id=uuid4(),
                source_capability="series.read",
                source_version="1",
                observed_at=datetime.now(UTC),
                value={"data": {"items": ["x" * 400] * 400}},
            )
        )
        state.model_observations.append(
            Observation(
                call_ref=f"read-{index}",
                capability="series.read",
                status="succeeded",
                fact_id=state.facts[-1].fact_id,
                arguments={"long_literal": "y" * 10000},
            )
        )
    context = runtime.context(state)
    assert len(context.model_dump_json()) < 30000
    assert len(context.facts) == 40
    assert all(fact.omitted_paths for fact in context.facts)
    assert len(context.observations) == 12
    assert all(item.arguments_omitted for item in context.observations)
    assert context.context_omissions
    value = resolve_argument(
        {
            "$fact_value": {
                "fact_id": str(state.facts[0].fact_id),
                "path": ["data", "items"],
            }
        },
        {fact.fact_id: fact for fact in state.facts},
    )
    assert len(value) == 400


def test_old_skills_are_evicted_explicitly_and_complete_schema_is_on_demand():
    def skill(name):
        return Skill(SkillDescription(name=name, description=name), "x" * 12000)

    capability = CapabilityDescription(
        name="record.read",
        version="1",
        description="Read",
        input_schema={"type": "object", "description": "x" * 3000},
    )
    pack = SimpleNamespace(
        capabilities={capability.name: capability},
        skills={"old": skill("old"), "new": skill("new")},
        system_prompt=lambda: "Use evidence",
    )
    runtime = AgentRuntime(pack=pack, model=None, max_context_characters=20000)
    state = runtime.new_state("Read")
    state.loaded_skills = ["old", "new"]
    packet = runtime.context(state)
    assert "old" not in packet.loaded_skills and "new" in packet.loaded_skills
    assert packet.context_omissions == ("skill: old; read_skill to load again",)
    assert packet.capabilities[0]["schema_requires_inspection"] is True
    assert "input_schema" not in packet.capabilities[0]
    assert len(packet.model_dump_json()) < 20000
    state.inspected_capability = "record.read"
    assert runtime.context(state).inspected_capability["input_schema"] == capability.input_schema


@pytest.mark.asyncio
@pytest.mark.parametrize("approval", [True, False])
async def test_transient_entry_cannot_bypass_approval_or_repeat_unknown_write(approval):
    calls = []

    async def invoke(name, arguments, *, context):
        calls.append(name)
        return CapabilityResult(error_code="provider_outcome_unknown")

    capability = CapabilityDescription(
        name="record.write",
        version="1",
        description="Write",
        input_schema={},
        effect="write",
        approval_required=approval,
    )
    pack = SimpleNamespace(
        capabilities={capability.name: capability},
        skills={},
        system_prompt=lambda: "Write",
        invoke=invoke,
    )

    class Model:
        count = 0

        async def decide(self, **kwargs):
            self.count += 1
            assert self.count == 1
            return ToolBatchDecision(
                calls=(
                    ToolCall(
                        call_ref="write",
                        capability="record.write",
                        arguments={},
                        reason="Write",
                    ),
                )
            )

    result = await AgentRuntime(
        pack=pack, model=Model(), granted_capabilities=frozenset({"record.write"})
    ).run("Write")
    assert result.status == ("needs_approval" if approval else "needs_reconciliation")
    assert len(calls) == (0 if approval else 1)


@pytest.mark.asyncio
async def test_identical_compute_is_not_invoked_twice_after_success_or_in_one_batch():
    calls = []

    async def invoke(name, arguments, *, context):
        calls.append((name, arguments))
        return CapabilityResult(data={"value": 7})

    capability = CapabilityDescription(
        name="calculate", version="1", description="Calculate", input_schema={}, effect="compute"
    )
    pack = SimpleNamespace(
        capabilities={capability.name: capability},
        skills={},
        system_prompt=lambda: "Calculate",
        invoke=invoke,
    )

    class Model:
        def __init__(self):
            self.count = 0
            self.prompts = []

        async def decide(self, *, context, system_prompt):
            self.count += 1
            self.prompts.append(system_prompt)
            if self.count <= 2:
                suffixes = ("first", "same-batch") if self.count == 1 else ("later",)
                return ToolBatchDecision(
                    calls=tuple(
                        ToolCall(
                            call_ref=suffix,
                            capability="calculate",
                            arguments={"body": {"input": 1}},
                            reason="Calculate once",
                        )
                        for suffix in suffixes
                    )
                )
            return FinalDecision(
                answer_markdown="Result is 7", fact_ids=(context.facts[0].fact_id,)
            )

    model = Model()
    result = await AgentRuntime(
        pack=pack, model=model, granted_capabilities=frozenset({"calculate"})
    ).run("Calculate")
    assert result.status == "completed"
    assert len(calls) == 1
    assert len(result.facts) == 1
    assert [(item.call_ref, item.error_code) for item in result.observations] == [
        ("same-batch", "repeated_equivalent_call"),
        ("first", None),
        ("later", "repeated_equivalent_call"),
    ]
    assert result.observations[-1].fact_id == result.facts[0].fact_id
    assert str(result.facts[0].fact_id) in model.prompts[-1]
    assert "Do not issue another equivalent tool call" in model.prompts[-1]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "previous,should_retry",
    [("available", False), ("expired", True), ("disconnected", True), ("operation_failed", True)],
)
async def test_compute_deduplication_uses_effective_result_availability(previous, should_retry):
    capability = CapabilityDescription(
        name="calculate", version="1", description="Calculate", input_schema={}, effect="compute"
    )
    pack = SimpleNamespace(
        capabilities={capability.name: capability},
        skills={},
        system_prompt=lambda: "Calculate",
    )
    retry = ToolCall(
        call_ref="retry", capability="calculate", arguments={"body": {"input": 1}}, reason="Retry"
    )

    class Model:
        async def decide(self, *, context, system_prompt):
            return ToolBatchDecision(calls=(retry,))

    runtime = AgentRuntime(pack=pack, model=Model(), connection_id="current")
    state = runtime.new_state("Calculate")
    fact = Fact(
        fact_id=uuid4(),
        source_capability="calculate",
        source_version="1",
        observed_at=datetime.now(UTC),
        value={"data": {"value": 7}},
        expires_at=datetime.now(UTC) - timedelta(seconds=1) if previous == "expired" else None,
        reference_scope="connection" if previous == "disconnected" else "durable",
        connection_id="previous" if previous == "disconnected" else None,
    )
    state.facts.append(fact)
    state.observations.append(
        Observation(
            call_ref="first",
            capability="calculate",
            status="succeeded",
            fact_id=fact.fact_id,
            arguments=retry.arguments,
        )
    )
    if previous == "operation_failed":
        state.observations.append(
            Observation(
                call_ref="first",
                capability="calculate",
                status="rejected",
                error_code="operation_failed",
            )
        )
    state.repeated[arguments_digest(retry)] = 1
    await runtime.step(state)
    assert len(state.pending) == int(should_retry)
    if not should_retry:
        assert state.observations[-1].error_code == "repeated_equivalent_call"
        assert state.observations[-1].fact_id == fact.fact_id


def test_non_json_numbers_and_ambiguous_expiry_are_rejected_before_journaling():
    with pytest.raises(ValidationError):
        DECISION_ADAPTER.validate_json(
            '{"kind":"tool_batch","calls":[{"call_ref":"call","capability":"record.write",'
            '"arguments":{"n":NaN},"reason":"test"}]}'
        )
    with pytest.raises(ValidationError):
        CapabilityResult(data={"n": float("nan")})
    with pytest.raises(ValidationError):
        CapabilityResult(data={}, expires_at="2026-10-01T00:00:00")
