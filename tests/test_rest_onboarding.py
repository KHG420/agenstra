"""REST onboarding contract and HTTP boundary tests, with no live network."""

from __future__ import annotations

import hashlib
import json
from contextlib import asynccontextmanager
from pathlib import Path
from types import SimpleNamespace
from typing import Any
from uuid import UUID, uuid4

import httpx
import pytest

from enterprise_agent.contracts import FinalDecision, ReadSkillDecision, ToolBatchDecision, ToolCall
from enterprise_agent.host import AgentHost, ExecutionPolicy, HostSettings
from enterprise_agent.openapi import import_openapi
from enterprise_agent.rest import load_rest_pack
from enterprise_agent.storage import SQLiteStore


@pytest.fixture
def manifest(tmp_path: Path) -> Path:
    document = {
        "schema": "enterprise.rest-pack.v2",
        "name": "record",
        "version": "1.0",
        "guidance": "Use reviewed records.",
        "base_url_env": "RECORDS_API_URL",
        "token_env": "RECORDS_API_TOKEN",
        "headers_env": {"X-Tenant": "RECORDS_TENANT"},
        "capabilities": [
            {
                "name": "record.update",
                "description": "Update a record",
                "method": "PATCH",
                "path": "/records/{record_id}",
                "effect": "write",
                "idempotency_header": "Idempotency-Key",
                "response_path": ["record"],
                "input_schema": {
                    "type": "object",
                    "additionalProperties": False,
                    "properties": {
                        "path": {
                            "type": "object",
                            "properties": {"record_id": {"type": "string"}},
                            "required": ["record_id"],
                            "additionalProperties": False,
                        },
                        "query": {
                            "type": "object",
                            "properties": {"tag": {"type": "array", "items": {"type": "string"}}},
                            "additionalProperties": False,
                        },
                        "body": {
                            "type": "object",
                            "properties": {"profile": {"$ref": "#/$defs/Profile"}},
                            "required": ["profile"],
                            "additionalProperties": False,
                        },
                    },
                    "$defs": {
                        "Profile": {
                            "type": "object",
                            "properties": {
                                "contact": {
                                    "type": "object",
                                    "properties": {"email": {"type": "string"}},
                                    "required": ["email"],
                                    "additionalProperties": False,
                                }
                            },
                            "required": ["contact"],
                            "additionalProperties": False,
                        }
                    },
                    "required": ["path", "body"],
                },
                "output_schema": {
                    "type": "object",
                    "properties": {"id": {"type": "string"}},
                    "required": ["id"],
                    "additionalProperties": False,
                },
                "response_schemas": {
                    "200": {
                        "type": "object",
                        "properties": {"id": {"type": "string"}},
                        "required": ["id"],
                        "additionalProperties": False,
                    }
                },
                "error_codes": {"409": "record.conflict"},
            }
        ],
    }
    result = tmp_path / "pack.json"
    result.write_text(json.dumps(document), encoding="utf-8")
    return result


def _args() -> dict[str, Any]:
    return {
        "path": {"record_id": "R 1"},
        "query": {"tag": ["north", "priority"]},
        "body": {"profile": {"contact": {"email": "owner@example.com"}}},
    }


@pytest.mark.asyncio
async def test_validation_serialization_and_secrets(manifest: Path) -> None:
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        return httpx.Response(200, json={"record": {"id": "R 1"}})

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        pack = load_rest_pack(
            manifest,
            environment={
                "RECORDS_API_URL": "https://internal.example/v1",
                "RECORDS_API_TOKEN": "supersecret",
                "RECORDS_TENANT": "tenant-secret",
            },
            client=client,
        )
        catalog = json.dumps(pack.capabilities["record.update"].model_view())
        assert "supersecret" not in catalog and "tenant-secret" not in catalog
        assert "RECORDS_API_TOKEN" not in catalog
        assert pack.capabilities["record.update"].replay == "idempotent"
        assert pack.capabilities["record.update"].effect == "write"
        invalid = _args()
        invalid["body"]["profile"]["contact"]["email"] = 42
        assert (await pack.invoke("record.update", invalid)).error_code == (
            "capability_input_invalid"
        )
        assert not requests
        context = SimpleNamespace(idempotency_key="stable-123")
        result = await pack.invoke("record.update", _args(), context=context)  # type: ignore[arg-type]
        assert result.data == {"id": "R 1"}
        assert requests[0].method == "PATCH"
        assert str(requests[0].url) == (
            "https://internal.example/v1/records/R%201?tag=north&tag=priority"
        )
        assert json.loads(requests[0].content) == _args()["body"]
        assert requests[0].headers["Authorization"] == "Bearer supersecret"
        assert requests[0].headers["X-Tenant"] == "tenant-secret"
        assert requests[0].headers["Idempotency-Key"] == "stable-123"
        await pack.invoke("record.update", _args(), context=context)  # type: ignore[arg-type]
        assert requests[1].headers["Idempotency-Key"] == "stable-123"
        await pack.aclose()


@pytest.mark.asyncio
async def test_endpoint_and_response_guards(manifest: Path) -> None:
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        if len(requests) == 1:
            return httpx.Response(200, json={"record": {"id": 7}})
        return httpx.Response(503 if len(requests) == 2 else 409)

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        pack = load_rest_pack(
            manifest,
            environment={
                "RECORDS_API_URL": "https://internal.example",
                "RECORDS_API_TOKEN": "secret",
                "RECORDS_TENANT": "tenant",
            },
            client=client,
        )
        context = SimpleNamespace(idempotency_key="key")
        for bad_id in ("..", "../admin", "%2e%2e", "a/b", "a\\b"):
            args = _args()
            args["path"]["record_id"] = bad_id
            assert (await pack.invoke("record.update", args, context=context)).error_code == (
                "capability_input_invalid"
            )  # type: ignore[arg-type]
        args = _args()
        args["url"] = "https://attacker.example"
        assert (await pack.invoke("record.update", args, context=context)).error_code == (
            "capability_input_invalid"
        )  # type: ignore[arg-type]
        assert not requests
        assert (await pack.invoke("record.update", _args(), context=context)).error_code == (
            "upstream_response_invalid"
        )  # type: ignore[arg-type]
        assert (await pack.invoke("record.update", _args(), context=context)).error_code == (
            "provider_outcome_unknown"
        )  # type: ignore[arg-type]
        assert (await pack.invoke("record.update", _args(), context=context)).error_code == (
            "record.conflict"
        )  # type: ignore[arg-type]
        assert (await pack.invoke("unknown", _args())).error_code == "capability_unknown"


@pytest.mark.asyncio
async def test_transport_error_is_unknown_outcome(manifest: Path) -> None:
    calls = 0

    def handler(request: httpx.Request) -> httpx.Response:
        nonlocal calls
        calls += 1
        raise httpx.ConnectError("unknown", request=request)

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        pack = load_rest_pack(
            manifest,
            environment={
                "RECORDS_API_URL": "https://internal.example",
                "RECORDS_API_TOKEN": "secret",
                "RECORDS_TENANT": "tenant",
            },
            client=client,
        )
        result = await pack.invoke(
            "record.update", _args(), context=SimpleNamespace(idempotency_key="key")
        )  # type: ignore[arg-type]
        assert result.error_code == "provider_outcome_unknown"
        assert calls == 1


def test_openapi_import_selects_only_reviewed_operation_and_preserves_refs(tmp_path: Path) -> None:
    spec = {
        "openapi": "3.0.3",
        "info": {"title": "Record", "version": "1"},
        "components": {
            "schemas": {
                "Profile": {
                    "type": "object",
                    "properties": {
                        "contact": {
                            "type": "object",
                            "properties": {"email": {"type": "string", "nullable": True}},
                            "required": ["email"],
                        }
                    },
                    "required": ["contact"],
                }
            }
        },
        "paths": {
            "/records/{record_id}": {
                "patch": {
                    "operationId": "record.update",
                    "parameters": [
                        {
                            "name": "record_id",
                            "in": "path",
                            "required": True,
                            "schema": {"type": "string"},
                        },
                        {
                            "name": "tag",
                            "in": "query",
                            "style": "form",
                            "explode": True,
                            "schema": {"type": "array", "items": {"type": "string"}},
                        },
                    ],
                    "requestBody": {
                        "required": True,
                        "content": {
                            "application/json": {
                                "schema": {
                                    "type": "object",
                                    "properties": {
                                        "profile": {"$ref": "#/components/schemas/Profile"}
                                    },
                                    "required": ["profile"],
                                }
                            }
                        },
                    },
                    "responses": {
                        "200": {
                            "content": {
                                "application/json": {
                                    "schema": {
                                        "type": "object",
                                        "properties": {"id": {"type": "string"}},
                                        "required": ["id"],
                                    }
                                }
                            }
                        },
                    },
                }
            },
            "/danger": {
                "delete": {
                    "operationId": "record.delete",
                    "responses": {
                        "200": {"content": {"application/json": {"schema": {"type": "object"}}}}
                    },
                }
            },
        },
    }
    path = tmp_path / "openapi.json"
    path.write_text(json.dumps(spec), encoding="utf-8")
    pack = import_openapi(
        path, name="record", base_url_env="RECORDS_API_URL", operations=["record.update"]
    )
    assert len(pack["capabilities"]) == 1
    cap = pack["capabilities"][0]
    assert cap["name"] == "record.update" and cap["effect"] == "write"
    assert cap["input_schema"]["properties"]["body"]["properties"]["profile"]["$ref"] == (
        "#/$defs/Profile"
    )
    email_schema = cap["input_schema"]["$defs"]["Profile"]["properties"]["contact"]["properties"][
        "email"
    ]
    assert {item["type"] for item in email_schema["anyOf"]} == {"string", "null"}
    spec["paths"]["/records/{record_id}"]["patch"]["parameters"][1]["explode"] = False
    path.write_text(json.dumps(spec), encoding="utf-8")
    with pytest.raises(ValueError, match="form/explode=true"):
        import_openapi(
            path, name="record", base_url_env="RECORDS_API_URL", operations=["record.update"]
        )


def _job_pack(tmp_path: Path) -> Path:
    """Create a synthetic pack in a temporary directory; no pack is shipped."""
    content = b"For a queued or running job, wait for its observed final status.\n"
    skill = tmp_path / "skills" / "job-guidance" / "SKILL.md"
    skill.parent.mkdir(parents=True)
    skill.write_bytes(content)
    job_schema = {
        "type": "object",
        "properties": {
            "jobId": {"type": "string"},
            "status": {"type": "string", "enum": ["queued", "running", "succeeded", "failed"]},
            "report": {"type": "string"},
        },
        "required": ["jobId", "status"],
        "additionalProperties": False,
    }
    document = {
        "schema": "enterprise.rest-pack.v2",
        "name": "jobs",
        "version": "1",
        "guidance": "Use the job API and verify its final status.",
        "base_url_env": "JOBS_API_URL",
        "skills": [
            {
                "name": "job-guidance",
                "description": "How to interpret job status",
                "path": "skills/job-guidance/SKILL.md",
                "sha256": hashlib.sha256(content).hexdigest(),
            }
        ],
        "capabilities": [
            {
                "name": "jobs.create",
                "description": "Submit a job",
                "method": "POST",
                "path": "/jobs",
                "effect": "write",
                "skills": ["job-guidance"],
                "idempotency_argument": ["body", "client_request_id"],
                "input_schema": {
                    "type": "object",
                    "properties": {
                        "body": {
                            "type": "object",
                            "properties": {
                                "record_id": {"type": "string"},
                                "client_request_id": {"type": "string"},
                            },
                            "required": ["record_id", "client_request_id"],
                            "additionalProperties": False,
                        }
                    },
                    "required": ["body"],
                    "additionalProperties": False,
                },
                "output_schema": job_schema,
                "response_schemas": {"202": job_schema},
                "operation": {
                    "id_path": ["jobId"],
                    "status_path": ["status"],
                    "poll_capability": "jobs.status",
                    "poll_argument": ["path", "job_id"],
                    "pending_states": ["queued", "running"],
                    "success_states": ["succeeded"],
                    "failure_states": ["failed"],
                },
            },
            {
                "name": "jobs.status",
                "description": "Read a job status",
                "method": "GET",
                "path": "/jobs/{job_id}",
                "effect": "read",
                "input_schema": {
                    "type": "object",
                    "properties": {
                        "path": {
                            "type": "object",
                            "properties": {"job_id": {"type": "string"}},
                            "required": ["job_id"],
                            "additionalProperties": False,
                        }
                    },
                    "required": ["path"],
                    "additionalProperties": False,
                },
                "output_schema": job_schema,
            },
        ],
    }
    path = tmp_path / "pack.json"
    path.write_text(json.dumps(document), encoding="utf-8")
    return path


def test_camel_case_operation_id_imports_and_loads(tmp_path: Path) -> None:
    spec = {
        "openapi": "3.0.3",
        "info": {"title": "Records", "version": "1"},
        "paths": {
            "/records/{record_id}": {
                "get": {
                    "operationId": "getRecord",
                    "parameters": [
                        {
                            "name": "record_id",
                            "in": "path",
                            "required": True,
                            "schema": {"type": "string"},
                        }
                    ],
                    "responses": {
                        "200": {
                            "content": {
                                "application/json": {
                                    "schema": {
                                        "type": "object",
                                        "properties": {"id": {"type": "string"}},
                                        "required": ["id"],
                                    }
                                }
                            }
                        }
                    },
                }
            }
        },
    }
    source = tmp_path / "openapi.json"
    source.write_text(json.dumps(spec), encoding="utf-8")
    draft = import_openapi(
        source, name="records", base_url_env="RECORDS_API_URL", operations=["getRecord"]
    )
    path = tmp_path / "pack.json"
    path.write_text(json.dumps(draft), encoding="utf-8")
    pack = load_rest_pack(path, environment={"RECORDS_API_URL": "https://records.test"})
    assert pack.capabilities["getRecord"].replay == "safe"


def test_trusted_rest_skills_and_tamper_rejection(tmp_path: Path) -> None:
    path = _job_pack(tmp_path)
    pack = load_rest_pack(path, environment={"JOBS_API_URL": "https://jobs.test"})
    assert "job-guidance" in pack.skills
    assert pack.capabilities["jobs.create"].skills == ("job-guidance",)
    skill = tmp_path / "skills" / "job-guidance" / "SKILL.md"
    original = skill.read_bytes()
    skill.write_text(skill.read_text() + "\nchanged", encoding="utf-8")
    with pytest.raises(ValueError, match="skill content changed"):
        load_rest_pack(path, environment={"JOBS_API_URL": "https://jobs.test"})
    reviewed = json.loads(path.read_text(encoding="utf-8"))
    reviewed["capabilities"][0]["skills"] = ["missing-skill"]
    path.write_text(json.dumps(reviewed), encoding="utf-8")
    skill.write_bytes(original)
    with pytest.raises(ValueError, match="unknown skill"):
        load_rest_pack(path, environment={"JOBS_API_URL": "https://jobs.test"})


@pytest.mark.asyncio
async def test_rest_host_reads_skill_waits_and_resumes_one_job(tmp_path: Path) -> None:
    path = _job_pack(tmp_path)
    job_id = str(uuid4())
    state: dict[str, Any] = {"status": "queued", "requests": [], "loaded_skill": False}
    clock = [1_000_000.0]

    def handler(request: httpx.Request) -> httpx.Response:
        state["requests"].append(request)
        if request.method == "POST" and request.url.path == "/jobs":
            payload = json.loads(request.content)
            assert payload["record_id"] == "R-1"
            assert str(UUID(payload["client_request_id"])) == payload["client_request_id"]
            return httpx.Response(202, json={"jobId": job_id, "status": "queued"})
        if request.method == "GET" and request.url.path == f"/jobs/{job_id}":
            return httpx.Response(
                200,
                json={
                    "jobId": job_id,
                    "status": state["status"],
                    "report": "Verified report",
                },
            )
        raise AssertionError(f"unexpected REST request: {request.method} {request.url.path}")

    @asynccontextmanager
    async def provider_factory(_owner_id: str, _pack_id: str):
        async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
            yield load_rest_pack(
                path,
                environment={"JOBS_API_URL": "https://jobs.test"},
                client=client,
            )

    async def policy(_owner_id: str, _pack_id: str) -> ExecutionPolicy:
        return ExecutionPolicy(
            granted_capabilities=frozenset({"jobs.create"}),
            allow_model_data=True,
        )

    class Model:
        async def decide(self, *, context: Any, system_prompt: str) -> Any:
            assert "job-guidance" in tuple(skill["name"] for skill in context.skills)
            if not context.loaded_skills:
                return ReadSkillDecision(name="job-guidance")
            assert "queued or running job" in context.loaded_skills["job-guidance"]
            state["loaded_skill"] = True
            if not context.facts:
                return ToolBatchDecision(
                    calls=(
                        ToolCall(
                            call_ref="submit",
                            capability="jobs.create",
                            arguments={"body": {"record_id": "R-1"}},
                            reason="Create the requested report",
                        ),
                    )
                )
            return FinalDecision(
                answer_markdown="The report job succeeded.",
                fact_ids=tuple(fact.fact_id for fact in context.facts),
            )

    database = tmp_path / "rest-host.sqlite"
    store = SQLiteStore(database, clock=lambda: clock[0])
    await store.initialize()
    settings = HostSettings(max_model_rounds=5, max_poll_calls=5)
    first = AgentHost(
        store=store,
        provider_factory=provider_factory,
        model=Model(),
        policy_resolver=policy,
        settings=settings,
        clock=lambda: clock[0],
    )
    created = await first.create("owner-1", "jobs", "Create a report")
    waiting = await first.drive(created.run_id, owner_id="owner-1")
    assert waiting.status == "waiting" and state["loaded_skill"]
    submits = [request for request in state["requests"] if request.method == "POST"]
    assert len(submits) == 1
    request_id = json.loads(submits[0].content)["client_request_id"]
    assert (
        waiting.state["runtime"]["pending"][0]["call"]["arguments"]["body"]["client_request_id"]
        == request_id
    )

    state["status"] = "succeeded"
    clock[0] += 6
    second = AgentHost(
        store=SQLiteStore(database, clock=lambda: clock[0]),
        provider_factory=provider_factory,
        model=Model(),
        policy_resolver=policy,
        settings=settings,
        clock=lambda: clock[0],
    )
    finished = await second.drive(created.run_id, owner_id="owner-1")
    assert finished.status == "completed"
    assert len([request for request in state["requests"] if request.method == "POST"]) == 1
    assert len([request for request in state["requests"] if request.method == "GET"]) == 1
    events = await second.store.list_events(created.run_id, owner_id="owner-1")
    assert any(event["event"]["kind"] == "operation_polled" for event in events)
