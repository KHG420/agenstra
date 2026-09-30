"""Durable host contract tests: restarts, external commits, authorization and fencing."""

import asyncio
import json
import os
import subprocess
import sys
from contextlib import asynccontextmanager
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import ClassVar

import pytest

from agenstra.contracts import (
    FinalDecision,
    InspectFactDecision,
    RequestInputDecision,
    ToolBatchDecision,
    ToolCall,
)
from agenstra.host import AgentHost, ExecutionPolicy, HostError, HostSettings
from agenstra.providers import CapabilityDescription, CapabilityResult, OperationBinding
from agenstra.storage import RunNotFound, SQLiteStore, StoreConflict


class Clock:
    def __init__(self):
        self.now = 1_800_000_000.0

    def __call__(self):
        return self.now


class SequenceModel:
    def __init__(self, *decisions):
        self.decisions = iter(decisions)
        self.contexts = []

    async def decide(self, *, context, system_prompt):
        self.contexts.append(context)
        decision = next(self.decisions)
        return decision(context) if callable(decision) else decision


def call(name="records.read", arguments=None, ref="read"):
    return ToolBatchDecision(
        calls=(
            ToolCall(
                call_ref=ref,
                capability=name,
                arguments=arguments or {},
                reason="Complete request",
            ),
        )
    )


def final(context):
    return FinalDecision(
        answer_markdown="Result with evidence.", fact_ids=tuple(f.fact_id for f in context.facts)
    )


def capability(name="records.read", **kwargs):
    return CapabilityDescription(
        **(
            {
                "name": name,
                "version": "1",
                "description": "Test external capability",
                "input_schema": {"type": "object"},
            }
            | kwargs
        )
    )


@dataclass
class StubProvider:
    capabilities: dict = field(default_factory=lambda: {"records.read": capability(replay="safe")})
    calls: list = field(default_factory=list)
    handler: object = None
    binding: str = "sample-system-1"

    @asynccontextmanager
    async def connect(self, owner_id, pack_id):
        provider = self

        class Provider:
            capabilities = provider.capabilities
            skills: ClassVar[dict] = {}
            binding_id = provider.binding

            def system_prompt(self):
                return "Choose the next action using external capabilities."

            async def invoke(self, name, arguments, *, context):
                assert context.owner_id == owner_id
                provider.calls.append((name, dict(arguments), context))
                if provider.handler is not None:
                    return await provider.handler(name, arguments, context)
                return CapabilityResult(data={"record_id": "A1", "owner": owner_id})

        yield Provider()


class Policies:
    def __init__(self, *grants, approvals=()):
        self.policy = ExecutionPolicy(
            granted_capabilities=frozenset(grants),
            approval_capabilities=frozenset(approvals),
            allow_model_data=True,
        )

    async def __call__(self, owner_id, pack_id):
        return self.policy


async def make_host(tmp_path, provider, model, *, clock=None, policies=None, settings=None):
    clock = clock or Clock()
    store = SQLiteStore(tmp_path / "runs.sqlite3", clock=clock)
    await store.initialize()
    return AgentHost(
        store=store,
        provider_factory=provider.connect,
        model=model,
        policy_resolver=policies or Policies(),
        settings=settings,
        clock=clock,
    )


def runtime(run):
    return run.state["runtime"]


@pytest.mark.asyncio
async def test_invalid_model_decision_repair_is_checkpointed_before_each_request(tmp_path):
    class RepairModel:
        def __init__(self):
            self.prompts = []

        async def decide(self, *, context, system_prompt):
            self.prompts.append(system_prompt)
            return "invalid" if len(self.prompts) == 1 else FinalDecision(answer_markdown="Done")

    model = RepairModel()
    host = await make_host(tmp_path, StubProvider(), model)
    created = await host.create("alice", "sample", "Answer")
    completed = await host.drive(created.run_id, owner_id="alice")
    assert completed.status == "completed"
    assert runtime(completed)["rounds_used"] == 2
    assert len(runtime(completed)["decisions"]) == 1
    assert "Return exactly one valid decision object" in model.prompts[1]
    events = await host.store.list_events(created.run_id, owner_id="alice")
    assert [
        item["event"]["round"] for item in events if item["event"]["kind"] == "model_requested"
    ] == [1, 2]


@pytest.mark.asyncio
async def test_question_survives_restart_and_keeps_facts_and_owner(tmp_path):
    provider = StubProvider()
    model = SequenceModel(call(), RequestInputDecision(field="region", prompt="Which region?"))
    host = await make_host(tmp_path, provider, model)
    created = await host.create("alice", "sample", "Summarize the records")
    paused = await host.drive(created.run_id, owner_id="alice")
    assert paused.status == "needs_input"
    fact_id = paused.state["artifact_ids"][0]
    with pytest.raises(RunNotFound):
        await host.get(paused.run_id, owner_id="bob")
    with pytest.raises(RunNotFound):
        await host.store.get_artifact(paused.run_id, fact_id, owner_id="bob")

    def answer(context):
        assert context.followups == ("region: east",)
        assert str(context.facts[0].fact_id) == fact_id
        assert context.facts[0].value["data"]["owner"] == "alice"
        return final(context)

    restarted = await make_host(tmp_path, provider, SequenceModel(answer))
    await restarted.supply_input(
        paused.run_id, owner_id="alice", field="region", text="east", revision=paused.revision
    )
    completed = await restarted.drive(paused.run_id, owner_id="alice")
    assert completed.status == "completed"
    assert runtime(completed)["rounds_used"] == 3
    assert len(provider.calls) == 1


@pytest.mark.asyncio
@pytest.mark.parametrize("replay", ["idempotent", "never"])
async def test_response_lost_after_commit_recovers_same_invocation_or_stops(tmp_path, replay):
    provider = StubProvider(
        capabilities={
            "jobs.create": capability(
                "jobs.create",
                effect="write",
                replay=replay,
                idempotency_argument=("request_id",),
            )
        }
    )
    accepted = {}

    async def commit(name, arguments, context):
        first = context.idempotency_key not in accepted
        accepted.setdefault(context.idempotency_key, {"job_id": "job-1"})
        if first:
            raise asyncio.CancelledError  # Process died after the remote commit.
        return CapabilityResult(data=accepted[context.idempotency_key])

    provider.handler = commit
    policy = Policies("jobs.create")
    model = SequenceModel(call("jobs.create", {"value": 42, "request_id": "model-key"}), final)
    host = await make_host(tmp_path, provider, model, policies=policy)
    created = await host.create("alice", "sample", "Submit job")
    with pytest.raises(asyncio.CancelledError):
        await host.drive(created.run_id, owner_id="alice")
    persisted = await host.get(created.run_id, owner_id="alice")
    assert runtime(persisted)["pending"][0]["status"] == "in_flight"

    restarted = await make_host(tmp_path, provider, model, policies=policy)
    recovered = await restarted.drive(created.run_id, owner_id="alice")
    assert len(accepted) == 1
    if replay == "idempotent":
        assert recovered.status == "completed"
        assert len(provider.calls) == 2
        first, second = provider.calls
        assert first[1] == second[1]
        assert first[1]["request_id"] != "model-key"
        assert first[2].invocation_id == second[2].invocation_id
    else:
        assert recovered.status == "needs_reconciliation"
        assert len(provider.calls) == 1
        assert runtime(recovered)["pending"][0]["status"] == "unknown"


@pytest.mark.asyncio
async def test_job_waiting_survives_restart_without_llm_polling(tmp_path):
    binding = OperationBinding(
        id_path=("job_id",),
        status_path=("status",),
        poll_capability="jobs.status",
        poll_argument=("job_id",),
        interval_seconds=10,
    )
    provider = StubProvider(
        capabilities={
            "jobs.create": capability(
                "jobs.create",
                effect="compute",
                replay="idempotent",
                idempotency_argument=("request_id",),
                operation=binding,
            ),
            "jobs.status": capability("jobs.status", replay="safe"),
        }
    )
    statuses = iter(["running", "succeeded"])

    async def jobs(name, arguments, context):
        status = "queued" if name == "jobs.create" else next(statuses)
        return CapabilityResult(data={"job_id": "job-1", "status": status})

    provider.handler = jobs
    model = SequenceModel(call("jobs.create"), final)
    policy, clock = Policies("jobs.create"), Clock()
    host = await make_host(tmp_path, provider, model, policies=policy, clock=clock)
    created = await host.create("alice", "sample", "Run model")
    waiting = await host.drive(created.run_id, owner_id="alice")
    assert waiting.status == "waiting"
    assert await host.wake_due() == 0
    assert len(model.contexts) == 1

    restarted = await make_host(tmp_path, provider, model, policies=policy, clock=clock)
    clock.now += 10
    assert await restarted.wake_due() == 1
    assert (await restarted.get(created.run_id, owner_id="alice")).status == "waiting"
    assert len(model.contexts) == 1
    clock.now += 10
    await restarted.wake_due()
    finished = await restarted.get(created.run_id, owner_id="alice")
    assert finished.status == "completed"
    assert len(model.contexts) == 2
    assert [name for name, _, _ in provider.calls] == ["jobs.create", "jobs.status", "jobs.status"]
    assert model.contexts[-1].facts[0].value["data"]["status"] == "succeeded"
    assert model.contexts[-1].observations[-1].status == "succeeded"
    assert len(finished.state["artifact_ids"]) == 1
    assert len(await restarted.store.list_events(created.run_id, owner_id="alice")) > 3


@pytest.mark.asyncio
async def test_failed_compute_operation_can_be_submitted_again(tmp_path):
    binding = OperationBinding(
        id_path=("job_id",),
        status_path=("status",),
        poll_capability="jobs.status",
        poll_argument=("job_id",),
    )
    provider = StubProvider(
        capabilities={
            "jobs.create": capability("jobs.create", effect="compute", operation=binding),
            "jobs.status": capability("jobs.status"),
        }
    )

    async def jobs(name, arguments, context):
        attempt = len(provider.calls)
        return CapabilityResult(
            data={"job_id": f"job-{attempt}", "status": "failed" if attempt == 1 else "succeeded"}
        )

    provider.handler = jobs
    model = SequenceModel(call("jobs.create", ref="first"), call("jobs.create", ref="retry"), final)
    host = await make_host(tmp_path, provider, model, policies=Policies("jobs.create"))
    created = await host.create("alice", "sample", "Retry a definitively failed calculation")
    completed = await host.drive(created.run_id, owner_id="alice")
    assert completed.status == "completed"
    assert [name for name, _, _ in provider.calls] == ["jobs.create", "jobs.create"]
    assert runtime(completed)["tool_calls_used"] == 2
    assert any(
        item["error_code"] == "operation_failed" for item in runtime(completed)["observations"]
    )


@pytest.mark.asyncio
async def test_approval_binds_owner_revision_arguments_expiry_and_current_policy(tmp_path):
    provider = StubProvider(
        capabilities={"records.write": capability("records.write", effect="write")}
    )
    policy, clock = Policies("records.write", approvals=("records.write",)), Clock()
    model = SequenceModel(call("records.write", {"value": 7}), final)
    host = await make_host(tmp_path, provider, model, policies=policy, clock=clock)
    created = await host.create("alice", "sample", "Write record")
    paused = await host.drive(created.run_id, owner_id="alice")
    assert paused.status == "needs_approval" and not provider.calls
    item = runtime(paused)["pending"][0]
    params = dict(
        owner_id="alice",
        invocation_id=item["invocation_id"],
        arguments_sha256=item["arguments_sha256"],
        revision=paused.revision,
    )
    for changed, error in [
        ({"arguments_sha256": "wrong"}, "approval_arguments_changed"),
        ({"revision": paused.revision - 1}, "revision_conflict"),
    ]:
        with pytest.raises(HostError, match=error):
            await host.approve(created.run_id, **(params | changed))
    with pytest.raises(RunNotFound):
        await host.approve(created.run_id, **(params | {"owner_id": "bob"}))
    clock.now += 901
    with pytest.raises(HostError, match="approval_expired"):
        await host.approve(created.run_id, **params)
    refreshed = await host.drive(created.run_id, owner_id="alice")
    item = runtime(refreshed)["pending"][0]
    await host.approve(created.run_id, **(params | {"revision": refreshed.revision}))
    policy.policy = policy.policy.model_copy(update={"granted_capabilities": frozenset()})
    denied = await host.drive(created.run_id, owner_id="alice")
    assert denied.status == "needs_authorization" and not provider.calls
    policy.policy = policy.policy.model_copy(
        update={"granted_capabilities": frozenset({"records.write"})}
    )
    finished = await host.drive(created.run_id, owner_id="alice")
    assert finished.status == "completed" and len(provider.calls) == 1


@pytest.mark.asyncio
async def test_reconnect_does_not_send_stale_connection_reference_after_approval(tmp_path):
    provider = StubProvider(
        capabilities={
            "records.read": capability(reference_scope="connection", replay="safe"),
            "records.write": capability("records.write", effect="write"),
        }
    )

    def write(context):
        return call(
            "records.write",
            {
                "ref": {
                    "$fact_value": {
                        "fact_id": str(context.facts[0].fact_id),
                        "path": ["data", "record_id"],
                    }
                }
            },
            ref="write",
        )

    def check(context):
        assert not context.facts[0].reference_available
        assert context.observations[-1].error_code == "fact_reference_expired"
        return InspectFactDecision(fact_id=context.facts[0].fact_id, path=("data", "record_id"))

    def inspected(context):
        assert context.inspected_fact["preview"] == {"value": "A1"}
        return final(context)

    policies = Policies("records.write", approvals=("records.write",))
    host = await make_host(
        tmp_path, provider, SequenceModel(call(), write, check, inspected), policies=policies
    )
    created = await host.create("alice", "sample", "Read and write")
    paused = await host.drive(created.run_id, owner_id="alice")
    item = runtime(paused)["pending"][0]
    await host.approve(
        created.run_id,
        owner_id="alice",
        invocation_id=item["invocation_id"],
        arguments_sha256=item["arguments_sha256"],
        revision=paused.revision,
    )
    finished = await host.drive(created.run_id, owner_id="alice")
    assert finished.status == "completed"
    assert [name for name, _, _ in provider.calls] == ["records.read"]


@pytest.mark.asyncio
async def test_two_drivers_do_not_send_twice_and_cancel_keeps_unknown_outcome(tmp_path):
    entered, blocker = asyncio.Event(), asyncio.Event()
    provider = StubProvider()

    async def blocked(name, arguments, context):
        entered.set()
        await blocker.wait()
        return CapabilityResult(data={})

    provider.handler = blocked
    host = await make_host(
        tmp_path, provider, SequenceModel(call()), settings=HostSettings(lease_seconds=3)
    )
    created = await host.create("alice", "sample", "Read")
    first = asyncio.create_task(host.drive(created.run_id, owner_id="alice"))
    await asyncio.wait_for(entered.wait(), 2)
    with pytest.raises(StoreConflict):
        await host.drive(created.run_id, owner_id="alice")
    await host.cancel(created.run_id, owner_id="alice")
    stopped = await asyncio.wait_for(first, 3)
    assert stopped.status == "cancelled"
    assert runtime(stopped)["pending"][0]["status"] == "unknown"
    assert len(provider.calls) == 1
    assert (await host.get(created.run_id, owner_id="alice")).lease_token is None


@pytest.mark.asyncio
@pytest.mark.parametrize("change", ["binding", "capability"])
async def test_contract_or_connection_change_blocks_resumption(tmp_path, change):
    provider = StubProvider()
    model = SequenceModel(RequestInputDecision(field="region", prompt="Which region?"), call())
    host = await make_host(tmp_path, provider, model)
    created = await host.create("alice", "sample", "Read")
    paused = await host.drive(created.run_id, owner_id="alice")
    await host.supply_input(
        created.run_id, owner_id="alice", field="region", text="east", revision=paused.revision
    )
    if change == "binding":
        provider.binding = "different-system"
    else:
        provider.capabilities["records.read"] = capability(description="New contract")
    resumed = await host.drive(created.run_id, owner_id="alice")
    assert resumed.status == "failed" and runtime(resumed)["error_code"] == "pack_changed"
    assert not provider.calls


@pytest.mark.asyncio
async def test_idempotent_run_creation_uses_unambiguous_owner_and_request_key(tmp_path):
    host = await make_host(tmp_path, StubProvider(), SequenceModel())
    one = await host.create("a:b", "sample", "Read", request_id="c")
    two = await host.create("a", "sample", "Read", request_id="b:c")
    assert one.run_id != two.run_id
    assert (await host.create("a:b", "sample", "Read", request_id="c")).run_id == one.run_id
    with pytest.raises(HostError, match="request_id_conflict"):
        await host.create("a:b", "sample", "Different instruction", request_id="c")


@pytest.mark.asyncio
async def test_denied_model_data_never_sends_persisted_data_to_model(tmp_path):
    policies = Policies()
    model = SequenceModel(final)
    host = await make_host(tmp_path, StubProvider(), model, policies=policies)
    created = await host.create("alice", "sample", "Read")
    policies.policy = policies.policy.model_copy(update={"allow_model_data": False})
    with pytest.raises(HostError, match="model_data_not_authorized"):
        await host.drive(created.run_id, owner_id="alice")
    assert not model.contexts
    assert (await host.get(created.run_id, owner_id="alice")).status == "needs_authorization"


@pytest.mark.asyncio
async def test_expired_reference_and_other_run_reference_are_rejected(tmp_path):
    provider = StubProvider()

    async def expired(name, arguments, context):
        return CapabilityResult(
            data={"record_id": "A1"}, expires_at=datetime.now(UTC) - timedelta(seconds=1)
        )

    provider.handler = expired

    def reuse(context):
        return call(arguments={"ref": {"$fact_id": str(context.facts[0].fact_id)}}, ref="second")

    model = SequenceModel(call(), reuse, final)
    host = await make_host(tmp_path, provider, model)
    created = await host.create("alice", "sample", "Read")
    done = await host.drive(created.run_id, owner_id="alice")
    assert model.contexts[-1].observations[-1].error_code == "fact_reference_expired"
    assert len(provider.calls) == 1
    foreign = done.state["artifact_ids"][0]
    second = await make_host(
        tmp_path,
        provider,
        SequenceModel(
            call(arguments={"ref": {"$fact_id": foreign}}),
            final,
        ),
    )
    other = await second.create("bob", "sample", "Read")
    done = await second.drive(other.run_id, owner_id="bob")
    assert runtime(done)["observations"][0]["error_code"] == "fact_reference_not_available"
    assert len(provider.calls) == 1


@pytest.mark.asyncio
async def test_connection_failure_after_crash_preserves_uncertain_commit(tmp_path):
    provider = StubProvider(
        capabilities={"records.write": capability("records.write", effect="write")}
    )

    async def commit(name, arguments, context):
        raise asyncio.CancelledError

    provider.handler = commit
    host = await make_host(
        tmp_path, provider, SequenceModel(call("records.write")), policies=Policies("records.write")
    )
    created = await host.create("alice", "sample", "Write")
    with pytest.raises(asyncio.CancelledError):
        await host.drive(created.run_id, owner_id="alice")

    @asynccontextmanager
    async def unavailable(owner_id, pack_id):
        raise RuntimeError("a credential-bearing library message that must never be saved")
        yield  # pragma: no cover

    host.provider_factory = unavailable
    result = await host.drive(created.run_id, owner_id="alice")
    assert result.status == "needs_reconciliation"
    assert runtime(result)["pending"][0]["status"] == "unknown"
    assert "credential-bearing" not in str(result.state)


@pytest.mark.asyncio
async def test_job_poll_cannot_reuse_session_scoped_job_after_restart(tmp_path):
    binding = OperationBinding(
        id_path=("job",),
        status_path=("status",),
        poll_capability="jobs.status",
        poll_argument=("job",),
    )
    provider = StubProvider(
        capabilities={
            "jobs.create": capability(
                "jobs.create", effect="write", operation=binding, reference_scope="connection"
            ),
            "jobs.status": capability("jobs.status"),
        }
    )

    async def jobs(name, arguments, context):
        return CapabilityResult(
            data={"job": "session-job", "status": "queued"}, reference_scope="connection"
        )

    provider.handler = jobs
    clock = Clock()
    host = await make_host(
        tmp_path,
        provider,
        SequenceModel(call("jobs.create")),
        clock=clock,
        policies=Policies("jobs.create"),
    )
    created = await host.create("alice", "sample", "Write")
    waiting = await host.drive(created.run_id, owner_id="alice")
    assert waiting.status == "waiting"
    clock.now += 6
    result = await host.drive(created.run_id, owner_id="alice")
    assert result.status == "needs_reconciliation"
    assert runtime(result)["error_code"] == "operation_reference_unavailable"
    assert len(provider.calls) == 1


@pytest.mark.asyncio
async def test_polling_cannot_bypass_approval_policy(tmp_path):
    binding = OperationBinding(
        id_path=("job",),
        status_path=("status",),
        poll_capability="jobs.status",
        poll_argument=("job",),
    )
    provider = StubProvider(
        capabilities={
            "jobs.create": capability("jobs.create", effect="write", operation=binding),
            "jobs.status": capability("jobs.status"),
        }
    )
    host = await make_host(
        tmp_path,
        provider,
        SequenceModel(call("jobs.create")),
        policies=Policies("jobs.create", approvals=("jobs.status",)),
    )
    created = await host.create("alice", "sample", "Write")
    with pytest.raises(HostError, match="operation_poll_requires_unattended_access"):
        await host.drive(created.run_id, owner_id="alice")
    assert not provider.calls
    assert (await host.get(created.run_id, owner_id="alice")).status == "needs_authorization"


@pytest.mark.asyncio
async def test_oversized_decision_stops_before_external_send(tmp_path):
    provider = StubProvider()
    host = await make_host(
        tmp_path,
        provider,
        SequenceModel(call(arguments={"data": "x" * 10000})),
        settings=HostSettings(max_state_bytes=5000),
    )
    created = await host.create("alice", "sample", "Read")
    result = await host.drive(created.run_id, owner_id="alice")
    assert result.status == "failed" and runtime(result)["error_code"] == "run_state_too_large"
    assert not provider.calls


@pytest.mark.asyncio
async def test_process_exit_after_external_commit_recovers_after_lease_expiry(tmp_path):
    script = """
import asyncio, json, os, sys
from pathlib import Path
from test_host import StubProvider, Policies, SequenceModel, call, capability, make_host

async def main():
    root = Path(sys.argv[1])
    provider = StubProvider(capabilities={"jobs.create": capability(
        "jobs.create", effect="write", replay="idempotent", idempotency_argument=("request_id",))})
    async def commit(name, arguments, context):
        (root / "external_commit.json").write_text(json.dumps({
            "key": context.idempotency_key, "arguments": arguments, "run_id": context.run_id,
        }))
        os._exit(23)
    provider.handler = commit
    host = await make_host(root, provider, SequenceModel(call("jobs.create")),
                           policies=Policies("jobs.create"))
    run = await host.create("alice", "sample", "Submit")
    await host.drive(run.run_id, owner_id="alice")

asyncio.run(main())
"""
    process = await asyncio.to_thread(
        subprocess.run,
        [sys.executable, "-c", script, str(tmp_path)],
        env={**os.environ, "PYTHONPATH": str(Path(__file__).parent)},
        capture_output=True,
        text=True,
        timeout=15,
    )
    assert process.returncode == 23, process.stderr
    committed = json.loads((tmp_path / "external_commit.json").read_text())
    provider = StubProvider(
        capabilities={
            "jobs.create": capability(
                "jobs.create",
                effect="write",
                replay="idempotent",
                idempotency_argument=("request_id",),
            )
        }
    )

    async def deduplicate(name, arguments, context):
        assert context.idempotency_key == committed["key"]
        assert arguments == committed["arguments"]
        return CapabilityResult(data={"job_id": "existing-job"})

    provider.handler = deduplicate
    clock = Clock()
    host = await make_host(
        tmp_path, provider, SequenceModel(final), policies=Policies("jobs.create"), clock=clock
    )
    with pytest.raises(StoreConflict):
        await host.drive(committed["run_id"], owner_id="alice")
    clock.now += 61
    result = await host.drive(committed["run_id"], owner_id="alice")
    assert result.status == "completed"
    assert len(provider.calls) == 1
