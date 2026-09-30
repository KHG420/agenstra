import json
from types import SimpleNamespace

import httpx
import pytest

from agenstra.contracts import FinalDecision, ToolBatchDecision, ToolCall
from agenstra.model import HttpJsonDecisionModel
from agenstra.runtime import AgentRuntime


def _pack():
    async def invoke(*args, **kwargs):
        raise AssertionError("an invalid decision must not reach a provider")

    return SimpleNamespace(
        capabilities={}, skills={}, system_prompt=lambda: "Use valid decisions", invoke=invoke
    )


def _response(content: str) -> httpx.Response:
    return httpx.Response(200, json={"choices": [{"message": {"content": content}}]})


@pytest.mark.asyncio
async def test_oversized_batch_gets_one_specific_repair_with_durable_checkpoints():
    prompts = []
    calls = []
    invalid = {
        "kind": "tool_batch",
        "calls": [
            {"call_ref": f"call-{index}", "capability": "fake.call", "arguments": {}, "reason": "x"}
            for index in range(5)
        ],
    }

    def handle(request: httpx.Request) -> httpx.Response:
        prompts.append(json.loads(request.content)["messages"][0]["content"])
        return _response(
            json.dumps(invalid)
            if len(prompts) == 1
            else FinalDecision(answer_markdown="Repaired").model_dump_json(by_alias=True)
        )

    client = httpx.AsyncClient(transport=httpx.MockTransport(handle))
    model = HttpJsonDecisionModel(
        model="test", base_url="http://model.test/v1", api_key="test", client=client
    )
    runtime = AgentRuntime(pack=_pack(), model=model)
    state = runtime.new_state("Answer")

    async def checkpoint() -> None:
        calls.append(state.rounds_used)

    try:
        await runtime.step(state, before_model=checkpoint)
    finally:
        await client.aclose()

    assert state.status == "completed"
    assert state.answer_markdown == "Repaired"
    assert state.rounds_used == 2
    assert calls == [1, 2]
    assert len(state.decisions) == 1
    assert len(state.pending) == 0
    assert "at most 4" in prompts[1]
    assert "at most 4" not in prompts[0]


@pytest.mark.asyncio
@pytest.mark.parametrize("max_rounds,expected_calls", [(1, 1), (3, 2)])
async def test_invalid_decision_stops_after_one_repair_or_at_round_budget(
    max_rounds, expected_calls
):
    prompts = []

    def handle(request: httpx.Request) -> httpx.Response:
        prompts.append(json.loads(request.content)["messages"][0]["content"])
        return _response("not json")

    client = httpx.AsyncClient(transport=httpx.MockTransport(handle))
    model = HttpJsonDecisionModel(
        model="test", base_url="http://model.test/v1", api_key="test", client=client
    )
    runtime = AgentRuntime(pack=_pack(), model=model, max_model_rounds=max_rounds)
    state = runtime.new_state("Answer")
    try:
        await runtime.step(state)
    finally:
        await client.aclose()
    assert state.status == "failed"
    assert state.error_code == "model_decision_invalid"
    assert state.rounds_used == expected_calls
    assert not state.pending and not state.decisions
    assert len(prompts) == expected_calls
    if expected_calls == 2:
        assert "Return exactly one valid decision object" in prompts[1]


@pytest.mark.asyncio
async def test_pseudo_tool_is_rejected_with_decision_kind_feedback_without_io():
    class Model:
        def __init__(self):
            self.contexts = []

        async def decide(self, *, context, system_prompt):
            self.contexts.append(context)
            if len(self.contexts) == 1:
                return ToolBatchDecision(
                    calls=(
                        ToolCall(
                            call_ref="inspect-one",
                            capability="agent.inspect_fact",
                            arguments={"fact_id": "example", "path": []},
                            reason="Inspect a result",
                        ),
                    )
                )
            return FinalDecision(answer_markdown="No tool data was used")

    model = Model()
    result = await AgentRuntime(pack=_pack(), model=model).run("Answer")
    assert result.status == "completed"
    assert len(result.observations) == 1
    assert result.observations[0].error_code == "use_inspect_fact_decision"
    assert model.contexts[1].observations[0].error_code == "use_inspect_fact_decision"
    assert len(result.facts) == 0


@pytest.mark.asyncio
async def test_model_http_error_is_not_retried_as_decision_repair():
    requests = []

    def handle(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(503)

    client = httpx.AsyncClient(transport=httpx.MockTransport(handle))
    model = HttpJsonDecisionModel(
        model="test", base_url="http://model.test/v1", api_key="test", client=client
    )
    runtime = AgentRuntime(pack=_pack(), model=model)
    state = runtime.new_state("Answer")
    try:
        await runtime.step(state)
    finally:
        await client.aclose()
    assert state.status == "failed"
    assert state.error_code == "model_http_error"
    assert state.rounds_used == 1
    assert len(requests) == 1
