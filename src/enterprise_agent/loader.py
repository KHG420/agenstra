"""Open trusted local packs without coupling the agent to a transport implementation."""

import json
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager
from pathlib import Path

from enterprise_agent.packs import load_pack
from enterprise_agent.providers import CapabilityProvider


@asynccontextmanager
async def open_pack(
    path: str | Path,
    *,
    environment: Mapping[str, str] | None = None,
) -> AsyncIterator[CapabilityProvider]:
    schema = json.loads(Path(path).read_text(encoding="utf-8")).get("schema")
    if schema == "enterprise.capability-pack.v1":
        pack = load_pack(path, environment=environment)
        try:
            yield pack
        finally:
            await pack.aclose()
    elif schema == "enterprise.rest-pack.v2":
        from enterprise_agent.rest import load_rest_pack

        rest_pack = load_rest_pack(path, environment=environment)
        try:
            yield rest_pack
        finally:
            await rest_pack.aclose()
    elif schema == "enterprise.mcp-pack.v1":
        try:
            from enterprise_agent.mcp import open_mcp_pack
        except ModuleNotFoundError as exc:
            raise RuntimeError("MCP packs require enterprise-agent-framework[mcp]") from exc
        async with open_mcp_pack(path, environment=environment) as mcp_pack:
            yield mcp_pack
    else:
        raise ValueError(f"unsupported capability pack schema: {schema}")
