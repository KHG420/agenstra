import asyncio
from pathlib import Path

import pytest

from agenstra.storage import LeaseLost, RunNotFound, SQLiteStore, StoreConflict


@pytest.mark.asyncio
async def test_reopen_and_owner_scope(tmp_path: Path) -> None:
    path = tmp_path / "runs.db"
    store = SQLiteStore(path)
    await store.initialize()
    run = await store.create_run(owner_id="alice", pack_id="pack", state={"value": [1, 2]})
    assert run.status == "queued"
    assert run.revision == 0
    reopened = SQLiteStore(path)
    await reopened.initialize()
    assert (await reopened.get_run(run.run_id, owner_id="alice")).state == {"value": [1, 2]}
    assert len(await reopened.list_runs(owner_id="alice")) == 1
    assert await reopened.list_runs(owner_id="bob") == ()
    for operation in (
        reopened.get_run(run.run_id, owner_id="bob"),
        reopened.get_invocation(run.run_id, "i", owner_id="bob"),
        reopened.get_artifact(run.run_id, "a", owner_id="bob"),
        reopened.list_events(run.run_id, owner_id="bob"),
    ):
        with pytest.raises(RunNotFound):
            await operation


@pytest.mark.asyncio
async def test_competing_claims_and_expired_token(tmp_path: Path) -> None:
    now = [100.0]
    path = tmp_path / "leases.db"
    first = SQLiteStore(path, clock=lambda: now[0])
    second = SQLiteStore(path, clock=lambda: now[0])
    await asyncio.gather(first.initialize(), second.initialize())
    run = await first.create_run(owner_id="alice", pack_id="pack", state={})
    results = await asyncio.gather(
        first.claim(run.run_id, owner_id="alice", lease_seconds=10),
        second.claim(run.run_id, owner_id="alice", lease_seconds=10),
        return_exceptions=True,
    )
    winners = [result for result in results if not isinstance(result, BaseException)]
    assert len(winners) == 1
    assert len([result for result in results if isinstance(result, StoreConflict)]) == 1
    old_token = winners[0].lease_token
    assert old_token is not None
    now[0] = 110.0
    with pytest.raises(LeaseLost):
        await first.renew(run.run_id, owner_id="alice", lease_token=old_token)
    new_lease = await second.claim(run.run_id, owner_id="alice")
    assert new_lease.lease_token != old_token
    with pytest.raises(LeaseLost):
        await first.checkpoint(
            run.run_id,
            owner_id="alice",
            lease_token=old_token,
            state={"bad": True},
            status="running",
        )
    assert (await first.get_run(run.run_id, owner_id="alice")).state == {}
    assert new_lease.lease_token is not None
    await second.release(run.run_id, owner_id="alice", lease_token=new_lease.lease_token)


@pytest.mark.asyncio
async def test_checkpoint_atomicity_and_immutable_artifacts(tmp_path: Path) -> None:
    store = SQLiteStore(tmp_path / "checkpoint.db")
    await store.initialize()
    run = await store.create_run(owner_id="alice", pack_id="pack", state={})
    lease = await store.claim(run.run_id, owner_id="alice")
    assert lease.lease_token is not None
    saved = await store.checkpoint(
        run.run_id,
        owner_id="alice",
        lease_token=lease.lease_token,
        state={"step": 1},
        status="running",
        invocations=({"invocation_id": "i1", "status": "done"},),
        artifacts={"a1": {"result": {"ok": True}}},
        events=({"kind": "started"},),
    )
    assert saved.revision == 1
    assert saved.lease_token == lease.lease_token
    assert await store.get_invocation(run.run_id, "i1", owner_id="alice") == {
        "invocation_id": "i1",
        "status": "done",
    }
    assert await store.get_artifact(run.run_id, "a1", owner_id="alice") == {"result": {"ok": True}}
    events = await store.list_events(run.run_id, owner_id="alice")
    assert len(events) == 1
    assert events[0]["event"] == {"kind": "started"}
    assert await store.list_events(run.run_id, owner_id="alice", after=events[0]["sequence"]) == ()

    with pytest.raises(StoreConflict, match="immutable"):
        await store.checkpoint(
            run.run_id,
            owner_id="alice",
            lease_token=lease.lease_token,
            state={"step": 2},
            status="completed",
            invocations=({"invocation_id": "i2", "status": "done"},),
            artifacts={"a1": {"result": {"ok": False}}},
            events=({"kind": "finished"},),
        )
    assert (await store.get_run(run.run_id, owner_id="alice")).revision == 1
    assert await store.get_invocation(run.run_id, "i2", owner_id="alice") is None
    assert len(await store.list_events(run.run_id, owner_id="alice")) == 1
    await store.checkpoint(
        run.run_id,
        owner_id="alice",
        lease_token=lease.lease_token,
        state={"step": 2},
        status="completed",
        artifacts={"a1": {"result": {"ok": True}}},
    )


@pytest.mark.asyncio
async def test_due_runs_filter_status_wake_and_lease(tmp_path: Path) -> None:
    now = [50.0]
    store = SQLiteStore(tmp_path / "due.db", clock=lambda: now[0])
    await store.initialize()
    queued = await store.create_run(owner_id="a", pack_id="p", state={})
    waiting = await store.create_run(owner_id="a", pack_id="p", state={})
    approval = await store.create_run(owner_id="a", pack_id="p", state={})
    leased = await store.create_run(owner_id="a", pack_id="p", state={})
    for run, status, wake in ((waiting, "waiting", 60.0), (approval, "needs_approval", None)):
        claim = await store.claim(run.run_id, owner_id="a")
        assert claim.lease_token is not None
        await store.checkpoint(
            run.run_id,
            owner_id="a",
            lease_token=claim.lease_token,
            state={},
            status=status,
            next_wake_at=wake,
        )
        await store.release(run.run_id, owner_id="a", lease_token=claim.lease_token)
    await store.claim(leased.run_id, owner_id="a", lease_seconds=20)
    assert {run.run_id for run in await store.due_runs()} == {queued.run_id}
    now[0] = 60.0
    assert {run.run_id for run in await store.due_runs()} == {queued.run_id, waiting.run_id}
    now[0] = 70.0
    assert {run.run_id for run in await store.due_runs()} == {
        queued.run_id,
        waiting.run_id,
        leased.run_id,
    }


@pytest.mark.asyncio
async def test_cancel_request_survives_active_lease_checkpoint(tmp_path: Path) -> None:
    now = [50.0]
    path = tmp_path / "cancel.db"
    worker = SQLiteStore(path, clock=lambda: now[0])
    caller = SQLiteStore(path, clock=lambda: now[0])
    await worker.initialize()
    run = await worker.create_run(owner_id="a", pack_id="p", state={})
    lease = await worker.claim(run.run_id, owner_id="a")
    assert lease.lease_token is not None
    await worker.checkpoint(
        run.run_id,
        owner_id="a",
        lease_token=lease.lease_token,
        state={"step": 1},
        status="waiting",
        next_wake_at=500.0,
    )
    canceled = await caller.request_cancel(run.run_id, owner_id="a")
    assert canceled.cancel_requested
    assert canceled.status == "waiting"
    assert canceled.state == {"step": 1}
    assert canceled.lease_token == lease.lease_token
    assert await caller.due_runs() == ()
    saved = await worker.checkpoint(
        run.run_id,
        owner_id="a",
        lease_token=lease.lease_token,
        state={"step": 2},
        status="waiting",
        next_wake_at=500.0,
    )
    assert saved.cancel_requested
    await worker.release(run.run_id, owner_id="a", lease_token=lease.lease_token)
    assert [item.run_id for item in await caller.due_runs()] == [run.run_id]
    events = await caller.list_events(run.run_id, owner_id="a")
    assert [item["event"] for item in events] == [{"kind": "cancel_requested"}]


@pytest.mark.asyncio
async def test_cancel_wakes_every_nonterminal_pause_after_lease_expires(tmp_path: Path) -> None:
    now = [10.0]
    store = SQLiteStore(tmp_path / "cancel-due.db", clock=lambda: now[0])
    await store.initialize()
    statuses = (
        "queued",
        "running",
        "waiting",
        "needs_input",
        "needs_approval",
        "needs_authorization",
        "needs_reconciliation",
    )
    paused = []
    leases = []
    for status in statuses:
        run = await store.create_run(owner_id="a", pack_id="p", state={})
        lease = await store.claim(run.run_id, owner_id="a", lease_seconds=10)
        assert lease.lease_token is not None
        await store.checkpoint(
            run.run_id,
            owner_id="a",
            lease_token=lease.lease_token,
            state={},
            status=status,
            next_wake_at=1000.0,
        )
        await store.request_cancel(run.run_id, owner_id="a")
        paused.append(run.run_id)
        leases.append(lease.lease_token)
    for status in ("completed", "failed", "cancelled"):
        terminal = await store.create_run(owner_id="a", pack_id="p", state={})
        terminal_lease = await store.claim(terminal.run_id, owner_id="a", lease_seconds=10)
        assert terminal_lease.lease_token is not None
        await store.checkpoint(
            terminal.run_id,
            owner_id="a",
            lease_token=terminal_lease.lease_token,
            state={},
            status=status,
        )
        await store.request_cancel(terminal.run_id, owner_id="a")
    assert await store.due_runs() == ()
    now[0] = 20.0
    assert {run.run_id for run in await store.due_runs()} == set(paused)
    with pytest.raises(LeaseLost):
        await store.release(paused[0], owner_id="a", lease_token=leases[0])
