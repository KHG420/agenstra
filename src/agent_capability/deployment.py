"""Trusted deployment configuration, user authentication, and connection scoping."""

import hashlib
import json
import os
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager
from dataclasses import dataclass
from hmac import compare_digest
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

import httpx
from pydantic import BaseModel, ConfigDict, Field, JsonValue

from agent_capability.host import ExecutionPolicy, HostError, HostSettings
from agent_capability.loader import open_pack
from agent_capability.providers import (
    CapabilityDescription,
    CapabilityProvider,
    CapabilityResult,
    InvocationContext,
    Skill,
)


class DeploymentError(HostError):
    """A safe authorization or connection error code for the durable host."""


@dataclass(frozen=True)
class _BoundProvider:
    provider: CapabilityProvider
    binding_id: str

    @property
    def capabilities(self) -> Mapping[str, CapabilityDescription]:
        return self.provider.capabilities

    @property
    def skills(self) -> Mapping[str, Skill]:
        return self.provider.skills

    def system_prompt(self) -> str:
        return self.provider.system_prompt()

    async def invoke(
        self,
        name: str,
        arguments: dict[str, JsonValue],
        *,
        context: InvocationContext | None = None,
    ) -> CapabilityResult:
        return await self.provider.invoke(name, arguments, context=context)


def _endpoint_value(value: str) -> str:
    """Identify a URL without credential-bearing userinfo, query or fragment."""
    parsed = urlsplit(value)
    host = parsed.hostname or ""
    port = f":{parsed.port}" if parsed.port is not None else ""
    return f"{parsed.scheme}://{host}{port}{parsed.path}"


def _endpoint_envs(manifest: dict[str, Any]) -> set[str]:
    schema = manifest.get("schema")
    if schema == "agent-capability.rest-pack.v2":
        name = manifest.get("base_url_env")
        return {name} if isinstance(name, str) else set()
    if schema == "agent-capability.mcp-pack.v1":
        source = manifest.get("source")
        name = source.get("url_env") if isinstance(source, dict) else None
        return {name} if isinstance(name, str) else set()
    if schema == "agent-capability.capability-pack.v1":
        capabilities = manifest.get("capabilities")
        if isinstance(capabilities, list):
            return {
                name
                for capability in capabilities
                if isinstance(capability, dict)
                if isinstance(name := capability.get("url_env"), str)
            }
    return set()


class _ConfigModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class IdentityConfig(_ConfigModel):
    url_env: str
    token_env: str
    subject_path: tuple[str | int, ...] = ("sub",)
    expected_subject: str | None = None


class ConnectionConfig(_ConfigModel):
    environment: dict[str, str] = Field(default_factory=dict)
    # Non-secret connection identities, especially upstream endpoints behind a stdio adapter.
    binding_environment: frozenset[str] = frozenset()
    granted_capabilities: frozenset[str] = frozenset()
    approval_capabilities: frozenset[str] = frozenset()
    allow_model_data: bool = False
    identity: IdentityConfig | None = None


class UserConfig(_ConfigModel):
    api_key_env: str
    packs: dict[str, ConnectionConfig]


class PackConfig(_ConfigModel):
    path: str


class DeploymentConfig(_ConfigModel):
    database_path: str
    packs: dict[str, PackConfig]
    users: dict[str, UserConfig]
    settings: HostSettings = Field(default_factory=HostSettings)


class Deployment:
    def __init__(
        self,
        config: DeploymentConfig,
        *,
        base_dir: Path,
        environment: Mapping[str, str] | None = None,
        identity_client: httpx.AsyncClient | None = None,
    ) -> None:
        self.config = config
        self.base_dir = base_dir
        self.environment = environment if environment is not None else os.environ
        self.identity_client = identity_client

    @property
    def database_path(self) -> Path:
        return (self.base_dir / self.config.database_path).resolve()

    async def authenticate(self, token: str) -> str:
        if not token:
            raise DeploymentError("unauthorized")
        matched = [
            owner_id
            for owner_id, user in self.config.users.items()
            if (secret := self.environment.get(user.api_key_env))
            and compare_digest(token.encode("utf-8"), secret.encode("utf-8"))
        ]
        if len(matched) != 1:
            raise DeploymentError("unauthorized")
        return matched[0]

    def _connection(self, owner_id: str, pack_id: str) -> ConnectionConfig:
        user = self.config.users.get(owner_id)
        if user is None or pack_id not in self.config.packs or pack_id not in user.packs:
            raise DeploymentError("access_denied")
        return user.packs[pack_id]

    def _secret(self, env_name: str) -> str:
        value = self.environment.get(env_name)
        if not value:
            raise DeploymentError("connection_unavailable")
        return value

    async def _validate_identity(self, owner_id: str, connection: ConnectionConfig) -> None:
        identity = connection.identity
        if identity is None:
            return
        url = self._secret(identity.url_env)
        token = self._secret(identity.token_env)
        try:
            if self.identity_client is None:
                async with httpx.AsyncClient(follow_redirects=False) as client:
                    response = await client.get(
                        url,
                        headers={"Authorization": f"Bearer {token}"},
                        timeout=10,
                        follow_redirects=False,
                    )
            else:
                response = await self.identity_client.get(
                    url,
                    headers={"Authorization": f"Bearer {token}"},
                    timeout=10,
                    follow_redirects=False,
                )
            if response.status_code != 200:
                raise DeploymentError("identity_unverified")
            subject: object = response.json()
            for step in identity.subject_path:
                if isinstance(subject, dict) and isinstance(step, str):
                    subject = subject[step]
                    continue
                if (
                    isinstance(subject, list)
                    and isinstance(step, int)
                    and not isinstance(step, bool)
                ):
                    subject = subject[step]
                    continue
                raise DeploymentError("identity_unverified")
            if not isinstance(subject, str) or subject != (identity.expected_subject or owner_id):
                raise DeploymentError("identity_unverified")
        except (httpx.HTTPError, ValueError, KeyError, IndexError, TypeError) as error:
            raise DeploymentError("identity_unverified") from error

    async def policy_resolver(self, owner_id: str, pack_id: str) -> "ExecutionPolicy":
        connection = self._connection(owner_id, pack_id)
        await self._validate_identity(owner_id, connection)
        return ExecutionPolicy(
            granted_capabilities=connection.granted_capabilities,
            approval_capabilities=connection.approval_capabilities,
            allow_model_data=connection.allow_model_data,
        )

    def _binding_id(
        self,
        owner_id: str,
        pack_id: str,
        path: Path,
        connection: ConnectionConfig,
        environment: Mapping[str, str],
    ) -> str:
        manifest = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(manifest, dict):
            raise DeploymentError("connection_unavailable")
        if not connection.binding_environment.issubset(environment):
            raise DeploymentError("connection_unavailable")
        identity = connection.identity
        source = manifest.get("source", {})
        known_credentials = {manifest.get("token_env"), source.get("token_env")}
        known_credentials.update(manifest.get("headers_env", {}).values())
        if identity is not None:
            known_credentials.update(
                target
                for target, origin in connection.environment.items()
                if origin == identity.token_env
            )
        if connection.binding_environment & known_credentials:
            raise DeploymentError("binding_contains_credentials")
        binding = {
            "owner_id": owner_id,
            "pack_id": pack_id,
            "pack_path": str(path),
            "manifest": manifest,
            "environment_refs": connection.environment,
            "working_directory": environment.get(source.get("cwd_env", "")),
            "connection_identity": {
                name: environment[name] for name in sorted(connection.binding_environment)
            },
            "endpoints": {
                name: _endpoint_value(environment.get(name, ""))
                for name in sorted(_endpoint_envs(manifest))
            },
            "identity": (
                {
                    "url": _endpoint_value(self._secret(identity.url_env)),
                    "subject_path": identity.subject_path,
                    "expected_subject": identity.expected_subject or owner_id,
                }
                if identity is not None
                else None
            ),
        }
        canonical = json.dumps(binding, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        return hashlib.sha256(canonical.encode("utf-8")).hexdigest()

    @asynccontextmanager
    async def provider_factory(
        self, owner_id: str, pack_id: str
    ) -> AsyncIterator[CapabilityProvider]:
        connection = self._connection(owner_id, pack_id)
        await self._validate_identity(owner_id, connection)
        environment = {
            target_env: self._secret(secret_env)
            for target_env, secret_env in connection.environment.items()
        }
        path = (self.base_dir / self.config.packs[pack_id].path).resolve()
        binding_id = self._binding_id(owner_id, pack_id, path, connection, environment)
        async with open_pack(path, environment=environment) as provider:
            yield _BoundProvider(provider=provider, binding_id=binding_id)


def load_deployment(path: str | Path) -> Deployment:
    config_path = Path(path).resolve()
    parsed = json.loads(config_path.read_text(encoding="utf-8"))
    config = DeploymentConfig.model_validate(parsed)
    return Deployment(config, base_dir=config_path.parent)
