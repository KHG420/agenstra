"""Trusted deployment manifests turn existing REST endpoints into agent capabilities."""

import os
import re
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any, cast

import httpx
from pydantic import BaseModel, ConfigDict, Field, JsonValue, ValidationError, create_model

from agenstra.contracts import PackManifest, RestCapability, RestField, StrictModel
from agenstra.providers import (
    CapabilityDescription,
    CapabilityResult,
    InvocationContext,
    Skill,
    agent_prompt,
)


class DeclaredResponse(BaseModel):
    # A provider may add fields without exposing them to the model or later tools.
    model_config = ConfigDict(extra="ignore", frozen=True)


@dataclass(frozen=True, slots=True)
class RestBinding:
    definition: RestCapability
    url: str
    token: str | None
    input_model: type[BaseModel]
    output_model: type[BaseModel] | None

    def model_view(self) -> dict[str, JsonValue]:
        view: dict[str, JsonValue] = {
            "name": self.definition.name,
            "version": self.definition.version,
            "description": self.definition.description,
            "effect": "read",
            "input_schema": self.input_model.model_json_schema(mode="validation"),
        }
        if self.definition.outputs is not None:
            view["output_fields"] = {
                name: field.model_dump(mode="json")
                for name, field in self.definition.outputs.items()
            }
        return view


@dataclass(slots=True)
class LoadedPack:
    manifest: PackManifest
    bindings: dict[str, RestBinding]
    client: httpx.AsyncClient
    owns_client: bool

    @property
    def capabilities(self) -> Mapping[str, CapabilityDescription]:
        return {
            name: CapabilityDescription.model_validate(
                binding.model_view()
                | {
                    "output_schema": binding.output_model.model_json_schema()
                    if binding.output_model is not None
                    else None,
                }
            )
            for name, binding in self.bindings.items()
        }

    @property
    def skills(self) -> Mapping[str, Skill]:
        return {}

    def system_prompt(self) -> str:
        return agent_prompt(self.manifest.guidance)

    def model_capabilities(self) -> tuple[dict[str, JsonValue], ...]:
        return tuple(item.model_view() for item in self.capabilities.values())

    async def invoke(
        self,
        name: str,
        arguments: dict[str, JsonValue],
        *,
        context: InvocationContext | None = None,
    ) -> CapabilityResult:
        binding = self.bindings[name]
        try:
            validated = binding.input_model.model_validate(arguments, strict=True)
        except ValidationError:
            return CapabilityResult(error_code="capability_input_invalid")
        response = await self._request(
            binding, validated.model_dump(mode="json", exclude_none=True)
        )
        if isinstance(response, str):
            return CapabilityResult(error_code=response)
        try:
            data = response.json()
            for step in binding.definition.response_path:
                if isinstance(data, dict) and isinstance(step, str) and step in data:
                    data = data[step]
                    continue
                if isinstance(data, list) and type(step) is int and 0 <= step < len(data):
                    data = data[step]
                    continue
                return CapabilityResult(error_code="upstream_response_invalid")
            if not isinstance(data, dict):
                return CapabilityResult(error_code="upstream_response_invalid")
            if binding.output_model is not None:
                data = binding.output_model.model_validate(data, strict=True).model_dump(
                    mode="json", exclude_none=True
                )
            return CapabilityResult(data=cast(dict[str, JsonValue], data))
        except (ValidationError, ValueError):
            return CapabilityResult(error_code="upstream_response_invalid")

    async def _request(self, binding: RestBinding, payload: dict[str, Any]) -> httpx.Response | str:
        headers = (
            {"Authorization": f"Bearer {binding.token}"} if binding.token is not None else None
        )
        for attempt in range(binding.definition.max_attempts):
            try:
                response = await self.client.request(
                    binding.definition.method,
                    binding.url,
                    params=payload if binding.definition.method == "GET" else None,
                    json=payload if binding.definition.method == "POST" else None,
                    headers=headers,
                    timeout=binding.definition.timeout_seconds,
                    follow_redirects=False,
                )
            except httpx.RequestError:
                if attempt + 1 < binding.definition.max_attempts:
                    continue
                return "upstream_unavailable"
            if response.status_code >= 400:
                if response.status_code in {429, 502, 503, 504} and (
                    attempt + 1 < binding.definition.max_attempts
                ):
                    continue
                return f"upstream_http_{response.status_code}"
            return response
        return "upstream_unavailable"

    async def aclose(self) -> None:
        if self.owns_client:
            await self.client.aclose()


_FIELD_TYPES: dict[str, Any] = {
    "string": str,
    "integer": int,
    "number": float,
    "boolean": bool,
    "object": dict[str, JsonValue],
    "array": list[JsonValue],
}
_FIELD_NAME = re.compile(r"[a-z][a-z0-9_]{0,63}\Z")


def _model_for_fields(
    capability: RestCapability,
    fields: Mapping[str, RestField],
    *,
    output: bool,
) -> type[BaseModel]:
    definitions: dict[str, Any] = {}
    for name, field in fields.items():
        if _FIELD_NAME.fullmatch(name) is None:
            side = "output" if output else "input"
            raise ValueError(f"invalid {side} name in {capability.name}: {name}")
        if not output and capability.method == "GET" and field.type in {"object", "array"}:
            raise ValueError(f"GET inputs must be scalar in {capability.name}: {name}")
        value_type = _FIELD_TYPES[field.type]
        annotation = value_type if field.required else value_type | None
        definitions[name] = (
            annotation,
            Field(... if field.required else None, description=field.description),
        )
    prefix = "Response" if output else "Request"
    model_name = prefix + "_" + re.sub(r"[^A-Za-z0-9]", "_", capability.name)
    return create_model(
        model_name,
        __base__=DeclaredResponse if output else StrictModel,
        **definitions,
    )


def load_pack(
    path: str | Path,
    *,
    environment: Mapping[str, str] | None = None,
    client: httpx.AsyncClient | None = None,
) -> LoadedPack:
    """Load a trusted local pack. Users cannot supply endpoint URLs through a run."""
    manifest = PackManifest.model_validate_json(Path(path).read_text(encoding="utf-8"))
    env = environment if environment is not None else os.environ
    bindings: dict[str, RestBinding] = {}
    for definition in manifest.capabilities:
        if definition.name in bindings:
            raise ValueError(f"duplicate capability name: {definition.name}")
        url = env.get(definition.url_env, "").strip()
        parsed = httpx.URL(url) if url else None
        if parsed is None or parsed.scheme not in {"http", "https"} or not parsed.host:
            raise ValueError(f"missing or invalid URL environment variable: {definition.url_env}")
        token = None
        if definition.token_env is not None:
            token = env.get(definition.token_env, "").strip()
            if not token:
                raise ValueError(f"missing token environment variable: {definition.token_env}")
        bindings[definition.name] = RestBinding(
            definition=definition,
            url=url,
            token=token,
            input_model=_model_for_fields(definition, definition.inputs, output=False),
            output_model=(
                _model_for_fields(definition, definition.outputs, output=True)
                if definition.outputs is not None
                else None
            ),
        )
    resolved_client = client or httpx.AsyncClient(follow_redirects=False)
    return LoadedPack(
        manifest=manifest,
        bindings=bindings,
        client=resolved_client,
        owns_client=client is None,
    )
