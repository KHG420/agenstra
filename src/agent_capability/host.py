"""Durable single-node host: scoped state, execution journal, approvals and job waiting."""

import asyncio
import hashlib
import json
import time
from collections.abc import Awaitable, Callable
from contextlib import AbstractAsyncContextManager, suppress
from copy import deepcopy
from typing import Any, cast
from uuid import NAMESPACE_URL, uuid4, uuid5

from pydantic import BaseModel, ConfigDict, Field, JsonValue

from agent_capability.broker import CallOutcome, ToolBroker, bind_idempotency
from agent_capability.contracts import DecisionModel, Fact, Observation, ToolCall
from agent_capability.providers import CapabilityProvider, InvocationContext, OperationBinding
from agent_capability.runtime import (
    AgentRuntime,
    FactReferenceError,
    arguments_digest,
    reference_available,
    resolve_argument,
)
from agent_capability.state import Invocation, OperationReceipt, RuntimeState
from agent_capability.storage import LeaseLost, SQLiteStore, StoreConflict, StoredRun


class HostError(RuntimeError):
    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


class ExecutionPolicy(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)
    granted_capabilities: frozenset[str] = frozenset()
    approval_capabilities: frozenset[str] = frozenset()
    allow_model_data: bool = False


class HostSettings(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)
    lease_seconds: float = Field(default=60, ge=3, le=3600)
    max_model_rounds: int = Field(default=30, ge=1, le=1000)
    max_tool_calls: int = Field(default=80, ge=1, le=10000)
    max_poll_calls: int = Field(default=720, ge=1, le=100000)
    max_run_seconds: float = Field(default=86400, gt=0)
    max_context_characters: int = Field(default=80000, ge=1000)
    max_artifact_bytes: int = Field(default=8_000_000, ge=1024)
    max_active_artifact_bytes: int = Field(default=64_000_000, ge=1024)
    max_state_bytes: int = Field(default=8_000_000, ge=1024)
    model_timeout_seconds: float = Field(default=60, gt=0)
    invocation_timeout_seconds: float = Field(default=300, gt=0)
    max_invocation_attempts: int = Field(default=3, ge=1, le=10)
    retry_interval_seconds: float = Field(default=5, ge=1)
    approval_seconds: float = Field(default=900, ge=1, le=86400)
    max_concurrent_runs: int = Field(default=4, ge=1, le=64)


ProviderFactory = Callable[[str, str], AbstractAsyncContextManager[CapabilityProvider]]
PolicyResolver = Callable[[str, str], Awaitable[ExecutionPolicy]]
_TERMINAL = {"completed", "failed", "cancelled"}
_AUTH_CODES = {
    "product_api_unauthorized",
    "product_api_forbidden",
    "upstream_http_401",
    "upstream_http_403",
    "unauthorized",
    "forbidden",
    "identity_unverified",
}
_UNKNOWN_CODES = {"provider_outcome_unknown", "upstream_response_invalid", "upstream_unavailable"}
_PAUSE_AUTH_CODES = _AUTH_CODES | {
    "authorization_unavailable",
    "model_data_not_authorized",
    "access_denied",
    "connection_unavailable",
    "capability_not_granted",
    "operation_poll_requires_unattended_access",
}


def _json(value: Any) -> dict[str, JsonValue]:
    return cast(dict[str, JsonValue], value)


def _canonical(value: Any) -> bytes:
    return json.dumps(
        value, sort_keys=True, ensure_ascii=False, separators=(",", ":"), allow_nan=False
    ).encode()


def _fingerprint(provider: CapabilityProvider) -> str:
    return hashlib.sha256(
        _canonical(
            {
                "capabilities": {
                    name: item.model_dump(mode="json")
                    for name, item in provider.capabilities.items()
                },
                "skills": {name: skill.content for name, skill in provider.skills.items()},
                "prompt": provider.system_prompt(),
                "binding": getattr(provider, "binding_id", ""),
            }
        )
    ).hexdigest()


def _value(data: JsonValue, path: tuple[str | int, ...]) -> JsonValue:
    value = data
    for part in path:
        if isinstance(value, dict) and isinstance(part, str) and part in value:
            value = value[part]
            continue
        if isinstance(value, list) and type(part) is int and 0 <= part < len(value):
            value = value[part]
            continue
        raise HostError("operation_contract_invalid")
    return value


def _argument(path: tuple[str, ...], value: JsonValue) -> dict[str, JsonValue]:
    root: dict[str, JsonValue] = {}
    cursor = root
    for key in path[:-1]:
        child: dict[str, JsonValue] = {}
        cursor[key] = child
        cursor = child
    cursor[path[-1]] = value
    return root


class AgentHost:
    def __init__(
        self,
        *,
        store: SQLiteStore,
        provider_factory: ProviderFactory,
        model: DecisionModel,
        policy_resolver: PolicyResolver,
        settings: HostSettings | None = None,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.store = store
        self.provider_factory = provider_factory
        self.model = model
        self.policy_resolver = policy_resolver
        self.settings = settings or HostSettings()
        self.clock = clock
        self._slots = asyncio.Semaphore(self.settings.max_concurrent_runs)

    async def _policy(
        self, owner_id: str, pack_id: str, *, model_data: bool = False
    ) -> ExecutionPolicy:
        try:
            policy = await self.policy_resolver(owner_id, pack_id)
        except HostError:
            raise
        except Exception as exc:
            raise HostError("authorization_unavailable") from exc
        if model_data and not policy.allow_model_data:
            raise HostError("model_data_not_authorized")
        return policy

    async def create(
        self,
        owner_id: str,
        pack_id: str,
        instruction: str,
        *,
        request_id: str | None = None,
    ) -> StoredRun:
        if not instruction.strip() or len(instruction) > 30_000:
            raise HostError("instruction_invalid")
        await self._policy(owner_id, pack_id, model_data=True)
        request_key = _canonical([owner_id, request_id]).decode()
        run_id = str(uuid5(NAMESPACE_URL, request_key)) if request_id else str(uuid4())
        state = AgentRuntime.new_state(instruction, run_id=run_id)
        envelope: dict[str, JsonValue] = {
            "runtime": state.model_dump(mode="json", exclude={"facts"}),
            "artifact_ids": [],
            "pack_fingerprint": None,
        }
        try:
            return await self.store.create_run(
                owner_id=owner_id, pack_id=pack_id, state=envelope, run_id=run_id
            )
        except StoreConflict:
            existing = await self.store.get_run(run_id, owner_id=owner_id)
            original = existing.state.get("runtime")
            if (
                existing.pack_id != pack_id
                or not isinstance(original, dict)
                or original.get("instruction") != instruction
            ):
                raise HostError("request_id_conflict") from None
            return existing

    async def get(self, run_id: str, *, owner_id: str) -> StoredRun:
        run = await self.store.get_run(run_id, owner_id=owner_id)
        await self._policy(owner_id, run.pack_id)
        return run

    async def _restore(self, run: StoredRun) -> RuntimeState:
        payload = deepcopy(run.state["runtime"])
        if not isinstance(payload, dict):
            raise HostError("run_state_invalid")
        ids = run.state.get("artifact_ids", [])
        if not isinstance(ids, list) or any(not isinstance(item, str) for item in ids):
            raise HostError("run_state_invalid")
        payload["facts"] = [
            await self.store.get_artifact(run.run_id, str(item), owner_id=run.owner_id)
            for item in ids
        ]
        return RuntimeState.model_validate(payload)

    async def _save(
        self,
        run: StoredRun,
        state: RuntimeState,
        *,
        fingerprint: str | None = None,
        next_wake_at: float | None = None,
        event: dict[str, JsonValue] | None = None,
    ) -> StoredRun:
        assert run.lease_token is not None
        envelope = {
            "runtime": state.model_dump(mode="json", exclude={"facts"}),
            "artifact_ids": [str(fact.fact_id) for fact in state.facts],
            "pack_fingerprint": fingerprint or run.state.get("pack_fingerprint"),
        }
        if len(_canonical(envelope)) > self.settings.max_state_bytes:
            raise HostError("run_state_too_large")
        if sum(len(fact.model_dump_json().encode()) for fact in state.facts) > (
            self.settings.max_active_artifact_bytes
        ):
            raise HostError("run_artifacts_too_large")
        return await self.store.checkpoint(
            run.run_id,
            owner_id=run.owner_id,
            lease_token=run.lease_token,
            state=_json(envelope),
            status=state.status,
            next_wake_at=next_wake_at,
            invocations=tuple(item.model_dump(mode="json") for item in state.pending),
            artifacts={str(fact.fact_id): fact.model_dump(mode="json") for fact in state.facts},
            events=(event,) if event else (),
        )

    async def supply_input(
        self,
        run_id: str,
        *,
        owner_id: str,
        field: str,
        text: str,
        revision: int,
    ) -> StoredRun:
        current = await self.get(run_id, owner_id=owner_id)
        if not text.strip() or len(text) > 30_000:
            raise HostError("input_invalid")
        run = await self.store.claim(
            run_id, owner_id=owner_id, lease_seconds=self.settings.lease_seconds
        )
        try:
            state = await self._restore(run)
            if run.revision != revision or current.revision != revision:
                raise HostError("revision_conflict")
            if state.status != "needs_input" or state.input_field != field:
                raise HostError("input_not_requested")
            state.followups.append(f"{field}: {text}")
            state.input_field = state.input_prompt = None
            state.status, state.error_code = "queued", None
            return await self._save(run, state, event={"kind": "input_received", "field": field})
        finally:
            await self.store.release(run_id, owner_id=owner_id, lease_token=run.lease_token or "")

    async def approve(
        self,
        run_id: str,
        *,
        owner_id: str,
        invocation_id: str,
        arguments_sha256: str,
        revision: int,
        approved: bool = True,
    ) -> StoredRun:
        await self.get(run_id, owner_id=owner_id)
        run = await self.store.claim(
            run_id, owner_id=owner_id, lease_seconds=self.settings.lease_seconds
        )
        try:
            state = await self._restore(run)
            if run.revision != revision:
                raise HostError("revision_conflict")
            item = next(
                (item for item in state.pending if item.invocation_id == invocation_id), None
            )
            if state.status != "needs_approval" or item is None or item.status != "needs_approval":
                raise HostError("approval_not_requested")
            if item.arguments_sha256 != arguments_sha256:
                raise HostError("approval_arguments_changed")
            if item.approval_expires_at is None or self.clock() >= item.approval_expires_at:
                raise HostError("approval_expired")
            if approved:
                item.approved_hash = arguments_sha256
                item.approved_until = item.approval_expires_at
                item.status = "prepared"
            else:
                AgentRuntime.observe(state, item, CallOutcome(error_code="approval_denied"))
            state.status = "queued"
            return await self._save(
                run,
                state,
                event={
                    "kind": "approval_granted" if approved else "approval_denied",
                    "invocation_id": invocation_id,
                    "arguments_sha256": arguments_sha256,
                },
            )
        finally:
            await self.store.release(run_id, owner_id=owner_id, lease_token=run.lease_token or "")

    async def cancel(self, run_id: str, *, owner_id: str) -> StoredRun:
        run = await self.get(run_id, owner_id=owner_id)
        if run.status in _TERMINAL:
            return run
        return await self.store.request_cancel(run_id, owner_id=owner_id)

    def _context(self, run: StoredRun, item: Invocation, connection_id: str) -> InvocationContext:
        return InvocationContext(
            run_id=run.run_id,
            owner_id=run.owner_id,
            invocation_id=item.invocation_id,
            idempotency_key=item.invocation_id,
            connection_id=connection_id,
        )

    @staticmethod
    def _poll_access(
        provider: CapabilityProvider,
        policy: ExecutionPolicy,
        binding: OperationBinding,
    ) -> None:
        capability = provider.capabilities.get(binding.poll_capability)
        if capability is None or capability.effect != "read":
            raise HostError("operation_poll_must_be_read")
        if capability.approval_required or capability.name in policy.approval_capabilities:
            raise HostError("operation_poll_requires_unattended_access")

    async def _execute(
        self,
        run: StoredRun,
        state: RuntimeState,
        item: Invocation,
        provider: CapabilityProvider,
        runtime: AgentRuntime,
    ) -> StoredRun:
        capability = provider.capabilities.get(item.call.capability)
        policy = await self._policy(run.owner_id, run.pack_id, model_data=True)
        if capability is None:
            runtime.observe(state, item, CallOutcome(error_code="capability_unknown"))
            return await self._save(run, state)
        if capability.operation is not None:
            self._poll_access(provider, policy, capability.operation)
        if capability.effect != "read" and capability.name not in policy.granted_capabilities:
            state.status, state.error_code = "needs_authorization", "capability_not_granted"
            return await self._save(
                run, state, event={"kind": "call_denied", "invocation_id": item.invocation_id}
            )
        context = self._context(run, item, runtime.connection_id)
        try:
            # Validate the original bindings on every send. A saved service ID can expire
            # while waiting for approval or when a process opens a new MCP session.
            resolved = {
                key: resolve_argument(
                    value,
                    {fact.fact_id: fact for fact in state.facts},
                    connection_id=runtime.connection_id,
                )
                for key, value in item.original_arguments.items()
            }
            normalized = bind_idempotency(
                item.call.model_copy(update={"arguments": resolved}), capability, context
            )
        except FactReferenceError as exc:
            if item.attempts:
                item.status, item.error_code = "unknown", str(exc)
                state.status, state.error_code = "needs_reconciliation", str(exc)
            else:
                runtime.observe(state, item, CallOutcome(error_code=str(exc)))
            return await self._save(
                run,
                state,
                event={"kind": "reference_unavailable", "invocation_id": item.invocation_id},
            )
        except ValueError:
            runtime.observe(state, item, CallOutcome(error_code="capability_input_invalid"))
            return await self._save(run, state)
        digest = arguments_digest(normalized)
        if item.arguments_sha256 and item.arguments_sha256 != digest:
            raise HostError("invocation_arguments_changed")
        item.call, item.arguments_sha256 = normalized, digest
        # Recover a request persisted before send, or a response lost after the external commit.
        if item.status in {"in_flight", "unknown"}:
            if (
                capability.replay == "never"
                or item.attempts >= self.settings.max_invocation_attempts
            ):
                item.status = "unknown"
                state.status, state.error_code = "needs_reconciliation", "provider_outcome_unknown"
                return await self._save(
                    run,
                    state,
                    event={"kind": "reconciliation_required", "invocation_id": item.invocation_id},
                )
            item.status = "prepared"
        needs_approval = (
            capability.approval_required or capability.name in policy.approval_capabilities
        )
        valid_approval = (
            item.approved_hash == digest
            and item.approved_until is not None
            and self.clock() < item.approved_until
        )
        if needs_approval and not valid_approval:
            item.status = "needs_approval"
            item.approval_expires_at = self.clock() + self.settings.approval_seconds
            item.approved_hash = item.approved_until = None
            state.status = "needs_approval"
            return await self._save(
                run,
                state,
                event={
                    "kind": "approval_requested",
                    "invocation_id": item.invocation_id,
                    "arguments_sha256": digest,
                },
            )
        # Commit the exact input and stable idempotency key before any external request.
        item.status = "in_flight"
        item.attempts += 1
        run = await self._save(
            run,
            state,
            event={
                "kind": "call_started",
                "invocation_id": item.invocation_id,
                "capability": capability.name,
                "arguments_sha256": digest,
            },
        )
        try:
            async with asyncio.timeout(self.settings.invocation_timeout_seconds):
                outcome = await ToolBroker(
                    provider, granted_capabilities=policy.granted_capabilities
                ).execute(item.call, context=context)
        except Exception:
            outcome = CallOutcome(error_code="provider_outcome_unknown")
        if outcome.error_code in _AUTH_CODES:
            # Authorization failures are definite denials. The original input remains prepared.
            item.status, item.error_code = "prepared", outcome.error_code
            state.status, state.error_code = "needs_authorization", outcome.error_code
            return await self._save(run, state, event={"kind": "authorization_required"})
        if outcome.error_code in _UNKNOWN_CODES and (
            capability.effect != "read" or outcome.error_code == "provider_outcome_unknown"
        ):
            item.status, item.error_code = "unknown", outcome.error_code
            replayable = capability.replay != "never" and (
                item.attempts < self.settings.max_invocation_attempts
            )
            state.status = "waiting" if replayable else "needs_reconciliation"
            state.error_code = outcome.error_code
            return await self._save(
                run,
                state,
                next_wake_at=self.clock() + self.settings.retry_interval_seconds
                if replayable
                else None,
                event={"kind": "call_outcome_unknown", "invocation_id": item.invocation_id},
            )
        if outcome.fact is not None and len(outcome.fact.model_dump_json().encode()) > (
            self.settings.max_artifact_bytes
        ):
            outcome = CallOutcome(error_code="result_too_large")
            state.status, state.error_code = "failed", "result_too_large"
        if (
            outcome.fact is not None
            and sum(len(fact.model_dump_json().encode()) for fact in [*state.facts, outcome.fact])
            > self.settings.max_active_artifact_bytes
        ):
            outcome = CallOutcome(error_code="run_artifacts_too_large")
            state.status, state.error_code = "failed", "run_artifacts_too_large"
        runtime.observe(state, item, outcome)
        if outcome.fact is not None and capability.operation is not None:
            self._operation(state, item, outcome.fact, capability.operation)
        return await self._save(
            run,
            state,
            event={
                "kind": "call_finished",
                "invocation_id": item.invocation_id,
                "fact_id": str(outcome.fact.fact_id) if outcome.fact else None,
                "error_code": outcome.error_code,
            },
        )

    def _operation(
        self,
        state: RuntimeState,
        item: Invocation,
        fact: Fact,
        binding: OperationBinding,
    ) -> None:
        data = fact.value["data"]
        status, operation_id = _value(data, binding.status_path), _value(data, binding.id_path)
        if not isinstance(status, str) or type(operation_id) not in {str, int}:
            raise HostError("operation_contract_invalid")
        if item.operation is not None and str(operation_id) != item.operation.operation_id:
            raise HostError("operation_identity_changed")
        if status in binding.pending_states:
            if item.operation is None:
                item.operation = OperationReceipt(
                    operation_id=str(operation_id),
                    binding=binding,
                    poll_arguments=_argument(binding.poll_argument, operation_id),
                    next_poll_at=self.clock() + binding.interval_seconds,
                    deadline=self.clock() + binding.timeout_seconds,
                )
            else:
                item.operation.next_poll_at = self.clock() + binding.interval_seconds
            item.status = "waiting"
        elif status in binding.success_states:
            item.status = "succeeded"
        elif status in binding.failure_states:
            item.status, item.error_code = "failed", "operation_failed"
            AgentRuntime.reject(state, item.call.call_ref, item.call.capability, "operation_failed")
        else:
            raise HostError("operation_state_unknown")

    async def _poll(
        self,
        run: StoredRun,
        state: RuntimeState,
        item: Invocation,
        provider: CapabilityProvider,
        runtime: AgentRuntime,
    ) -> StoredRun:
        receipt = item.operation
        assert receipt is not None
        receipt_fact = next((fact for fact in state.facts if fact.fact_id == item.fact_id), None)
        if receipt_fact is None or not reference_available(receipt_fact, runtime.connection_id):
            state.status, state.error_code = (
                "needs_reconciliation",
                "operation_reference_unavailable",
            )
            return await self._save(
                run,
                state,
                event={"kind": "reconciliation_required", "invocation_id": item.invocation_id},
            )
        if self.clock() >= receipt.deadline:
            item.status, item.error_code = "failed", "operation_deadline_exceeded"
            runtime.reject(state, item.call.call_ref, item.call.capability, item.error_code)
            return await self._save(run, state, event={"kind": "operation_timed_out"})
        if self.clock() < receipt.next_poll_at and not item.poll_in_flight:
            return run
        if state.poll_calls_used >= self.settings.max_poll_calls:
            state.status, state.error_code = "failed", "poll_budget_exhausted"
            return await self._save(run, state)
        capability = provider.capabilities.get(receipt.binding.poll_capability)
        if capability is None or capability.effect != "read":
            raise HostError("operation_poll_must_be_read")
        policy = await self._policy(run.owner_id, run.pack_id, model_data=True)
        self._poll_access(provider, policy, receipt.binding)
        if not item.poll_in_flight:
            receipt.polls += 1
        item.poll_in_flight = True
        state.poll_calls_used += 1
        poll = ToolCall(
            call_ref=f"poll-{item.invocation_id}-{receipt.polls}",
            capability=capability.name,
            arguments=receipt.poll_arguments,
            reason="Read persisted operation status",
        )
        context = self._context(run, item, runtime.connection_id).model_copy(
            update={
                "invocation_id": f"{item.invocation_id}:poll:{receipt.polls}",
                "idempotency_key": f"{item.invocation_id}:poll:{receipt.polls}",
            }
        )
        run = await self._save(
            run,
            state,
            event={
                "kind": "operation_poll_started",
                "invocation_id": item.invocation_id,
                "poll": receipt.polls,
            },
        )
        try:
            async with asyncio.timeout(self.settings.invocation_timeout_seconds):
                outcome = await ToolBroker(
                    provider, granted_capabilities=policy.granted_capabilities
                ).execute(poll, context=context)
        except Exception:
            outcome = CallOutcome(error_code="provider_outcome_unknown")
        item.poll_in_flight = False
        if outcome.error_code in _AUTH_CODES:
            state.status, state.error_code = "needs_authorization", outcome.error_code
        elif outcome.fact is None:
            receipt.next_poll_at = self.clock() + receipt.binding.interval_seconds
            item.error_code = outcome.error_code
        else:
            if len(outcome.fact.model_dump_json().encode()) > self.settings.max_artifact_bytes:
                raise HostError("result_too_large")
            if sum(
                len(f.model_dump_json().encode()) for f in state.facts if f.fact_id != item.fact_id
            ) + len(outcome.fact.model_dump_json().encode()) > (
                self.settings.max_active_artifact_bytes
            ):
                raise HostError("run_artifacts_too_large")
            # Keep the most recent job status active; previous receipts remain immutable artifacts.
            state.facts[:] = [fact for fact in state.facts if fact.fact_id != item.fact_id]
            state.facts.append(outcome.fact)
            item.fact_id = outcome.fact.fact_id
            item.error_code = None
            self._operation(state, item, outcome.fact, receipt.binding)
            observation = Observation(
                call_ref=poll.call_ref,
                capability=poll.capability,
                status="succeeded",
                fact_id=outcome.fact.fact_id,
                arguments=poll.arguments,
            )
            # Full poll history is in immutable artifacts/events. Only the latest status
            # of this operation belongs in the model context and active state.
            prefix = f"poll-{item.invocation_id}-"
            for observations in (state.observations, state.model_observations):
                observations[:] = [o for o in observations if not o.call_ref.startswith(prefix)]
                observations.append(observation)
        return await self._save(
            run,
            state,
            event={
                "kind": "operation_polled",
                "invocation_id": item.invocation_id,
                "poll": receipt.polls,
                "fact_id": str(item.fact_id) if item.fact_id else None,
                "error_code": outcome.error_code,
            },
        )

    async def _work(self, run: StoredRun) -> StoredRun:
        state = await self._restore(run)
        if run.cancel_requested:
            state.status, state.error_code = "cancelled", "cancel_requested"
            for item in state.pending:
                if item.status == "in_flight":
                    item.status, item.error_code = "unknown", "provider_outcome_unknown"
            return await self._save(run, state, event={"kind": "run_cancelled"})
        if state.status in _TERMINAL or state.status == "needs_input":
            return run
        if state.status == "needs_reconciliation":
            return run
        if state.status == "waiting" and run.next_wake_at and self.clock() < run.next_wake_at:
            return run
        try:
            policy = await self._policy(run.owner_id, run.pack_id, model_data=True)
            async with self.provider_factory(run.owner_id, run.pack_id) as provider:
                fingerprint = _fingerprint(provider)
                previous = run.state.get("pack_fingerprint")
                if previous is not None and fingerprint != previous:
                    raise HostError("pack_changed")
                runtime = AgentRuntime(
                    pack=provider,
                    model=self.model,
                    granted_capabilities=policy.granted_capabilities,
                    max_model_rounds=self.settings.max_model_rounds,
                    max_tool_calls=self.settings.max_tool_calls,
                    max_context_characters=self.settings.max_context_characters,
                    connection_id=str(uuid4()),
                    durable=True,
                )
                state.status, state.error_code = "running", None
                run = await self._save(
                    run, state, fingerprint=fingerprint, event={"kind": "run_resumed"}
                )
                while state.status == "running":
                    latest = await self.store.get_run(run.run_id, owner_id=run.owner_id)
                    if latest.cancel_requested:
                        state.status, state.error_code = "cancelled", "cancel_requested"
                        break
                    if self.clock() - run.created_at >= self.settings.max_run_seconds:
                        state.status = (
                            "needs_reconciliation"
                            if any(
                                item.status in {"in_flight", "unknown"} for item in state.pending
                            )
                            else "failed"
                        )
                        state.error_code = "run_deadline_exceeded"
                        for item in state.pending:
                            if item.status == "in_flight":
                                item.status = "unknown"
                        break
                    if state.pending:
                        for item in tuple(state.pending):
                            if item.status in {"succeeded", "failed"}:
                                continue
                            if item.status == "waiting" and item.operation is not None:
                                run = await self._poll(run, state, item, provider, runtime)
                            else:
                                run = await self._execute(run, state, item, provider, runtime)
                            if state.status != "running":
                                break
                        if state.status != "running":
                            return run
                        pending = [item for item in state.pending if item.status == "waiting"]
                        if pending:
                            state.status = "waiting"
                            wake = min(
                                item.operation.next_poll_at
                                for item in pending
                                if item.operation is not None
                            )
                            return await self._save(
                                run, state, next_wake_at=wake, event={"kind": "run_waiting"}
                            )
                        state.pending.clear()
                        run = await self._save(run, state)
                        continue
                    # Recheck credentials and policy before sending persisted data to the model.
                    policy = await self._policy(run.owner_id, run.pack_id, model_data=True)
                    runtime.grants = policy.granted_capabilities

                    async def before_model() -> None:
                        nonlocal run
                        run = await self._save(
                            run,
                            state,
                            event={"kind": "model_requested", "round": state.rounds_used},
                        )

                    try:
                        async with asyncio.timeout(self.settings.model_timeout_seconds):
                            await runtime.step(state, before_model=before_model)
                    except TimeoutError:
                        state.status, state.error_code = "failed", "model_timeout"
                    run = await self._save(
                        run, state, event={"kind": "model_decided", "round": state.rounds_used}
                    )
                return await self._save(
                    run, state, event={"kind": "run_stopped", "status": state.status}
                )
        except HostError as exc:
            if exc.code == "run_state_too_large":
                # Stop from the last durable checkpoint, not an oversized unsaved packet.
                run = await self.store.get_run(run.run_id, owner_id=run.owner_id)
                state = await self._restore(run)
            state.status = "needs_authorization" if exc.code in _PAUSE_AUTH_CODES else "failed"
            if any(item.status in {"in_flight", "unknown"} for item in state.pending):
                if state.status != "needs_authorization":
                    state.status = "needs_reconciliation"
                for item in state.pending:
                    if item.status == "in_flight":
                        item.status = "unknown"
            state.error_code = exc.code
            await self._save(run, state, event={"kind": "run_blocked", "code": exc.code})
            if state.status == "needs_authorization":
                raise
            return await self.store.get_run(run.run_id, owner_id=run.owner_id)
        except (LeaseLost, asyncio.CancelledError):
            raise
        except Exception:
            # No exception payloads: adapters or external libraries may include credentials.
            state.status, state.error_code = "failed", "connection_or_execution_failed"
            if any(item.status in {"in_flight", "unknown"} for item in state.pending):
                state.status, state.error_code = "needs_reconciliation", "provider_outcome_unknown"
                for item in state.pending:
                    if item.status == "in_flight":
                        item.status, item.error_code = "unknown", "provider_outcome_unknown"
            return await self._save(
                run, state, event={"kind": "run_failed", "code": state.error_code}
            )

    async def drive(self, run_id: str, *, owner_id: str) -> StoredRun:
        async with self._slots:
            run = await self.store.claim(
                run_id, owner_id=owner_id, lease_seconds=self.settings.lease_seconds
            )
            assert run.lease_token is not None
            parent = asyncio.current_task()
            lease_error: list[BaseException] = []

            async def heartbeat() -> None:
                try:
                    while True:
                        await asyncio.sleep(self.settings.lease_seconds / 3)
                        await self.store.renew(
                            run_id,
                            owner_id=owner_id,
                            lease_token=run.lease_token or "",
                            lease_seconds=self.settings.lease_seconds,
                        )
                        current = await self.store.get_run(run_id, owner_id=owner_id)
                        if current.cancel_requested and parent is not None:
                            parent.cancel()
                            return
                except asyncio.CancelledError:
                    raise
                except Exception as exc:
                    lease_error.append(exc)
                    if parent is not None:
                        parent.cancel()

            task = asyncio.create_task(heartbeat())
            try:
                return await self._work(run)
            except asyncio.CancelledError:
                if lease_error:
                    raise LeaseLost(run_id) from lease_error[0]
                current = await self.store.get_run(run_id, owner_id=owner_id)
                if current.cancel_requested:
                    state = await self._restore(current)
                    state.status, state.error_code = "cancelled", "cancel_requested"
                    for item in state.pending:
                        if item.status == "in_flight":
                            item.status, item.error_code = "unknown", "provider_outcome_unknown"
                    return await self._save(current, state, event={"kind": "run_cancelled"})
                raise
            finally:
                task.cancel()
                with suppress(asyncio.CancelledError):
                    await task
                with suppress(LeaseLost):
                    await asyncio.shield(
                        self.store.release(run_id, owner_id=owner_id, lease_token=run.lease_token)
                    )

    async def wake_due(self, *, limit: int = 10) -> int:
        runs = await self.store.due_runs(limit=min(limit, self.settings.max_concurrent_runs))

        async def wake(run: StoredRun) -> None:
            try:
                await self.drive(run.run_id, owner_id=run.owner_id)
            except (StoreConflict, LeaseLost, HostError):
                return

        await asyncio.gather(*(wake(run) for run in runs))
        return len(runs)
