import json
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager
from pathlib import Path

import httpx
import pytest
from pydantic import JsonValue

from agent_capability.contracts import (
    ContextPacket,
    FinalDecision,
    RequestInputDecision,
    ToolBatchDecision,
    ToolCall,
)
from agent_capability.deployment import Deployment, DeploymentConfig, DeploymentError
from agent_capability.host import AgentHost, ExecutionPolicy
from agent_capability.providers import (
    CapabilityDescription,
    CapabilityResult,
    InvocationContext,
    Skill,
)
from agent_capability.server import create_app
from agent_capability.storage import SQLiteStore


class ScriptedModel:
    async def decide(self, *, context: ContextPacket, system_prompt: str):
        if context.instruction == "ask":
            if not context.followups:
                return RequestInputDecision(field="destination", prompt="Where to?")
            return FinalDecision(answer_markdown=f"Destination: {context.followups[0]}")
        if context.instruction == "approve":
            if not context.observations:
                return ToolBatchDecision(
                    calls=(
                        ToolCall(
                            call_ref="write-1",
                            capability="record.write",
                            arguments={"text": "hello"},
                            reason="Save requested record",
                        ),
                    )
                )
            return FinalDecision(
                answer_markdown="Saved", fact_ids=tuple(fact.fact_id for fact in context.facts)
            )
        return FinalDecision(answer_markdown="Done")


class WorkflowProvider:
    binding_id = "test-binding"

    def __init__(self, calls: list[dict[str, JsonValue]]) -> None:
        self.calls = calls

    @property
    def capabilities(self) -> Mapping[str, CapabilityDescription]:
        return {
            "record.write": CapabilityDescription(
                name="record.write",
                version="1",
                description="Save a record",
                input_schema={
                    "type": "object",
                    "properties": {"text": {"type": "string"}},
                    "required": ["text"],
                    "additionalProperties": False,
                },
                output_schema={"type": "object"},
                effect="write",
                approval_required=True,
            )
        }

    @property
    def skills(self) -> Mapping[str, Skill]:
        return {}

    def system_prompt(self) -> str:
        return "Follow the user's request."

    async def invoke(
        self,
        name: str,
        arguments: dict[str, JsonValue],
        *,
        context: InvocationContext | None = None,
    ) -> CapabilityResult:
        self.calls.append(arguments)
        return CapabilityResult(data={"saved": True})


@pytest.mark.asyncio
async def test_http_auth_owner_scope_and_key_revocation(tmp_path: Path) -> None:
    secrets = {"ALICE_KEY": "alice-secret", "BOB_KEY": "bob-secret"}
    deployment = Deployment(
        DeploymentConfig.model_validate(
            {
                "database_path": "agent.db",
                "packs": {"p": {"path": "unused.json"}},
                "users": {
                    "alice": {"api_key_env": "ALICE_KEY", "packs": {"p": {}}},
                    "bob": {"api_key_env": "BOB_KEY", "packs": {"p": {}}},
                },
            }
        ),
        base_dir=tmp_path,
        environment=secrets,
    )
    store = SQLiteStore(tmp_path / "agent.db")
    calls: list[dict[str, JsonValue]] = []
    allowed_packs = {"p"}

    @asynccontextmanager
    async def provider_factory(owner_id: str, pack_id: str) -> AsyncIterator[WorkflowProvider]:
        yield WorkflowProvider(calls)

    async def policy_resolver(owner_id: str, pack_id: str) -> ExecutionPolicy:
        if pack_id not in allowed_packs:
            raise DeploymentError("access_denied")
        return ExecutionPolicy(
            granted_capabilities=frozenset({"record.write"}), allow_model_data=True
        )

    host = AgentHost(
        store=store,
        provider_factory=provider_factory,
        model=ScriptedModel(),
        policy_resolver=policy_resolver,
    )
    app = create_app(host, deployment.authenticate, worker_enabled=False)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://test") as client,
    ):
        assert (await client.get("/healthz")).status_code == 200
        assert (await client.get("/readyz")).status_code == 200
        assert (await client.get("/runs")).status_code == 401
        alice = {"Authorization": "Bearer alice-secret"}
        bob = {"Authorization": "Bearer bob-secret"}
        created = await client.post(
            "/runs", headers=alice, json={"pack_id": "p", "instruction": "ask"}
        )
        assert created.status_code == 200
        run_id = created.json()["run_id"]
        assert "lease_token" not in created.json()
        assert (await client.get(f"/runs/{run_id}", headers=bob)).status_code == 404
        assert (await client.get(f"/runs/{run_id}/events", headers=bob)).status_code == 404
        assert (await client.get(f"/runs/{run_id}/artifacts/a", headers=bob)).status_code == 404
        assert (await client.get("/runs", headers=bob)).json() == []
        assert len((await client.get("/runs", headers=alice)).json()) == 1
        asked = await client.post(f"/runs/{run_id}/resume", headers=alice)
        assert asked.status_code == 200
        assert asked.json()["status"] == "needs_input"
        revision = asked.json()["revision"]
        answered = await client.post(
            f"/runs/{run_id}/input",
            headers=alice,
            json={"field": "destination", "text": "Shanghai", "revision": revision},
        )
        assert answered.status_code == 200
        assert answered.json()["status"] == "queued"
        completed = await client.post(f"/runs/{run_id}/resume", headers=alice)
        assert completed.status_code == 200
        assert completed.json()["status"] == "completed"
        assert "Shanghai" in completed.json()["state"]["runtime"]["answer_markdown"]

        canceled_run = await client.post(
            "/runs", headers=alice, json={"pack_id": "p", "instruction": "ask"}
        )
        canceled_id = canceled_run.json()["run_id"]
        assert (await client.post(f"/runs/{canceled_id}/resume", headers=alice)).json()[
            "status"
        ] == "needs_input"
        cancellation = await client.post(f"/runs/{canceled_id}/cancel", headers=alice)
        assert cancellation.json()["cancel_requested"] is True
        assert await host.wake_due() == 1
        assert (await client.get(f"/runs/{canceled_id}", headers=alice)).json()["status"] == (
            "cancelled"
        )

        approval_run = await client.post(
            "/runs", headers=alice, json={"pack_id": "p", "instruction": "approve"}
        )
        approval_id = approval_run.json()["run_id"]
        pending = await client.post(f"/runs/{approval_id}/resume", headers=alice)
        assert pending.status_code == 200
        assert pending.json()["status"] == "needs_approval"
        assert calls == []
        invocation = pending.json()["state"]["runtime"]["pending"][0]
        approved = await client.post(
            f"/runs/{approval_id}/approval",
            headers=alice,
            json={
                "invocation_id": invocation["invocation_id"],
                "arguments_sha256": invocation["arguments_sha256"],
                "revision": pending.json()["revision"],
                "approved": True,
            },
        )
        assert approved.status_code == 200
        assert approved.json()["status"] == "queued"
        finished = await client.post(f"/runs/{approval_id}/resume", headers=alice)
        assert finished.status_code == 200
        assert finished.json()["status"] == "completed"
        assert calls == [{"text": "hello"}]
        artifact_id = finished.json()["state"]["artifact_ids"][0]
        assert (
            await client.get(f"/runs/{approval_id}/artifacts/{artifact_id}", headers=alice)
        ).status_code == 200
        assert (
            await client.get(f"/runs/{approval_id}/artifacts/{artifact_id}", headers=bob)
        ).status_code == 404
        assert (await client.get(f"/runs/{approval_id}/events", headers=alice)).status_code == 200
        allowed_packs.clear()
        assert (await client.get("/runs", headers=alice)).json() == []
        assert (await client.get(f"/runs/{run_id}", headers=alice)).status_code == 403
        secrets.pop("ALICE_KEY")
        assert (await client.get(f"/runs/{run_id}", headers=alice)).status_code == 401
        assert (await client.get("/runs", headers=bob)).status_code == 200


@pytest.mark.asyncio
async def test_identity_rechecked_against_exact_subject(tmp_path: Path) -> None:
    subjects = ["alice"]
    calls = []

    def identity(request: httpx.Request) -> httpx.Response:
        calls.append(request)
        return httpx.Response(200, json={"principal": {"subject": subjects[0]}})

    client = httpx.AsyncClient(transport=httpx.MockTransport(identity))
    deployment = Deployment(
        DeploymentConfig.model_validate(
            {
                "database_path": "agent.db",
                "packs": {"p": {"path": "p.json"}},
                "users": {
                    "alice": {
                        "api_key_env": "KEY",
                        "packs": {
                            "p": {
                                "environment": {},
                                "allow_model_data": True,
                                "identity": {
                                    "url_env": "IDENTITY_URL",
                                    "token_env": "IDENTITY_TOKEN",
                                    "subject_path": ["principal", "subject"],
                                },
                            }
                        },
                    }
                },
            }
        ),
        base_dir=tmp_path,
        environment={
            "KEY": "key",
            "IDENTITY_URL": "https://identity.invalid/me",
            "IDENTITY_TOKEN": "secret",
        },
        identity_client=client,
    )
    assert calls == []
    policy = await deployment.policy_resolver("alice", "p")
    assert policy.allow_model_data
    assert len(calls) == 1
    subjects[0] = "bob"
    with pytest.raises(DeploymentError, match="identity_unverified"):
        await deployment.policy_resolver("alice", "p")
    await client.aclose()


@pytest.mark.asyncio
async def test_provider_binding_tracks_endpoint_and_identity_without_tokens(tmp_path: Path) -> None:
    pack = tmp_path / "pack.json"
    pack.write_text(
        json.dumps(
            {
                "schema": "agent-capability.rest-pack.v2",
                "name": "records",
                "version": "1",
                "guidance": "Read records.",
                "base_url_env": "PACK_URL",
                "token_env": "PACK_TOKEN",
                "capabilities": [
                    {
                        "name": "record.read",
                        "description": "Read a record",
                        "method": "GET",
                        "path": "/records",
                        "effect": "read",
                        "input_schema": {
                            "type": "object",
                            "properties": {},
                            "additionalProperties": False,
                        },
                        "output_schema": {
                            "type": "object",
                            "properties": {"ok": {"type": "boolean"}},
                        },
                    }
                ],
            }
        ),
        encoding="utf-8",
    )
    secrets = {
        "KEY": "api-key",
        "REST_URL": "https://records.example/v1",
        "REST_TOKEN": "token-one",
        "IDENTITY_URL": "https://identity.example/me",
        "IDENTITY_TOKEN": "identity-token-one",
    }
    identity_client = httpx.AsyncClient(
        transport=httpx.MockTransport(lambda _: httpx.Response(200, json={"sub": "alice"}))
    )
    deployment = Deployment(
        DeploymentConfig.model_validate(
            {
                "database_path": "agent.db",
                "packs": {"p": {"path": "pack.json"}},
                "users": {
                    "alice": {
                        "api_key_env": "KEY",
                        "packs": {
                            "p": {
                                "environment": {"PACK_URL": "REST_URL", "PACK_TOKEN": "REST_TOKEN"},
                                "allow_model_data": True,
                                "identity": {
                                    "url_env": "IDENTITY_URL",
                                    "token_env": "IDENTITY_TOKEN",
                                    "subject_path": ["sub"],
                                },
                            }
                        },
                    }
                },
            }
        ),
        base_dir=tmp_path,
        environment=secrets,
        identity_client=identity_client,
    )
    async with deployment.provider_factory("alice", "p") as provider:
        original = provider.binding_id
    assert len(original) == 64
    secrets["REST_TOKEN"] = "token-two"
    secrets["IDENTITY_TOKEN"] = "identity-token-two"
    async with deployment.provider_factory("alice", "p") as provider:
        assert provider.binding_id == original
    secrets["REST_URL"] = "https://records.example/v2"
    async with deployment.provider_factory("alice", "p") as provider:
        assert provider.binding_id != original
        endpoint_changed = provider.binding_id
    secrets["IDENTITY_URL"] = "https://identity.example/other-subject"
    async with deployment.provider_factory("alice", "p") as provider:
        assert provider.binding_id != endpoint_changed
        before_path_change = provider.binding_id
    manifest = json.loads(pack.read_text())
    manifest["capabilities"][0]["path"] = "/other-records"
    pack.write_text(json.dumps(manifest))
    async with deployment.provider_factory("alice", "p") as provider:
        assert provider.binding_id != before_path_change
    connection = deployment.config.users["alice"].packs["p"]
    with pytest.raises(DeploymentError, match="binding_contains_credentials"):
        deployment._binding_id(
            "alice",
            "p",
            pack,
            connection.model_copy(
                update={
                    "binding_environment": frozenset({"PACK_TOKEN"}),
                }
            ),
            {"PACK_TOKEN": "secret"},
        )
    bound = connection.model_copy(update={"binding_environment": frozenset({"UPSTREAM_URL"})})
    first = deployment._binding_id(
        "alice", "p", pack, bound, {"UPSTREAM_URL": "https://one.example"}
    )
    second = deployment._binding_id(
        "alice", "p", pack, bound, {"UPSTREAM_URL": "https://two.example"}
    )
    assert first != second
    await identity_client.aclose()
