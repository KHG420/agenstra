from datetime import UTC, datetime
from types import SimpleNamespace
from uuid import uuid4

import pytest
from pydantic import ValidationError

from agenstra.contracts import (
    DECISION_ADAPTER,
    Fact,
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
from agenstra.runtime import AgentRuntime, resolve_argument


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
