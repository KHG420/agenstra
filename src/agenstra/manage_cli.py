"""Manage capability releases and bindings through the running management API."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
from typing import Any
from urllib.parse import quote, urlsplit

import httpx


def _package(path: Path, version: str | None) -> dict[str, Any]:
    manifest = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(manifest, dict):
        raise ValueError("manifest must be a JSON object")
    root = path.parent.resolve()
    skills: dict[str, str] = {}
    for entry in manifest.get("skills", []):
        name = entry["path"]
        source = (root / name).resolve()
        if not source.is_relative_to(root) or not source.is_file():
            raise ValueError(f"skill path must stay inside the pack directory: {name}")
        skills[name] = source.read_text(encoding="utf-8")
    return {
        "pack_id": manifest["name"],
        "version": version or manifest.get("version"),
        "manifest": manifest,
        "skills": skills,
    }


def _server(value: str) -> str:
    parsed = urlsplit(value)
    if (
        parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError("management URL cannot contain credentials, a query, or a fragment")
    if parsed.scheme == "https" and parsed.hostname:
        return value.rstrip("/")
    if parsed.scheme == "http" and parsed.hostname in {"localhost", "127.0.0.1", "::1"}:
        return value.rstrip("/")
    raise ValueError("management URL must use HTTPS, or HTTP on localhost")


def main() -> None:
    parser = argparse.ArgumentParser(description="Manage Agenstra capability releases")
    parser.add_argument("--server", default=os.getenv("AGENSTRA_SERVER", "http://127.0.0.1:8091"))
    parser.add_argument("--admin-key-env", default="AGENSTRA_ADMIN_API_KEY")
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("list", help="list published and active releases")
    for name in ("validate", "publish"):
        command = commands.add_parser(name, help=f"{name} a local capability pack")
        command.add_argument("manifest", type=Path)
        command.add_argument("--version", help="required for legacy packs without a version field")
    activate = commands.add_parser("activate", help="activate a release for new runs")
    activate.add_argument("pack_id")
    activate.add_argument("digest")
    activate.add_argument("--revision", type=int)
    binding = commands.add_parser("bind", help="set a user's connection and grants")
    binding.add_argument("owner_id")
    binding.add_argument("pack_id")
    binding.add_argument("config", type=Path, help="JSON ConnectionConfig without secret values")
    disable = commands.add_parser("disable", help="revoke a user's managed connection")
    disable.add_argument("owner_id")
    disable.add_argument("pack_id")
    check = commands.add_parser("check", help="open the connection and inspect its catalog")
    check.add_argument("owner_id")
    check.add_argument("pack_id")
    commands.add_parser("audit", help="show recent management changes")
    args = parser.parse_args()
    key = os.environ.get(args.admin_key_env, "")
    if not key:
        parser.error(f"missing administrator key in {args.admin_key_env}")
    try:
        server = _server(args.server)
        method = "GET"
        path = "/admin/api/overview"
        body: dict[str, Any] | None = None
        if args.command in {"validate", "publish"}:
            body = _package(args.manifest, args.version)
            method = "POST"
            path = "/admin/api/validate" if args.command == "validate" else "/admin/api/releases"
        elif args.command == "activate":
            method = "POST"
            path = f"/admin/api/packs/{quote(args.pack_id, safe='')}/activate"
            body = {"digest": args.digest, "expected_revision": args.revision}
        elif args.command == "bind":
            method = "PUT"
            path = (
                f"/admin/api/bindings/{quote(args.owner_id, safe='')}"
                f"/{quote(args.pack_id, safe='')}"
            )
            body = json.loads(args.config.read_text(encoding="utf-8"))
        elif args.command == "disable":
            method = "DELETE"
            path = (
                f"/admin/api/bindings/{quote(args.owner_id, safe='')}"
                f"/{quote(args.pack_id, safe='')}"
            )
        elif args.command == "check":
            method = "POST"
            path = (
                f"/admin/api/bindings/{quote(args.owner_id, safe='')}"
                f"/{quote(args.pack_id, safe='')}/check"
            )
        elif args.command == "audit":
            path = "/admin/api/audit"
        with httpx.Client(timeout=30, follow_redirects=False) as client:
            response = client.request(
                method,
                server + path,
                headers={"Authorization": f"Bearer {key}"},
                json=body,
            )
        if response.status_code >= 400:
            raise ValueError(f"management API returned {response.status_code}: {response.text}")
        print(json.dumps(response.json(), ensure_ascii=False, indent=2))
    except (OSError, ValueError, KeyError, TypeError, httpx.HTTPError) as error:
        parser.exit(2, f"agenstra-manage: {error}\n")


if __name__ == "__main__":
    main()
