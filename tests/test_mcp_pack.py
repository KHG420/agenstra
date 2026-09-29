import json

import pytest

pytest.importorskip("mcp")

from mcp import types
from mcp.server.lowlevel import Server
from mcp.shared.memory import create_connected_server_and_client_session

from agent_capability.broker import ToolBroker
from agent_capability.contracts import ToolCall
from agent_capability.mcp import (
    McpPack,
    McpPackManifest,
    bind_mcp_pack,
    contract_digest,
)


def catalog_tool():
    return types.Tool(
        name="Metrics.Capacity/v2",
        description="Read capacity for the requested resource.",
        inputSchema={
            "type": "object",
            "required": ["resource"],
            "additionalProperties": False,
            "properties": {"resource": {"$ref": "#/$defs/Resource"}},
            "$defs": {
                "Resource": {
                    "type": "object",
                    "required": ["code"],
                    "additionalProperties": False,
                    "properties": {"code": {"type": "integer", "minimum": 1}},
                }
            },
        },
        outputSchema={
            "type": "object",
            "required": ["capacity", "unit"],
            "properties": {"capacity": {"type": "number"}, "unit": {"const": "units/day"}},
            "additionalProperties": False,
        },
    )


def manifest(tool):
    return {
        "schema": "agent-capability.mcp-pack.v1",
        "name": "metrics",
        "version": "1",
        "guidance": "Use actual resource capacity and retain the unit.",
        "source": {"transport": "stdio", "command": "unused"},
        "tools": [{"name": tool.name, "effect": "read", "contract_sha256": contract_digest(tool)}],
    }


async def test_generic_mcp_adapter_retains_response_shape(tmp_path):
    tool = catalog_tool()
    calls = []
    server = Server("metrics")

    @server.list_tools()
    async def list_tools():
        return [tool]

    @server.call_tool(validate_input=False)
    async def invoke(name, arguments):
        calls.append((name, arguments))
        return types.CallToolResult(
            content=[],
            structuredContent={
                "capacity": 2400,
                "unit": "units/day",
            },
        )

    path = tmp_path / "pack.json"
    path.write_text(json.dumps(manifest(tool)))
    async with create_connected_server_and_client_session(server) as session:
        pack = await bind_mcp_pack(path, session)
        broker = ToolBroker(pack)
        call = ToolCall(
            call_ref="capacity",
            capability=tool.name,
            arguments={"resource": {"code": 8}},
            reason="Read current resource capacity",
        )
        invalid = call.model_copy(update={"arguments": {"resource": {"code": "8"}}})
        assert (await broker.execute(invalid)).error_code == "capability_input_invalid"
        assert calls == []
        result = await broker.execute(call)
        assert result.fact.value == {"data": {"capacity": 2400, "unit": "units/day"}}
        assert calls == [(tool.name, {"resource": {"code": 8}})]
        assert not pack.skills


def test_reviewed_schema_drift_prevents_import():
    tool = catalog_tool()
    config = McpPackManifest.model_validate(manifest(tool))
    changed = tool.model_copy(update={"description": "A changed contract"})
    with pytest.raises(ValueError, match="capability contract changed"):
        McpPack(None, config, {tool.name: changed}, {})


async def test_timeout_is_an_unknown_outcome_and_never_automatically_resubmitted():
    class TimeoutSession:
        count = 0

        async def call_tool(self, name, arguments):
            self.count += 1
            raise TimeoutError

    tool = catalog_tool()
    session = TimeoutSession()
    config = McpPackManifest.model_validate(manifest(tool))
    pack = McpPack(session, config, {tool.name: tool}, {})
    result = await pack.invoke(tool.name, {"resource": {"code": 8}})
    assert result.error_code == "provider_outcome_unknown"
    assert session.count == 1
