"""Reviewed REST capability bindings. The manifest is trusted deployment configuration."""

from __future__ import annotations

import os
import re
from collections.abc import Mapping
from pathlib import Path
from typing import TYPE_CHECKING, Any, Literal, cast
from urllib.parse import quote, unquote

import httpx
from jsonschema import Draft202012Validator, FormatChecker
from jsonschema.exceptions import ValidationError as SchemaValidationError
from pydantic import Field, JsonValue, model_validator

from agenstra.contracts import StrictModel
from agenstra.providers import (
    CapabilityDescription,
    CapabilityResult,
    OperationBinding,
    Skill,
    agent_prompt,
)
from agenstra.skills import SkillFile, load_skill_files

if TYPE_CHECKING:
    from agenstra.providers import InvocationContext

_ENV = r"^[A-Z][A-Z0-9_]*$"
_NAME = r"^[A-Za-z][A-Za-z0-9_.-]{1,127}$"
_CODE = re.compile(r"[A-Za-z0-9_.:-]{1,120}\Z")
_SLOT = re.compile(r"\{([a-zA-Z][a-zA-Z0-9_]*)\}")
_HEADER = re.compile(r"[A-Za-z0-9!#$%&'*+.^_`|~-]+\Z")
_FORBIDDEN_HEADERS = {
    "authorization",
    "proxy-authorization",
    "cookie",
    "set-cookie",
    "host",
    "content-length",
    "content-type",
    "transfer-encoding",
    "connection",
    "upgrade",
    "te",
    "trailer",
    "idempotency-key",
}


class RestEndpoint(StrictModel):
    name: str = Field(pattern=_NAME)
    description: str = Field(min_length=1, max_length=500)
    method: Literal["GET", "POST", "PUT", "PATCH", "DELETE"]
    path: str
    input_schema: dict[str, JsonValue]
    output_schema: dict[str, JsonValue]
    effect: Literal["read", "compute", "write", "destructive"]
    response_path: tuple[str | int, ...] = Field(default=(), max_length=16)
    response_schemas: dict[str, dict[str, JsonValue]] = Field(default_factory=dict)
    error_codes: dict[str, str] = Field(default_factory=dict)
    timeout_seconds: float = Field(default=20, gt=0, le=300)
    idempotency_header: str | None = None
    idempotency_argument: tuple[str, ...] | None = None
    operation: OperationBinding | None = None
    skills: tuple[str, ...] = ()
    approval_required: bool = False

    @model_validator(mode="after")
    def check_binding(self) -> RestEndpoint:
        if not self.path.startswith("/") or "?" in self.path or "#" in self.path:
            raise ValueError(
                "REST path must be an absolute path template without query or fragment"
            )
        if "//" in self.path or "\\" in self.path or "%" in self.path:
            raise ValueError("REST path contains unsafe syntax")
        segments = self.path.split("/")[1:]
        if any(segment in {"", ".", ".."} for segment in segments):
            raise ValueError("REST path contains empty or traversal segment")
        if "{" in _SLOT.sub("", self.path) or "}" in _SLOT.sub("", self.path):
            raise ValueError("REST path has an invalid template slot")
        if self.idempotency_header is not None:
            _checked_header(self.idempotency_header, allow_idempotency=True)
        if self.idempotency_argument is not None and not self.idempotency_argument:
            raise ValueError("idempotency_argument must identify an input field")
        for status, code in self.error_codes.items():
            if not re.fullmatch(r"[1-5][0-9]{2}", status) or not _CODE.fullmatch(code):
                raise ValueError("error_codes must map HTTP status to safe error codes")
        for status in self.response_schemas:
            if not re.fullmatch(r"2[0-9]{2}", status):
                raise ValueError("response_schemas must use successful HTTP statuses")
        return self


class RestManifest(StrictModel):
    schema_: Literal["agenstra.rest-pack.v2"] = Field(alias="schema")
    name: str = Field(min_length=1, max_length=80)
    version: str = Field(min_length=1, max_length=40)
    guidance: str = Field(min_length=1, max_length=8000)
    base_url_env: str = Field(pattern=_ENV)
    token_env: str | None = Field(default=None, pattern=_ENV)
    headers_env: dict[str, str] = Field(default_factory=dict)
    capabilities: tuple[RestEndpoint, ...] = Field(min_length=1)
    skills: tuple[SkillFile, ...] = ()

    @model_validator(mode="after")
    def check_headers(self) -> RestManifest:
        for header, env_name in self.headers_env.items():
            _checked_header(header)
            if re.fullmatch(_ENV, env_name) is None:
                raise ValueError("headers_env values must be environment variable names")
        if self.token_env and any(name.lower() == "authorization" for name in self.headers_env):
            raise ValueError("Authorization must use token_env only")
        return self


def _checked_header(name: str, *, allow_idempotency: bool = False) -> None:
    if not _HEADER.fullmatch(name) or name.lower() in _FORBIDDEN_HEADERS - (
        {"idempotency-key"} if allow_idempotency else set()
    ):
        raise ValueError(f"unsafe REST header name: {name}")


def _local_validator(schema: dict[str, JsonValue]) -> Draft202012Validator:
    def inspect(value: object) -> None:
        if isinstance(value, dict):
            for key, item in value.items():
                if key == "$ref" and (not isinstance(item, str) or not item.startswith("#/")):
                    raise ValueError("REST schemas require local references")
                if key == "$schema" and item != "https://json-schema.org/draft/2020-12/schema":
                    raise ValueError("REST schemas require the JSON Schema 2020-12 dialect")
                if key in {"$id", "$anchor", "$dynamicAnchor", "$dynamicRef"}:
                    raise ValueError(f"REST schemas do not support {key}")
                inspect(item)
        elif isinstance(value, list):
            for item in value:
                inspect(item)

    inspect(schema)
    Draft202012Validator.check_schema(schema)
    validator = Draft202012Validator(schema, format_checker=FormatChecker())

    # Resolve every reference while loading, before an HTTP request can begin.
    def resolve(ref: str) -> None:
        target: object = schema
        for encoded in ref[2:].split("/"):
            key = unquote(encoded).replace("~1", "/").replace("~0", "~")
            if not isinstance(target, dict) or key not in target:
                raise ValueError(f"unresolved REST schema reference: {ref}")
            target = target[key]

    def visit(value: object) -> None:
        if isinstance(value, dict):
            ref = value.get("$ref")
            if isinstance(ref, str):
                resolve(ref)
            for item in value.values():
                visit(item)
        elif isinstance(value, list):
            for item in value:
                visit(item)

    visit(schema)
    return validator


def _required(env: Mapping[str, str], name: str) -> str:
    value = env.get(name, "").strip()
    if not value or any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise ValueError(f"missing or invalid environment variable: {name}")
    return value


def _path_value(value: object) -> str:
    if type(value) not in {str, int, float, bool}:
        raise ValueError("path parameter must be scalar")
    raw = str(value).lower() if type(value) is bool else str(value)
    if not raw or raw in {".", ".."} or any(c in raw for c in "/\\%?#"):
        raise ValueError("unsafe path parameter")
    if any(ord(c) < 32 or ord(c) == 127 for c in raw):
        raise ValueError("unsafe path parameter")
    return quote(raw, safe="")


def _query_value(value: object) -> str:
    if type(value) not in {str, int, float, bool}:
        raise ValueError("query parameter must be scalar")
    return str(value).lower() if type(value) is bool else str(value)


def _header_value(value: object) -> str:
    result = _query_value(value)
    if any(ord(char) < 32 or ord(char) == 127 for char in result):
        raise ValueError("unsafe header value")
    return result


class RestPack:
    def __init__(
        self,
        manifest: RestManifest,
        *,
        base_url: str,
        headers: dict[str, str],
        skills: Mapping[str, Skill],
        client: httpx.AsyncClient,
        owns_client: bool,
    ) -> None:
        self.manifest = manifest
        self.base_url = base_url
        self.headers = headers
        self._skills = skills
        self.client = client
        self.owns_client = owns_client
        self._endpoints: dict[str, RestEndpoint] = {}
        self._inputs: dict[str, Draft202012Validator] = {}
        self._outputs: dict[str, Draft202012Validator] = {}
        self._statuses: dict[str, dict[str, Draft202012Validator]] = {}
        self._capabilities: dict[str, CapabilityDescription] = {}
        for endpoint in manifest.capabilities:
            if endpoint.name in self._endpoints:
                raise ValueError(f"duplicate REST capability: {endpoint.name}")
            if not set(endpoint.skills).issubset(skills):
                raise ValueError(f"unknown skill for REST capability: {endpoint.name}")
            schema = cast(dict[str, Any], endpoint.input_schema)
            if schema.get("type") != "object" or schema.get("additionalProperties") is not False:
                raise ValueError(f"{endpoint.name}: input_schema must be a closed object")
            properties = schema.get("properties")
            if not isinstance(properties, dict) or set(properties) - {
                "path",
                "query",
                "body",
                "headers",
            }:
                raise ValueError(f"{endpoint.name}: input sections must be path/query/body/headers")
            slots = set(_SLOT.findall(endpoint.path))
            path_schema = properties.get("path", {})
            path_properties = (
                path_schema.get("properties", {}) if isinstance(path_schema, dict) else {}
            )
            path_required = path_schema.get("required", []) if isinstance(path_schema, dict) else []
            if slots != set(path_properties) or slots != set(path_required):
                raise ValueError(
                    f"{endpoint.name}: path schema must require exactly its template slots"
                )
            if slots and "path" not in schema.get("required", []):
                raise ValueError(f"{endpoint.name}: path section must be required")
            for section in ("path", "query", "headers"):
                part = properties.get(section)
                if part is not None and (
                    not isinstance(part, dict)
                    or part.get("type") != "object"
                    or part.get("additionalProperties") is not False
                ):
                    raise ValueError(f"{endpoint.name}: {section} must be a closed object")
            header_schema = properties.get("headers", {})
            header_names = (
                header_schema.get("properties", {}) if isinstance(header_schema, dict) else {}
            )
            for header in header_names:
                _checked_header(header)
                if any(header.lower() == static.lower() for static in headers):
                    raise ValueError(
                        f"{endpoint.name}: dynamic header conflicts with credential header"
                    )
                if endpoint.idempotency_header and (
                    header.lower() == endpoint.idempotency_header.lower()
                ):
                    raise ValueError(f"{endpoint.name}: idempotency header is host supplied")
            if endpoint.method == "GET" and "body" in properties:
                raise ValueError(f"{endpoint.name}: GET cannot declare a JSON body")
            self._inputs[endpoint.name] = _local_validator(schema)
            self._outputs[endpoint.name] = _local_validator(endpoint.output_schema)
            self._statuses[endpoint.name] = {
                status: _local_validator(status_schema)
                for status, status_schema in endpoint.response_schemas.items()
            }
            replay = (
                "safe"
                if endpoint.method == "GET" and endpoint.effect == "read"
                else (
                    "idempotent"
                    if endpoint.idempotency_header or endpoint.idempotency_argument
                    else "never"
                )
            )
            self._capabilities[endpoint.name] = CapabilityDescription(
                name=endpoint.name,
                version=manifest.version,
                description=endpoint.description,
                input_schema=schema,
                output_schema=endpoint.output_schema,
                effect=endpoint.effect,
                replay=replay,
                idempotency_argument=endpoint.idempotency_argument,
                operation=endpoint.operation,
                skills=endpoint.skills,
                approval_required=endpoint.approval_required,
            )
            self._endpoints[endpoint.name] = endpoint

    @property
    def capabilities(self) -> Mapping[str, CapabilityDescription]:
        return self._capabilities

    @property
    def skills(self) -> Mapping[str, Skill]:
        return self._skills

    def system_prompt(self) -> str:
        return agent_prompt(self.manifest.guidance)

    async def invoke(
        self,
        name: str,
        arguments: dict[str, JsonValue],
        *,
        context: InvocationContext | None = None,
    ) -> CapabilityResult:
        endpoint = self._endpoints.get(name)
        if endpoint is None:
            return CapabilityResult(error_code="capability_unknown")
        try:
            self._inputs[name].validate(arguments)
            path_args = arguments.get("path", {})
            query_args = arguments.get("query", {})
            body = arguments.get("body")
            dynamic_headers = arguments.get("headers", {})
            assert isinstance(path_args, dict) and isinstance(query_args, dict)
            assert isinstance(dynamic_headers, dict)
            path = _SLOT.sub(lambda match: _path_value(path_args[match.group(1)]), endpoint.path)
            params: list[tuple[str, str]] = []
            for key, value in query_args.items():
                if isinstance(value, list):
                    params.extend((key, _query_value(item)) for item in value)
                else:
                    params.append((key, _query_value(value)))
            headers = dict(self.headers)
            headers.update({key: _header_value(value) for key, value in dynamic_headers.items()})
            if endpoint.idempotency_header:
                if context is None or not context.idempotency_key:
                    return CapabilityResult(error_code="invocation_context_required")
                headers[endpoint.idempotency_header] = _header_value(context.idempotency_key)
        except (SchemaValidationError, ValueError, KeyError, AssertionError, TypeError):
            return CapabilityResult(error_code="capability_input_invalid")
        try:
            response = await self.client.request(
                endpoint.method,
                self.base_url + path,
                params=cast(Any, params),
                json=body if "body" in arguments else None,
                headers=headers,
                timeout=endpoint.timeout_seconds,
                follow_redirects=False,
            )
        except httpx.RequestError:
            return CapabilityResult(error_code="provider_outcome_unknown")
        status = str(response.status_code)
        if not 200 <= response.status_code < 300:
            if response.status_code >= 500 and endpoint.effect != "read":
                return CapabilityResult(error_code="provider_outcome_unknown")
            return CapabilityResult(
                error_code=endpoint.error_codes.get(status, f"upstream_http_{status}")
            )
        if endpoint.response_schemas and status not in endpoint.response_schemas:
            return CapabilityResult(error_code="upstream_response_invalid")
        try:
            data: Any = response.json()
            for step in endpoint.response_path:
                if (isinstance(data, dict) and isinstance(step, str)) or (
                    isinstance(data, list) and type(step) is int
                ):
                    data = data[cast(Any, step)]
                else:
                    raise ValueError("invalid response path")
            validator = self._statuses[name].get(status, self._outputs[name])
            self._outputs[name].validate(data)
            validator.validate(data)
            if not isinstance(data, dict):
                raise ValueError("response must be an object")
            return CapabilityResult(data=cast(dict[str, JsonValue], data))
        except (ValueError, KeyError, IndexError, SchemaValidationError):
            return CapabilityResult(error_code="upstream_response_invalid")

    async def aclose(self) -> None:
        if self.owns_client:
            await self.client.aclose()


def load_rest_pack(
    path: str | Path,
    *,
    environment: Mapping[str, str] | None = None,
    client: httpx.AsyncClient | None = None,
) -> RestPack:
    path = Path(path)
    manifest = RestManifest.model_validate_json(path.read_text(encoding="utf-8"))
    skills = load_skill_files(manifest.skills, path.parent)
    env = environment if environment is not None else os.environ
    base_url = _required(env, manifest.base_url_env)
    parsed = httpx.URL(base_url)
    if (
        parsed.scheme not in {"http", "https"}
        or not parsed.host
        or parsed.userinfo
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError("REST base URL must be an HTTP(S) origin or fixed base path")
    if any(segment in {".", ".."} for segment in parsed.path.split("/")):
        raise ValueError("REST base URL contains traversal path")
    headers = {name: _required(env, variable) for name, variable in manifest.headers_env.items()}
    if manifest.token_env:
        headers["Authorization"] = f"Bearer {_required(env, manifest.token_env)}"
    resolved_client = client if client is not None else httpx.AsyncClient(follow_redirects=False)
    return RestPack(
        manifest,
        base_url=base_url.rstrip("/"),
        headers=headers,
        skills=skills,
        client=resolved_client,
        owns_client=client is None,
    )
