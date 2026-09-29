"""Optional MCP connection adapter; no enterprise-specific imports or response rewrites."""

import asyncio
import hashlib
import json
import os
import re
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager
from datetime import datetime, timedelta
from pathlib import Path
from typing import Literal, cast

import httpx
from anyio import BrokenResourceError, ClosedResourceError
from jsonschema import Draft202012Validator, FormatChecker
from jsonschema.exceptions import ValidationError as SchemaValidationError
from mcp import ClientSession, StdioServerParameters, types
from mcp.client.stdio import stdio_client
from mcp.client.streamable_http import streamable_http_client
from mcp.shared.exceptions import McpError
from pydantic import Field, JsonValue, model_validator

from enterprise_agent.contracts import StrictModel
from enterprise_agent.providers import (
    CapabilityDescription,
    CapabilityResult,
    InvocationContext,
    OperationBinding,
    Skill,
    agent_prompt,
)
from enterprise_agent.skills import SkillFile, load_skill_files


class McpSource(StrictModel):
    transport: Literal["stdio", "streamable_http"]
    command: str | None = None
    args: tuple[str, ...] = ()
    cwd_env: str | None = None
    environment: dict[str, str] = Field(default_factory=dict)
    url_env: str | None = None
    token_env: str | None = None
    timeout_seconds: float = Field(default=60, gt=0, le=300)

    @model_validator(mode="after")
    def valid_transport(self) -> "McpSource":
        if self.transport == "stdio":
            if not self.command or self.url_env or self.token_env:
                raise ValueError("stdio requires a command and environment-based credentials")
        elif not self.url_env or self.command or self.args or self.cwd_env or self.environment:
            raise ValueError("streamable_http requires url_env and optional token_env")
        return self


class ToolExposure(StrictModel):
    name: str
    effect: Literal["read", "compute", "write", "destructive"]
    optional: bool = False
    skills: tuple[str, ...] = ()
    # Digest is of the reviewed MCP Tool, including nested schemas and annotations.
    contract_sha256: str = Field(pattern=r"^[a-f0-9]{64}$")
    replay: Literal["never", "safe", "idempotent"] = "never"
    idempotency_argument: tuple[str, ...] | None = None
    reference_scope: Literal["durable", "connection"] = "durable"
    expires_at_path: tuple[str, ...] = ()
    approval_required: bool = False
    operation: OperationBinding | None = None


class McpPackManifest(StrictModel):
    schema_: Literal["enterprise.mcp-pack.v1"] = Field(alias="schema")
    name: str
    version: str
    guidance: str = Field(min_length=1, max_length=8_000)
    source: McpSource
    tools: tuple[ToolExposure, ...] = Field(min_length=1)
    skills: tuple[SkillFile, ...] = ()
    error_code_path: tuple[str, ...] = ()


def contract_digest(tool: types.Tool) -> str:
    serialized = json.dumps(
        tool.model_dump(mode="json", by_alias=True, exclude_none=True),
        sort_keys=True,
        ensure_ascii=False,
        separators=(",", ":"),
    )
    return hashlib.sha256(serialized.encode()).hexdigest()


def _schema_validator(schema: dict[str, object]) -> Draft202012Validator:
    # Connection metadata cannot make validation fetch arbitrary external resources.
    def local_refs(value: object) -> None:
        if isinstance(value, dict):
            for key, item in value.items():
                if key in {"$ref", "$dynamicRef"} and (
                    not isinstance(item, str) or not item.startswith("#")
                ):
                    raise ValueError("capability schema must use local references")
                local_refs(item)
        elif isinstance(value, list):
            for item in value:
                local_refs(item)

    local_refs(schema)
    Draft202012Validator.check_schema(schema)
    return Draft202012Validator(schema, format_checker=FormatChecker())


class McpPack:
    """A catalog bound to one authenticated, already initialized MCP session."""

    def __init__(
        self,
        session: ClientSession,
        manifest: McpPackManifest,
        tools: Mapping[str, types.Tool],
        skills: Mapping[str, Skill],
    ) -> None:
        self._session = session
        self._manifest = manifest
        self._skills = skills
        self._capabilities: dict[str, CapabilityDescription] = {}
        self._inputs: dict[str, Draft202012Validator] = {}
        self._outputs: dict[str, Draft202012Validator] = {}
        self._exposures: dict[str, ToolExposure] = {}
        seen: set[str] = set()
        for exposure in manifest.tools:
            if exposure.name in seen:
                raise ValueError(f"duplicate capability: {exposure.name}")
            seen.add(exposure.name)
            if not set(exposure.skills).issubset(skills):
                raise ValueError(f"unknown skill for capability: {exposure.name}")
            tool = tools.get(exposure.name)
            if tool is None:
                if exposure.optional:
                    continue
                raise ValueError(f"required capability unavailable: {exposure.name}")
            if contract_digest(tool) != exposure.contract_sha256:
                raise ValueError(f"capability contract changed: {exposure.name}")
            if tool.outputSchema is None:
                raise ValueError(f"structured output schema required: {exposure.name}")
            self._inputs[tool.name] = _schema_validator(tool.inputSchema)
            self._exposures[tool.name] = exposure
            self._outputs[tool.name] = _schema_validator(tool.outputSchema)
            self._capabilities[tool.name] = CapabilityDescription(
                name=tool.name,
                version=manifest.version,
                description=tool.description or tool.name,
                input_schema=cast(dict[str, JsonValue], tool.inputSchema),
                effect=exposure.effect,
                skills=exposure.skills,
                output_schema=cast(dict[str, JsonValue], tool.outputSchema),
                replay=exposure.replay,
                idempotency_argument=exposure.idempotency_argument,
                reference_scope=exposure.reference_scope,
                approval_required=exposure.approval_required,
                operation=exposure.operation,
            )

    @property
    def capabilities(self) -> Mapping[str, CapabilityDescription]:
        return self._capabilities

    @property
    def skills(self) -> Mapping[str, Skill]:
        return self._skills

    def system_prompt(self) -> str:
        return agent_prompt(self._manifest.guidance)

    async def invoke(
        self,
        name: str,
        arguments: dict[str, JsonValue],
        *,
        context: InvocationContext | None = None,
    ) -> CapabilityResult:
        if name not in self._capabilities:
            return CapabilityResult(error_code="capability_unknown")
        try:
            self._inputs[name].validate(arguments)
        except SchemaValidationError:
            return CapabilityResult(error_code="capability_input_invalid")
        try:
            # No transparent retry: a timeout may mean a remote write was already accepted.
            async with asyncio.timeout(self._manifest.source.timeout_seconds):
                reply = await self._session.call_tool(name, arguments)
        except (McpError, httpx.HTTPError, TimeoutError, BrokenResourceError, ClosedResourceError):
            return CapabilityResult(error_code="provider_outcome_unknown")
        except RuntimeError:
            # The SDK can reject a malformed structured response before returning it to us.
            return CapabilityResult(error_code="upstream_response_invalid")
        if reply.isError:
            code: object = reply.structuredContent
            for key in self._manifest.error_code_path:
                code = code.get(key) if isinstance(code, dict) else None
            safe = isinstance(code, str) and re.fullmatch(r"[A-Za-z0-9_.:-]{1,120}", code)
            return CapabilityResult(error_code=code if safe else "capability_failed")
        data = reply.structuredContent
        if data is None:
            return CapabilityResult(error_code="upstream_response_invalid")
        try:
            self._outputs[name].validate(data)
            exposure = self._exposures[name]
            expires_at = None
            if exposure.expires_at_path:
                raw: object = data
                for key in exposure.expires_at_path:
                    raw = raw.get(key) if isinstance(raw, dict) else None
                if not isinstance(raw, str):
                    return CapabilityResult(error_code="upstream_response_invalid")
                expires_at = datetime.fromisoformat(raw.replace("Z", "+00:00"))
                if expires_at.utcoffset() is None:
                    return CapabilityResult(error_code="upstream_response_invalid")
            return CapabilityResult(
                data=cast(dict[str, JsonValue], data),
                reference_scope=exposure.reference_scope,
                expires_at=expires_at,
            )
        except (SchemaValidationError, ValueError):
            return CapabilityResult(error_code="upstream_response_invalid")


async def bind_mcp_pack(path: str | Path, session: ClientSession) -> McpPack:
    """Bind a trusted package to an initialized session; caller owns that session."""
    path = Path(path)
    manifest = McpPackManifest.model_validate_json(path.read_text(encoding="utf-8"))
    skills = load_skill_files(manifest.skills, path.parent)
    tools: dict[str, types.Tool] = {}
    cursor: str | None = None
    cursors: set[str] = set()
    while True:
        page = await session.list_tools(cursor=cursor)
        for tool in page.tools:
            if tool.name in tools:
                raise ValueError(f"duplicate remote capability: {tool.name}")
            tools[tool.name] = tool
        cursor = page.nextCursor
        if cursor is None:
            break
        if cursor in cursors:
            raise ValueError("capability pagination repeated cursor")
        cursors.add(cursor)
    return McpPack(session, manifest, tools, skills)


def _required(environment: Mapping[str, str], name: str) -> str:
    value = environment.get(name, "").strip()
    if not value:
        raise ValueError(f"missing environment variable: {name}")
    return value


@asynccontextmanager
async def open_mcp_pack(
    path: str | Path,
    *,
    environment: Mapping[str, str] | None = None,
) -> AsyncIterator[McpPack]:
    """Create and close a connection for one principal. Never accept user-authored manifests."""
    manifest = McpPackManifest.model_validate_json(Path(path).read_text(encoding="utf-8"))
    env = environment if environment is not None else os.environ
    source = manifest.source
    timeout = timedelta(seconds=source.timeout_seconds)
    if source.transport == "stdio":
        assert source.command is not None
        parameters = StdioServerParameters(
            command=source.command,
            args=list(source.args),
            cwd=_required(env, source.cwd_env) if source.cwd_env else None,
            env={target: _required(env, origin) for target, origin in source.environment.items()},
        )
        async with (
            stdio_client(parameters) as (reader, writer),
            ClientSession(reader, writer, read_timeout_seconds=timeout) as session,
        ):
            await session.initialize()
            yield await bind_mcp_pack(path, session)
    else:
        assert source.url_env is not None
        url = _required(env, source.url_env)
        parsed = httpx.URL(url)
        if parsed.scheme not in {"http", "https"} or not parsed.host:
            raise ValueError("invalid MCP URL")
        headers = (
            {"Authorization": f"Bearer {_required(env, source.token_env)}"}
            if (source.token_env)
            else {}
        )
        async with (
            httpx.AsyncClient(headers=headers, follow_redirects=False) as http,
            streamable_http_client(url, http_client=http) as (reader, writer, _),
            ClientSession(reader, writer, read_timeout_seconds=timeout) as session,
        ):
            await session.initialize()
            yield await bind_mcp_pack(path, session)
