"""Authorize a capability call and record domain-neutral Fact provenance."""

from copy import deepcopy
from dataclasses import dataclass
from datetime import UTC, datetime
from uuid import uuid4

from agent_capability.contracts import Fact, ToolCall
from agent_capability.providers import CapabilityDescription, CapabilityProvider, InvocationContext


def bind_idempotency(
    call: ToolCall, capability: CapabilityDescription, context: InvocationContext
) -> ToolCall:
    path = capability.idempotency_argument
    if path is None:
        return call
    if not path:
        raise ValueError("idempotency_argument_invalid")
    arguments = deepcopy(call.arguments)
    cursor = arguments
    for key in path[:-1]:
        value = cursor.setdefault(key, {})
        if not isinstance(value, dict):
            raise ValueError("capability_input_invalid")
        cursor = value
    cursor[path[-1]] = context.idempotency_key
    return call.model_copy(update={"arguments": arguments})


@dataclass(frozen=True, slots=True)
class CallOutcome:
    fact: Fact | None = None
    error_code: str | None = None


class ToolBroker:
    def __init__(
        self, pack: CapabilityProvider, *, granted_capabilities: frozenset[str] = frozenset()
    ) -> None:
        self._pack = pack
        self._grants = granted_capabilities

    async def execute(
        self, call: ToolCall, *, context: InvocationContext | None = None
    ) -> CallOutcome:
        capability = self._pack.capabilities.get(call.capability)
        if capability is None:
            return CallOutcome(error_code="capability_unknown")
        # Grants come from the authenticated embedding host, never a model decision or skill.
        if capability.effect != "read" and capability.name not in self._grants:
            return CallOutcome(error_code="capability_not_granted")
        if context is not None:
            try:
                call = bind_idempotency(call, capability, context)
            except ValueError as exc:
                return CallOutcome(error_code=str(exc))
        result = await self._pack.invoke(call.capability, call.arguments, context=context)
        if result.error_code is not None or result.data is None:
            return CallOutcome(error_code=result.error_code or "upstream_response_invalid")
        return CallOutcome(
            fact=Fact(
                fact_id=uuid4(),
                source_capability=capability.name,
                source_version=capability.version,
                value={"data": result.data},
                observed_at=datetime.now(UTC),
                reference_scope=(
                    "connection"
                    if "connection" in {result.reference_scope, capability.reference_scope}
                    else "durable"
                ),
                connection_id=context.connection_id if context else None,
                expires_at=result.expires_at,
            )
        )
