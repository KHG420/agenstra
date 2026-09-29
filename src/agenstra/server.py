"""Authenticated HTTP surface for a deployment-owned AgentHost."""

import asyncio
import logging
from collections.abc import AsyncIterator, Awaitable, Callable
from contextlib import asynccontextmanager, suppress
from dataclasses import asdict
from typing import TYPE_CHECKING, cast

from fastapi import Depends, FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict, Field

from agenstra.deployment import DeploymentError
from agenstra.storage import LeaseLost, RunNotFound, StoreConflict, StoredRun

if TYPE_CHECKING:
    from agenstra.deployment import Deployment
    from agenstra.host import AgentHost

_LOG = logging.getLogger(__name__)


class _Body(BaseModel):
    model_config = ConfigDict(extra="forbid")


class CreateRunBody(_Body):
    pack_id: str = Field(min_length=1, max_length=128)
    instruction: str = Field(min_length=1, max_length=30_000)
    request_id: str | None = Field(default=None, min_length=1, max_length=128)


class InputBody(_Body):
    field: str = Field(min_length=1)
    text: str
    revision: int = Field(ge=0)


class ApprovalBody(_Body):
    invocation_id: str = Field(min_length=1)
    arguments_sha256: str = Field(min_length=64, max_length=64)
    revision: int = Field(ge=0)
    approved: bool = True


def _run_view(run: StoredRun) -> dict[str, object]:
    view = asdict(run)
    # Lease tokens are worker fencing credentials, never public response data.
    view.pop("lease_token")
    view.pop("lease_until")
    return view


def create_app(
    host: "AgentHost",
    authenticator: Callable[[str], Awaitable[str]],
    *,
    worker_interval_seconds: float = 1,
    worker_enabled: bool = True,
    on_shutdown: Callable[[], Awaitable[None]] | None = None,
    deployment: "Deployment | None" = None,
) -> FastAPI:
    if worker_interval_seconds <= 0:
        raise ValueError("worker_interval_seconds must be positive")
    ready = False
    worker_failed = False
    worker_task: asyncio.Task[None] | None = None

    async def worker() -> None:
        nonlocal worker_failed
        while True:
            try:
                await host.wake_due()
                worker_failed = False
            except asyncio.CancelledError:
                raise
            except Exception:
                worker_failed = True
                _LOG.error("agent worker cycle failed")
            await asyncio.sleep(worker_interval_seconds)

    @asynccontextmanager
    async def lifespan(_: FastAPI) -> AsyncIterator[None]:
        nonlocal ready, worker_task
        if deployment is not None and deployment.registry is not None:
            management = deployment.config.management
            assert management is not None
            admin_key = deployment.environment.get(management.admin_api_key_env, "")
            if len(admin_key) < 24 or any(
                admin_key == deployment.environment.get(user.api_key_env)
                for user in deployment.config.users.values()
            ):
                raise RuntimeError(
                    "management requires a distinct admin key of at least 24 characters"
                )
            await deployment.registry.initialize()
        await host.store.initialize()
        ready = True
        worker_task = asyncio.create_task(worker()) if worker_enabled else None
        try:
            yield
        finally:
            ready = False
            if worker_task is not None:
                worker_task.cancel()
                with suppress(asyncio.CancelledError):
                    await worker_task
                worker_task = None
            if on_shutdown is not None:
                await on_shutdown()

    app = FastAPI(lifespan=lifespan)
    if deployment is not None and deployment.registry is not None:
        from agenstra.admin import create_admin_router

        app.include_router(create_admin_router(deployment))

    async def owner_id(authorization: str | None = Header(default=None)) -> str:
        if authorization is None or not authorization.startswith("Bearer "):
            raise HTTPException(status_code=401, detail={"code": "unauthorized"})
        token = authorization.removeprefix("Bearer ")
        if not token or token != token.strip():
            raise HTTPException(status_code=401, detail={"code": "unauthorized"})
        try:
            return await authenticator(token)
        except DeploymentError:
            raise HTTPException(status_code=401, detail={"code": "unauthorized"}) from None

    @app.exception_handler(RunNotFound)
    async def not_found(_: Request, __: RunNotFound) -> JSONResponse:
        return JSONResponse(status_code=404, content={"code": "not_found"})

    @app.exception_handler(StoreConflict)
    @app.exception_handler(LeaseLost)
    async def conflict(_: Request, __: StoreConflict | LeaseLost) -> JSONResponse:
        return JSONResponse(status_code=409, content={"code": "conflict"})

    @app.exception_handler(DeploymentError)
    async def deployment_error(_: Request, error: DeploymentError) -> JSONResponse:
        status = 403 if error.code in {"access_denied", "identity_unverified"} else 503
        return JSONResponse(status_code=status, content={"code": error.code})

    from agenstra.host import HostError

    @app.exception_handler(HostError)
    async def host_error(_: Request, error: HostError) -> JSONResponse:
        status = 404 if error.code == "not_found" else 409
        if error.code in {
            "access_denied",
            "forbidden",
            "identity_unverified",
            "model_data_not_authorized",
        }:
            status = 403
        elif error.code in {"authorization_unavailable", "connection_unavailable"}:
            status = 503
        return JSONResponse(status_code=status, content={"code": error.code})

    @app.get("/healthz")
    async def healthz() -> dict[str, str]:
        return {"status": "ok"}

    @app.get("/readyz")
    async def readyz() -> JSONResponse:
        if (
            not ready
            or worker_failed
            or (worker_enabled and (worker_task is None or worker_task.done()))
        ):
            return JSONResponse(status_code=503, content={"status": "unavailable"})
        try:
            await host.store.list_runs(owner_id="__health__", limit=1)
        except Exception:
            return JSONResponse(status_code=503, content={"status": "unavailable"})
        return JSONResponse(status_code=200, content={"status": "ready"})

    @app.post("/runs")
    async def create_run(body: CreateRunBody, owner: str = Depends(owner_id)) -> dict[str, object]:
        run = await host.create(owner, body.pack_id, body.instruction, request_id=body.request_id)
        return _run_view(run)

    @app.get("/runs")
    async def list_runs(
        limit: int = 100, owner: str = Depends(owner_id)
    ) -> list[dict[str, object]]:
        if limit < 1 or limit > 1000:
            raise HTTPException(status_code=422, detail={"code": "invalid_limit"})
        runs = await host.store.list_runs(owner_id=owner, limit=limit)
        visible = []
        for run in runs:
            try:
                visible.append(_run_view(await host.get(run.run_id, owner_id=owner)))
            except RunNotFound:
                continue
            except HostError as error:
                if error.code in {
                    "access_denied",
                    "forbidden",
                    "identity_unverified",
                    "model_data_not_authorized",
                }:
                    continue
                raise
        return visible

    @app.get("/runs/{run_id}")
    async def get_run(run_id: str, owner: str = Depends(owner_id)) -> dict[str, object]:
        return _run_view(await host.get(run_id, owner_id=owner))

    @app.post("/runs/{run_id}/input")
    async def supply_input(
        run_id: str, body: InputBody, owner: str = Depends(owner_id)
    ) -> dict[str, object]:
        return _run_view(
            await host.supply_input(
                run_id,
                owner_id=owner,
                field=body.field,
                text=body.text,
                revision=body.revision,
            )
        )

    @app.post("/runs/{run_id}/approval")
    async def approve(
        run_id: str, body: ApprovalBody, owner: str = Depends(owner_id)
    ) -> dict[str, object]:
        return _run_view(
            await host.approve(
                run_id,
                owner_id=owner,
                invocation_id=body.invocation_id,
                arguments_sha256=body.arguments_sha256,
                revision=body.revision,
                approved=body.approved,
            )
        )

    @app.post("/runs/{run_id}/resume")
    async def resume(run_id: str, owner: str = Depends(owner_id)) -> dict[str, object]:
        return _run_view(await host.drive(run_id, owner_id=owner))

    @app.post("/runs/{run_id}/cancel")
    async def cancel(run_id: str, owner: str = Depends(owner_id)) -> dict[str, object]:
        return _run_view(await host.cancel(run_id, owner_id=owner))

    @app.get("/runs/{run_id}/events")
    async def events(
        run_id: str, after: int = 0, limit: int = 100, owner: str = Depends(owner_id)
    ) -> tuple[dict[str, object], ...]:
        if after < 0 or limit < 1 or limit > 1000:
            raise HTTPException(status_code=422, detail={"code": "invalid_page"})
        await host.get(run_id, owner_id=owner)
        return cast(
            tuple[dict[str, object], ...],
            await host.store.list_events(run_id, owner_id=owner, after=after, limit=limit),
        )

    @app.get("/runs/{run_id}/artifacts/{artifact_id}")
    async def artifact(
        run_id: str, artifact_id: str, owner: str = Depends(owner_id)
    ) -> dict[str, object]:
        await host.get(run_id, owner_id=owner)
        try:
            return cast(
                dict[str, object],
                await host.store.get_artifact(run_id, artifact_id, owner_id=owner),
            )
        except KeyError:
            raise HTTPException(status_code=404, detail={"code": "not_found"}) from None

    return app
