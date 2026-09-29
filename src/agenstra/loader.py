"""Open trusted local packs without coupling the agent to a transport implementation."""

import json
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager
from pathlib import Path

from agenstra.packs import load_pack
from agenstra.providers import CapabilityProvider


@asynccontextmanager
async def open_pack(
    path: str | Path,
    *,
    environment: Mapping[str, str] | None = None,
) -> AsyncIterator[CapabilityProvider]:
    schema = json.loads(Path(path).read_text(encoding="utf-8")).get("schema")
    if schema == "agenstra.capability-pack.v1":
        pack = load_pack(path, environment=environment)
        try:
            yield pack
        finally:
            await pack.aclose()
    elif schema == "agenstra.rest-pack.v2":
        from agenstra.rest import load_rest_pack

        rest_pack = load_rest_pack(path, environment=environment)
        try:
            yield rest_pack
        finally:
            await rest_pack.aclose()
    elif schema == "agenstra.mcp-pack.v1":
        try:
            from agenstra.mcp import open_mcp_pack
        except ModuleNotFoundError as exc:
            raise RuntimeError("MCP packs require agenstra[mcp]") from exc
        async with open_mcp_pack(path, environment=environment) as mcp_pack:
            yield mcp_pack
    else:
        raise ValueError(f"unsupported capability pack schema: {schema}")
