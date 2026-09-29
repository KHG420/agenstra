"""Authenticated management API and its small, dependency-free web client."""

from __future__ import annotations

import asyncio
import re
from hmac import compare_digest
from importlib.resources import files
from typing import Any

from fastapi import APIRouter, Depends, Header, HTTPException
from fastapi.responses import HTMLResponse, Response
from pydantic import BaseModel, ConfigDict, Field, JsonValue

from agenstra.deployment import ConnectionConfig, Deployment, DeploymentError
from agenstra.openapi import import_openapi_document
from agenstra.registry import RegistryError

_ENV = re.compile(r"[A-Z][A-Z0-9_]*\Z")


class _Body(BaseModel):
    model_config = ConfigDict(extra="forbid")


class PackageBody(_Body):
    pack_id: str = Field(min_length=1, max_length=128)
    version: str = Field(min_length=1, max_length=40)
    manifest: dict[str, Any]
    skills: dict[str, str] = Field(default_factory=dict)


class ActivateBody(_Body):
    digest: str
    expected_revision: int | None = Field(default=None, ge=0)


class OpenAPIDraftBody(_Body):
    spec: dict[str, Any]
    name: str = Field(min_length=1, max_length=80)
    base_url_env: str
    token_env: str | None = None
    operations: list[str] = Field(min_length=1)
    effects: dict[str, str] = Field(default_factory=dict)


def _problem(error: RegistryError) -> HTTPException:
    if error.code in {"release_not_found", "binding_not_found"}:
        status = 404
    elif error.code in {"revision_conflict", "version_already_published"}:
        status = 409
    else:
        status = 422
    return HTTPException(status_code=status, detail={"code": error.code})


def create_admin_router(deployment: Deployment) -> APIRouter:
    registry = deployment.registry
    settings = deployment.config.management
    if registry is None or settings is None:
        raise ValueError("management is not enabled")

    router = APIRouter()

    async def administrator(authorization: str | None = Header(default=None)) -> None:
        secret = deployment.environment.get(settings.admin_api_key_env, "")
        if (
            not secret
            or authorization is None
            or not authorization.startswith("Bearer ")
            or not compare_digest(authorization.removeprefix("Bearer ").encode(), secret.encode())
        ):
            raise HTTPException(status_code=401, detail={"code": "admin_unauthorized"})

    @router.get("/admin", response_class=HTMLResponse)
    async def page() -> Response:
        content = files("agenstra").joinpath("admin_ui.html").read_text(encoding="utf-8")
        return HTMLResponse(
            content,
            headers={
                "Cache-Control": "no-store",
                "Referrer-Policy": "no-referrer",
                "X-Content-Type-Options": "nosniff",
                "Content-Security-Policy": (
                    "default-src 'none'; script-src 'self'; style-src 'self'; "
                    "connect-src 'self'; img-src 'self' data:; base-uri 'none'; "
                    "form-action 'none'; frame-ancestors 'none'"
                ),
            },
        )

    @router.get("/admin/assets/{filename}")
    async def asset(filename: str) -> Response:
        if filename not in {"admin_ui.js", "admin_ui.css"}:
            raise HTTPException(status_code=404)
        content = files("agenstra").joinpath(filename).read_text(encoding="utf-8")
        media_type = "text/javascript" if filename.endswith(".js") else "text/css"
        return Response(content, media_type=media_type, headers={"Cache-Control": "no-store"})

    @router.get("/admin/api/overview", dependencies=[Depends(administrator)])
    async def overview() -> dict[str, object]:
        return {
            "users": sorted(deployment.config.users),
            "releases": await registry.list_packs(),
            "bindings": await registry.list_bindings(),
            "audit": await registry.audit(limit=30),
        }

    @router.post("/admin/api/validate", dependencies=[Depends(administrator)])
    async def validate(body: PackageBody) -> dict[str, object]:
        try:
            capabilities = await registry.validate(
                body.pack_id, body.version, body.manifest, body.skills
            )
        except RegistryError as error:
            raise _problem(error) from error
        return {"pack_id": body.pack_id, "version": body.version, "capabilities": capabilities}

    @router.post("/admin/api/openapi-draft", dependencies=[Depends(administrator)])
    async def openapi_draft(body: OpenAPIDraftBody) -> dict[str, Any]:
        try:
            if len(str(body.spec)) > 2_000_000:
                raise ValueError("OpenAPI document is too large")
            return import_openapi_document(
                body.spec,
                name=body.name,
                base_url_env=body.base_url_env,
                operations=body.operations,
                effects=body.effects,
                token_env=body.token_env,
            )
        except (ValueError, KeyError, TypeError) as error:
            raise HTTPException(
                status_code=422, detail={"code": "openapi_import_failed", "message": str(error)}
            ) from error

    @router.post("/admin/api/releases", dependencies=[Depends(administrator)])
    async def publish(body: PackageBody) -> dict[str, object]:
        try:
            return await registry.publish(body.pack_id, body.version, body.manifest, body.skills)
        except RegistryError as error:
            raise _problem(error) from error

    @router.post("/admin/api/packs/{pack_id}/activate", dependencies=[Depends(administrator)])
    async def activate(pack_id: str, body: ActivateBody) -> dict[str, object]:
        try:
            return await registry.activate(
                pack_id, body.digest, expected_revision=body.expected_revision
            )
        except RegistryError as error:
            raise _problem(error) from error

    @router.put("/admin/api/bindings/{owner_id}/{pack_id}", dependencies=[Depends(administrator)])
    async def put_binding(owner_id: str, pack_id: str, body: ConnectionConfig) -> dict[str, object]:
        if owner_id not in deployment.config.users:
            raise HTTPException(status_code=404, detail={"code": "owner_not_found"})
        if any(
            not _ENV.fullmatch(name)
            or not (
                _ENV.fullmatch(origin)
                or (
                    settings.secret_dir is not None
                    and origin.startswith("secret:")
                    and _ENV.fullmatch(origin.removeprefix("secret:"))
                )
            )
            for name, origin in body.environment.items()
        ):
            raise HTTPException(status_code=422, detail={"code": "invalid_environment_ref"})
        try:
            environment = {
                target: deployment._secret(origin) for target, origin in body.environment.items()
            }
            if body.identity is not None:
                deployment._secret(body.identity.url_env)
                deployment._secret(body.identity.token_env)
            active = await registry.active_release(pack_id)
            if active is None:
                raise RegistryError("release_not_active")
            path = await registry.release_path(pack_id, active)
            deployment._binding_id(owner_id, pack_id, path, body, environment)
            await registry.put_binding(owner_id, pack_id, body.model_dump(mode="json"))
        except DeploymentError as error:
            raise HTTPException(status_code=422, detail={"code": error.code}) from error
        except RegistryError as error:
            raise _problem(error) from error
        except (ValueError, OSError, TypeError) as error:
            raise HTTPException(status_code=422, detail={"code": "invalid_connection"}) from error
        return {"owner_id": owner_id, "pack_id": pack_id, "enabled": True}

    @router.delete(
        "/admin/api/bindings/{owner_id}/{pack_id}", dependencies=[Depends(administrator)]
    )
    async def disable_binding(owner_id: str, pack_id: str) -> dict[str, object]:
        try:
            await registry.disable_binding(owner_id, pack_id)
        except RegistryError as error:
            raise _problem(error) from error
        return {"owner_id": owner_id, "pack_id": pack_id, "enabled": False}

    @router.post(
        "/admin/api/bindings/{owner_id}/{pack_id}/check",
        dependencies=[Depends(administrator)],
    )
    async def check_binding(owner_id: str, pack_id: str) -> dict[str, object]:
        try:
            async with (
                asyncio.timeout(30),
                deployment.provider_factory(owner_id, pack_id) as provider,
            ):
                return {
                    "owner_id": owner_id,
                    "pack_id": pack_id,
                    "capabilities": sorted(provider.capabilities),
                    "skills": sorted(provider.skills),
                }
        except DeploymentError as error:
            raise HTTPException(status_code=422, detail={"code": error.code}) from error
        except Exception as error:
            raise HTTPException(
                status_code=422, detail={"code": "connection_check_failed"}
            ) from error

    @router.get("/admin/api/audit", dependencies=[Depends(administrator)])
    async def audit(limit: int = 100) -> list[dict[str, JsonValue]]:
        try:
            return await registry.audit(limit=limit)
        except RegistryError as error:
            raise _problem(error) from error

    return router
