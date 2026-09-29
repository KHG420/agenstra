import json

import pytest

pytest.importorskip("mcp")

from mcp import types
from mcp.server.lowlevel import Server
from mcp.shared.memory import create_connected_server_and_client_session

from enterprise_agent.broker import ToolBroker
from enterprise_agent.contracts import ToolCall
from enterprise_agent.mcp import (
    McpPack,
    McpPackManifest,
    bind_mcp_pack,
    contract_digest,
)


def catalog_tool():
    return types.Tool(
        name="Factory.Capacity/v2",
        description="Read capacity for the requested production line.",
        inputSchema={
            "type": "object",
            "required": ["line"],
            "additionalProperties": False,
            "properties": {"line": {"$ref": "#/$defs/Line"}},
            "$defs": {
                "Line": {
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
        "schema": "enterprise.mcp-pack.v1",
        "name": "factory",
        "version": "1",
        "guidance": "Use actual production capacity and retain the unit.",
        "source": {"transport": "stdio", "command": "unused"},
        "tools": [{"name": tool.name, "effect": "read", "contract_sha256": contract_digest(tool)}],
    }


async def test_unrelated_enterprise_uses_same_adapter_and_retains_its_response_shape(tmp_path):
    tool = catalog_tool()
    calls = []
    server = Server("factory")

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
            arguments={"line": {"code": 8}},
            reason="Read current production capacity",
        )
        invalid = call.model_copy(update={"arguments": {"line": {"code": "8"}}})
        assert (await broker.execute(invalid)).error_code == "capability_input_invalid"
        assert calls == []
        result = await broker.execute(call)
        assert result.fact.value == {"data": {"capacity": 2400, "unit": "units/day"}}
        assert calls == [(tool.name, {"line": {"code": 8}})]
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
    result = await pack.invoke(tool.name, {"line": {"code": 8}})
    assert result.error_code == "provider_outcome_unknown"
    assert session.count == 1
