"""Management API and immutable-release behavior across live activation."""

import json
import sys
from pathlib import Path

import httpx
import pytest

from agenstra.contracts import FinalDecision, RequestInputDecision
from agenstra.deployment import Deployment, DeploymentConfig
from agenstra.host import AgentHost
from agenstra.registry import RegistryError
from agenstra.server import create_app
from agenstra.storage import SQLiteStore


class AskThenFinish:
    async def decide(self, *, context, system_prompt):
        if not context.followups:
            return RequestInputDecision(field="item", prompt="Which item?")
        return FinalDecision(answer_markdown=f"Item: {context.followups[0]}")


def manifest(version: str, description: str) -> dict:
    return {
        "schema": "agenstra.rest-pack.v2",
        "name": "records",
        "version": version,
        "guidance": "Use reviewed record data.",
        "base_url_env": "RECORDS_URL",
        "capabilities": [
            {
                "name": "records.get",
                "description": description,
                "method": "GET",
                "path": "/records/{record_id}",
                "effect": "read",
                "input_schema": {
                    "type": "object",
                    "properties": {
                        "path": {
                            "type": "object",
                            "properties": {"record_id": {"type": "string"}},
                            "required": ["record_id"],
                            "additionalProperties": False,
                        }
                    },
                    "required": ["path"],
                    "additionalProperties": False,
                },
                "output_schema": {
                    "type": "object",
                    "properties": {"id": {"type": "string"}},
                    "required": ["id"],
                    "additionalProperties": False,
                },
            }
        ],
    }


@pytest.mark.asyncio
async def test_publish_activate_bind_and_pin_running_release(tmp_path: Path):
    config = DeploymentConfig.model_validate(
        {
            "database_path": str(tmp_path / "runs.sqlite3"),
            "users": {"alice": {"api_key_env": "USER_KEY"}},
            "management": {
                "database_path": str(tmp_path / "registry.sqlite3"),
                "package_dir": str(tmp_path / "releases"),
                "admin_api_key_env": "ADMIN_KEY",
                "secret_dir": str(tmp_path / "secrets"),
            },
        }
    )
    environment = {
        "USER_KEY": "alice-user-key",
        "ADMIN_KEY": "a-separate-admin-key-longer-than-24",
    }
    deployment = Deployment(config, base_dir=tmp_path, environment=environment)
    assert deployment.registry is not None
    host = AgentHost(
        store=SQLiteStore(deployment.database_path),
        provider_factory=deployment.provider_factory,
        release_resolver=deployment.release_resolver,
        release_provider_factory=deployment.release_provider_factory,
        model=AskThenFinish(),
        policy_resolver=deployment.policy_resolver,
    )
    app = create_app(host, deployment.authenticate, worker_enabled=False, deployment=deployment)
    headers = {"Authorization": f"Bearer {environment['ADMIN_KEY']}"}
    user_headers = {"Authorization": f"Bearer {environment['USER_KEY']}"}
    transport = httpx.ASGITransport(app=app)
    async with (
        app.router.lifespan_context(app),
        httpx.AsyncClient(transport=transport, base_url="http://test") as client,
    ):
        assert (await client.get("/admin/api/overview")).status_code == 401
        assert (await client.get("/admin/api/overview", headers=user_headers)).status_code == 401
        page = await client.get("/admin", headers=headers)
        assert page.status_code == 200
        assert "frame-ancestors 'none'" in page.headers["content-security-policy"]
        assert (await client.get("/admin/assets/admin_ui.js")).status_code == 200

        spec = {
            "openapi": "3.1.0",
            "info": {"title": "Records", "version": "1"},
            "paths": {
                "/records/{record_id}": {
                    "get": {
                        "operationId": "records.get",
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
                                "description": "Found",
                                "content": {
                                    "application/json": {
                                        "schema": {
                                            "type": "object",
                                            "properties": {"id": {"type": "string"}},
                                            "required": ["id"],
                                            "additionalProperties": False,
                                        }
                                    }
                                },
                            }
                        },
                    }
                }
            },
        }
        draft = await client.post(
            "/admin/api/openapi-draft",
            headers=headers,
            json={
                "spec": spec,
                "name": "records",
                "base_url_env": "RECORDS_URL",
                "operations": ["records.get"],
            },
        )
        assert draft.status_code == 200, draft.text
        assert draft.json()["capabilities"][0]["name"] == "records.get"

        body1 = {"pack_id": "records", "version": "1.0.0", "manifest": manifest("1.0.0", "Read")}
        validated = await client.post("/admin/api/validate", headers=headers, json=body1)
        assert validated.status_code == 200, validated.text
        published1 = await client.post("/admin/api/releases", headers=headers, json=body1)
        assert published1.status_code == 200, published1.text
        digest1 = published1.json()["digest"]
        assert (
            await client.post(
                "/runs", headers=user_headers, json={"pack_id": "records", "instruction": "ask"}
            )
        ).status_code == 403

        activated = await client.post(
            "/admin/api/packs/records/activate",
            headers=headers,
            json={"digest": digest1, "expected_revision": 0},
        )
        assert activated.status_code == 200, activated.text
        assert activated.json()["revision"] == 1
        (tmp_path / "secrets").mkdir()
        (tmp_path / "secrets" / "RECORDS_URL").write_text(
            "https://records.example\n", encoding="utf-8"
        )
        binding = {
            "environment": {"RECORDS_URL": "secret:RECORDS_URL"},
            "granted_capabilities": ["records.get"],
            "allow_model_data": True,
        }
        bound = await client.put("/admin/api/bindings/alice/records", headers=headers, json=binding)
        assert bound.status_code == 200, bound.text
        checked = await client.post("/admin/api/bindings/alice/records/check", headers=headers)
        assert checked.status_code == 200, checked.text
        assert checked.json()["capabilities"] == ["records.get"]

        created = await client.post(
            "/runs",
            headers=user_headers,
            json={"pack_id": "records", "instruction": "ask", "request_id": "old-run"},
        )
        assert created.status_code == 200, created.text
        old_id = created.json()["run_id"]
        assert created.json()["state"]["pack_release"] == digest1
        pending = await host.drive(old_id, owner_id="alice")
        assert pending.status == "needs_input"

        body2 = {
            "pack_id": "records",
            "version": "2.0.0",
            "manifest": manifest("2.0.0", "Read a record using the new contract"),
        }
        published2 = await client.post("/admin/api/releases", headers=headers, json=body2)
        assert published2.status_code == 200, published2.text
        digest2 = published2.json()["digest"]
        conflict = await client.post(
            "/admin/api/packs/records/activate",
            headers=headers,
            json={"digest": digest2, "expected_revision": 0},
        )
        assert conflict.status_code == 409
        switched = await client.post(
            "/admin/api/packs/records/activate",
            headers=headers,
            json={"digest": digest2, "expected_revision": 1},
        )
        assert switched.status_code == 200, switched.text

        newer = await client.post(
            "/runs",
            headers=user_headers,
            json={"pack_id": "records", "instruction": "ask", "request_id": "new-run"},
        )
        assert newer.json()["state"]["pack_release"] == digest2
        supplied = await client.post(
            f"/runs/{old_id}/input",
            headers=user_headers,
            json={"field": "item", "text": "R-1", "revision": pending.revision},
        )
        assert supplied.status_code == 200, supplied.text
        finished = await host.drive(old_id, owner_id="alice")
        assert finished.status == "completed"
        assert finished.state["pack_release"] == digest1

        third_manifest = manifest("3.0.0", "Different capability")
        third_manifest["capabilities"][0]["name"] = "records.other"
        third = await client.post(
            "/admin/api/releases",
            headers=headers,
            json={
                "pack_id": "records",
                "version": "3.0.0",
                "manifest": third_manifest,
            },
        )
        assert third.status_code == 200, third.text
        incompatible = await client.post(
            "/admin/api/packs/records/activate",
            headers=headers,
            json={"digest": third.json()["digest"], "expected_revision": 2},
        )
        assert incompatible.status_code == 422
        assert incompatible.json()["detail"]["code"] == "binding_capability_missing"

        rolled_back = await client.post(
            "/admin/api/packs/records/activate",
            headers=headers,
            json={"digest": digest1, "expected_revision": 2},
        )
        assert rolled_back.status_code == 200
        assert rolled_back.json()["revision"] == 3
        audit = (await client.get("/admin/api/audit", headers=headers)).json()
        assert {item["action"] for item in audit} == {"publish", "activate", "bind"}
        disabled = await client.delete("/admin/api/bindings/alice/records", headers=headers)
        assert disabled.status_code == 200
        assert (await client.get(f"/runs/{old_id}", headers=user_headers)).status_code == 403

    release = await deployment.registry.release_path("records", digest1)
    release.write_text("{}", encoding="utf-8")
    with pytest.raises(RegistryError, match="release_tampered"):
        await deployment.registry.release_path("records", digest1)


@pytest.mark.asyncio
async def test_release_validation_rejects_missing_skills_and_version_reuse(tmp_path: Path):
    from agenstra.registry import CapabilityRegistry

    registry = CapabilityRegistry(tmp_path / "registry.sqlite3", tmp_path / "releases")
    await registry.initialize()
    first = await registry.publish("records", "1.0.0", manifest("1.0.0", "Read"), {})
    again = await registry.publish("records", "1.0.0", manifest("1.0.0", "Read"), {})
    assert first["digest"] == again["digest"]
    with pytest.raises(RegistryError, match="version_already_published"):
        await registry.publish("records", "1.0.0", manifest("1.0.0", "Changed"), {})
    bad = manifest("2.0.0", "Read")
    bad["skills"] = [
        {"name": "guide", "description": "Guide", "path": "../secret", "sha256": "0" * 64}
    ]
    with pytest.raises(RegistryError, match="invalid_skill_path"):
        await registry.validate("records", "2.0.0", bad, {"../secret": "value"})
    bad["skills"][0]["path"] = "pack.json/guide.md"
    with pytest.raises(RegistryError, match="invalid_skill_path"):
        await registry.validate("records", "2.0.0", bad, {"pack.json/guide.md": "value"})
    bad["skills"] = [
        {"name": "one", "description": "One", "path": "guides", "sha256": "0" * 64},
        {"name": "two", "description": "Two", "path": "guides/two.md", "sha256": "0" * 64},
    ]
    with pytest.raises(RegistryError, match="skill_path_conflict"):
        await registry.validate(
            "records", "2.0.0", bad, {"guides": "value", "guides/two.md": "value"}
        )


@pytest.mark.asyncio
async def test_release_path_rejects_linked_components_and_unlisted_content(tmp_path: Path):
    from agenstra.registry import CapabilityRegistry

    registry = CapabilityRegistry(tmp_path / "registry.sqlite3", tmp_path / "releases")
    await registry.initialize()
    published = await registry.publish("records", "1.0.0", manifest("1.0.0", "Read"), {})
    digest = published["digest"]
    pack_dir = registry.package_dir / "records"
    release_dir = pack_dir / digest
    manifest_path = release_dir / "pack.json"

    manifest_copy = tmp_path / "manifest-copy.json"
    manifest_copy.write_bytes(manifest_path.read_bytes())
    manifest_path.unlink()
    manifest_path.symlink_to(manifest_copy)
    with pytest.raises(RegistryError, match="release_tampered"):
        await registry.release_path("records", digest)
    manifest_path.unlink()
    manifest_path.write_bytes(manifest_copy.read_bytes())

    saved_release = tmp_path / "saved-release"
    release_dir.rename(saved_release)
    release_dir.symlink_to(saved_release, target_is_directory=True)
    with pytest.raises(RegistryError, match="release_tampered"):
        await registry.release_path("records", digest)
    release_dir.unlink()
    saved_release.rename(release_dir)

    saved_pack = tmp_path / "saved-pack"
    pack_dir.rename(saved_pack)
    pack_dir.symlink_to(saved_pack, target_is_directory=True)
    with pytest.raises(RegistryError, match="release_tampered"):
        await registry.release_path("records", digest)
    with pytest.raises(RegistryError, match="release_path_conflict"):
        await registry.publish("records", "1.0.0", manifest("1.0.0", "Read"), {})
    pack_dir.unlink()
    saved_pack.rename(pack_dir)

    unexpected = release_dir / "unlisted.txt"
    unexpected.write_text("unexpected", encoding="utf-8")
    with pytest.raises(RegistryError, match="release_tampered"):
        await registry.release_path("records", digest)
    unexpected.unlink()
    assert await registry.release_path("records", digest) == manifest_path


@pytest.mark.asyncio
async def test_existing_static_run_keeps_its_pack_after_managed_activation(tmp_path: Path):
    static_dir = tmp_path / "static"
    static_dir.mkdir()
    (static_dir / "pack.json").write_text(
        json.dumps(manifest("0.9.0", "Original static contract")), encoding="utf-8"
    )
    connection = {
        "environment": {"RECORDS_URL": "RECORDS_URL"},
        "granted_capabilities": ["records.get"],
        "allow_model_data": True,
    }
    config = DeploymentConfig.model_validate(
        {
            "database_path": str(tmp_path / "runs.sqlite3"),
            "packs": {"records": {"path": "static/pack.json"}},
            "users": {"alice": {"api_key_env": "USER_KEY", "packs": {"records": connection}}},
            "management": {
                "database_path": str(tmp_path / "registry.sqlite3"),
                "package_dir": str(tmp_path / "releases"),
                "admin_api_key_env": "ADMIN_KEY",
            },
        }
    )
    deployment = Deployment(
        config,
        base_dir=tmp_path,
        environment={
            "USER_KEY": "alice-user-key",
            "ADMIN_KEY": "a-separate-admin-key-longer-than-24",
            "RECORDS_URL": "https://records.example",
        },
    )
    assert deployment.registry is not None
    await deployment.registry.initialize()
    host = AgentHost(
        store=SQLiteStore(deployment.database_path),
        provider_factory=deployment.provider_factory,
        release_resolver=deployment.release_resolver,
        release_provider_factory=deployment.release_provider_factory,
        model=AskThenFinish(),
        policy_resolver=deployment.policy_resolver,
    )
    await host.store.initialize()
    created = await host.create("alice", "records", "ask")
    assert created.state["pack_release"] is None
    pending = await host.drive(created.run_id, owner_id="alice")
    assert pending.status == "needs_input"

    published = await deployment.registry.publish(
        "records", "1.0.0", manifest("1.0.0", "New managed contract"), {}
    )
    await deployment.registry.activate("records", published["digest"])
    await deployment.registry.put_binding("alice", "records", connection)
    await host.supply_input(
        created.run_id, owner_id="alice", field="item", text="R-1", revision=pending.revision
    )
    finished = await host.drive(created.run_id, owner_id="alice")
    assert finished.status == "completed"
    assert finished.state["pack_release"] is None


def test_management_cli_encodes_route_segments(monkeypatch: pytest.MonkeyPatch) -> None:
    from agenstra import manage_cli

    requested: list[str] = []

    class Client:
        def __init__(self, **kwargs: object) -> None:
            pass

        def __enter__(self) -> "Client":
            return self

        def __exit__(self, *args: object) -> None:
            pass

        def request(self, method: str, url: str, **kwargs: object) -> httpx.Response:
            assert method == "POST"
            requested.append(url)
            return httpx.Response(200, json={"capabilities": [], "skills": []})

    monkeypatch.setenv("AGENSTRA_ADMIN_API_KEY", "a-separate-admin-key-longer-than-24")
    monkeypatch.setattr(
        sys,
        "argv",
        ["agenstra-manage", "--server", "https://example.com", "check", "甲 乙", "records"],
    )
    monkeypatch.setattr(manage_cli.httpx, "Client", Client)
    manage_cli.main()
    assert requested == [
        "https://example.com/admin/api/bindings/%E7%94%B2%20%E4%B9%99/records/check"
    ]
