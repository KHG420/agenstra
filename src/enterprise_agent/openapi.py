"""Import explicitly selected JSON OpenAPI operations into a reviewed REST pack draft."""

from __future__ import annotations

import argparse
import json
import re
from copy import deepcopy
from pathlib import Path
from typing import Any
from urllib.parse import unquote

_METHODS = {"get", "post", "put", "patch", "delete"}
_SLOT = re.compile(r"\{([a-zA-Z][a-zA-Z0-9_]*)\}")


def _pointer(document: dict[str, Any], ref: str) -> Any:
    if not ref.startswith("#/"):
        raise ValueError(f"external reference is unsupported: {ref}")
    current: Any = document
    for part in ref[2:].split("/"):
        key = unquote(part).replace("~1", "/").replace("~0", "~")
        if not isinstance(current, dict) or key not in current:
            raise ValueError(f"unresolved OpenAPI reference: {ref}")
        current = current[key]
    return current


def _resolve(
    document: dict[str, Any], value: Any, seen: frozenset[str] = frozenset()
) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ValueError("OpenAPI object must be a JSON object")
    if "$ref" in value:
        if set(value) != {"$ref"}:
            raise ValueError("OpenAPI reference siblings are unsupported")
        ref = value["$ref"]
        if not isinstance(ref, str):
            raise ValueError("OpenAPI reference must be a string")
        if ref in seen:
            raise ValueError(f"cyclic OpenAPI reference: {ref}")
        return _resolve(document, _pointer(document, ref), seen | {ref})
    return value


def _schema(document: dict[str, Any], source: dict[str, Any]) -> dict[str, Any]:
    definitions: dict[str, Any] = {}
    active: set[str] = set()
    version = document["openapi"]

    def convert(value: Any) -> Any:
        if isinstance(value, list):
            return [convert(item) for item in value]
        if not isinstance(value, dict):
            return value
        ref = value.get("$ref")
        if ref is not None:
            if not isinstance(ref, str) or not ref.startswith("#/components/schemas/"):
                raise ValueError(f"unsupported schema reference: {ref}")
            name = ref.rsplit("/", 1)[-1].replace("~1", "/").replace("~0", "~")
            if not name or "/" in name or "~" in name:
                raise ValueError(f"unsupported schema name in reference: {ref}")
            if name not in definitions and name not in active:
                active.add(name)
                target = _pointer(document, ref)
                if not isinstance(target, dict):
                    raise ValueError(f"schema reference is not an object: {ref}")
                definitions[name] = convert(target)
                active.remove(name)
            siblings = {key: item for key, item in value.items() if key != "$ref"}
            if siblings and not (version.startswith("3.1.") or set(siblings) <= {"nullable"}):
                raise ValueError("OpenAPI 3.0 schema reference siblings are unsupported")
            result: dict[str, Any] = {"$ref": f"#/$defs/{name}"}
            if version.startswith("3.0.") and siblings == {"nullable": True}:
                result = {"anyOf": [result, {"type": "null"}]}
            elif siblings:
                extra = convert(siblings)
                if extra:
                    result = {"allOf": [result, extra]}
            return result
        result = {key: convert(item) for key, item in value.items() if key != "nullable"}
        if version.startswith("3.0.") and value.get("nullable") is True:
            result = {"anyOf": [result, {"type": "null"}]}
        return result

    converted = convert(deepcopy(source))
    if not isinstance(converted, dict):
        raise ValueError("schema must be a JSON object")
    if definitions:
        converted["$defs"] = definitions
    return converted


def _schema_parts(document: dict[str, Any], sources: dict[str, Any]) -> dict[str, Any]:
    """Convert all sections together so their references share a single local $defs."""
    converted = _schema(document, {"type": "object", "properties": sources})
    return converted


def _json_media(document: dict[str, Any], parent: dict[str, Any], label: str) -> dict[str, Any]:
    content = parent.get("content")
    if not isinstance(content, dict) or set(content) != {"application/json"}:
        raise ValueError(f"{label}: only application/json content is supported")
    media = _resolve(document, content["application/json"])
    schema = media.get("schema")
    if not isinstance(schema, dict):
        raise ValueError(f"{label}: JSON content requires a schema")
    return schema


def import_openapi(
    spec: str | Path,
    *,
    name: str,
    base_url_env: str,
    operations: list[str],
    effects: dict[str, str] | None = None,
    token_env: str | None = None,
) -> dict[str, Any]:
    """Generate an auditable draft; no URL, credential, or operation is inferred from input."""
    document = json.loads(Path(spec).read_text(encoding="utf-8"))
    if not isinstance(document, dict) or not str(document.get("openapi", "")).startswith(
        ("3.0.", "3.1.")
    ):
        raise ValueError("only OpenAPI 3.0/3.1 JSON documents are supported")
    if not operations or len(set(operations)) != len(operations):
        raise ValueError("select distinct operationIds with --operation")
    overrides = effects or {}
    if set(overrides) - set(operations):
        raise ValueError("--effect references an operationId that was not selected")
    if any(
        effect not in {"read", "compute", "write", "destructive"} for effect in overrides.values()
    ):
        raise ValueError("effect must be read, compute, write, or destructive")
    paths = document.get("paths")
    if not isinstance(paths, dict):
        raise ValueError("OpenAPI paths must be an object")
    found: dict[str, dict[str, Any]] = {}
    for path, path_item in paths.items():
        if not isinstance(path, str) or not isinstance(path_item, dict):
            raise ValueError("OpenAPI path item is invalid")
        for method, operation in path_item.items():
            if method not in _METHODS or not isinstance(operation, dict):
                continue
            op_id = operation.get("operationId")
            if op_id not in operations:
                continue
            if op_id in found:
                raise ValueError(f"duplicate operationId: {op_id}")
            security = operation.get("security", document.get("security", []))
            if security:
                if (
                    not token_env
                    or not isinstance(security, list)
                    or len(security) != 1
                    or not isinstance(security[0], dict)
                    or len(security[0]) != 1
                ):
                    raise ValueError(
                        f"{op_id}: security needs explicit --token-env for one bearer scheme"
                    )
                security_name, scopes = next(iter(security[0].items()))
                schemes = document.get("components", {}).get("securitySchemes", {})
                scheme = _resolve(document, schemes.get(security_name, {}))
                if (
                    scopes != []
                    or scheme.get("type") != "http"
                    or str(scheme.get("scheme", "")).lower() != "bearer"
                ):
                    raise ValueError(
                        f"{op_id}: unsupported security scheme; use a reviewed REST pack"
                    )
            parameter_map: dict[tuple[Any, Any], dict[str, Any]] = {}
            for group in (
                _resolve_parameters(document, path_item.get("parameters", [])),
                _resolve_parameters(document, operation.get("parameters", [])),
            ):
                within_group: set[tuple[Any, Any]] = set()
                for parameter in group:
                    identity = (parameter.get("in"), parameter.get("name"))
                    if identity in within_group:
                        raise ValueError(f"{op_id}: duplicate parameter {identity}")
                    within_group.add(identity)
                    parameter_map[identity] = parameter
            parameters = list(parameter_map.values())
            by_location: dict[str, dict[str, Any]] = {"path": {}, "query": {}}
            required: dict[str, list[str]] = {"path": [], "query": []}
            for parameter in parameters:
                location = parameter.get("in")
                key = parameter.get("name")
                if location not in by_location or not isinstance(key, str):
                    raise ValueError(f"{op_id}: only path and query parameters are supported")
                if key in by_location[location]:
                    raise ValueError(f"{op_id}: duplicate {location} parameter {key}")
                if "content" in parameter:
                    raise ValueError(f"{op_id}: parameter content serialization is unsupported")
                style = parameter.get("style", "simple" if location == "path" else "form")
                explode = parameter.get("explode", style == "form")
                schema = parameter.get("schema")
                if not isinstance(schema, dict):
                    raise ValueError(f"{op_id}: parameter {key} needs a schema")
                if location == "path" and (style != "simple" or explode):
                    raise ValueError(f"{op_id}: path parameters require simple serialization")
                if location == "query" and (style != "form" or explode is not True):
                    raise ValueError(f"{op_id}: query parameters require form/explode=true")
                if schema.get("type") in {"object", "array"} and location == "path":
                    raise ValueError(f"{op_id}: complex path parameters are unsupported")
                if schema.get("type") == "object" and location == "query":
                    raise ValueError(f"{op_id}: object query parameters are unsupported")
                if schema.get("type") == "array" and location == "query":
                    item_schema = schema.get("items")
                    if not isinstance(item_schema, dict) or item_schema.get("type") not in {
                        "string",
                        "integer",
                        "number",
                        "boolean",
                    }:
                        raise ValueError(f"{op_id}: query arrays require scalar items")
                by_location[location][key] = schema
                if parameter.get("required") is True or location == "path":
                    required[location].append(key)
            slots = set(_SLOT.findall(path))
            if slots != set(by_location["path"]):
                raise ValueError(f"{op_id}: path parameters do not match the path template")
            source_props: dict[str, Any] = {}
            root_required: list[str] = []
            for location in ("path", "query"):
                if by_location[location]:
                    source_props[location] = {
                        "type": "object",
                        "properties": by_location[location],
                        "required": required[location],
                        "additionalProperties": False,
                    }
                    if required[location]:
                        root_required.append(location)
            body_source = operation.get("requestBody")
            if body_source is not None:
                body_spec = _resolve(document, body_source)
                source_props["body"] = _json_media(document, body_spec, f"{op_id} request body")
                if body_spec.get("required") is True:
                    root_required.append("body")
            if method == "get" and body_source is not None:
                raise ValueError(f"{op_id}: GET request body is unsupported")
            input_schema = _schema_parts(document, source_props)
            input_schema["required"] = root_required
            input_schema["additionalProperties"] = False
            responses = operation.get("responses")
            if not isinstance(responses, dict):
                raise ValueError(f"{op_id}: responses must be an object")
            output_by_status: dict[str, Any] = {}
            for status, response_source in responses.items():
                if re.fullmatch(r"2[0-9]{2}", str(status)):
                    response_spec = _resolve(document, response_source)
                    schema = _json_media(document, response_spec, f"{op_id} response {status}")
                    output_by_status[str(status)] = _schema(document, schema)
            if not output_by_status:
                raise ValueError(f"{op_id}: a JSON 2xx response schema is required")
            first = next(iter(output_by_status.values()))
            if any(schema != first for schema in output_by_status.values()):
                raise ValueError(f"{op_id}: differing 2xx response schemas need manual mapping")
            found[op_id] = {
                "name": op_id,
                "description": operation.get("description") or operation.get("summary") or op_id,
                "method": method.upper(),
                "path": path,
                "input_schema": input_schema,
                "output_schema": first,
                "response_schemas": output_by_status,
                "effect": overrides.get(op_id, "read" if method == "get" else "write"),
            }
    missing = [op_id for op_id in operations if op_id not in found]
    if missing:
        raise ValueError(f"operationIds not found: {', '.join(missing)}")
    return {
        "schema": "enterprise.rest-pack.v2",
        "name": name,
        "version": "1.0.0",
        "guidance": f"Use the reviewed {name} REST capabilities for their declared purpose.",
        "base_url_env": base_url_env,
        **({"token_env": token_env} if token_env else {}),
        "capabilities": [found[op_id] for op_id in operations],
    }


def _resolve_parameters(document: dict[str, Any], source: Any) -> list[dict[str, Any]]:
    if not isinstance(source, list):
        raise ValueError("OpenAPI parameters must be an array")
    return [_resolve(document, item) for item in source]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--spec", required=True, type=Path, help="local OpenAPI 3.0/3.1 JSON file")
    parser.add_argument("--out", required=True, type=Path, help="output REST pack JSON file")
    parser.add_argument("--name", required=True)
    parser.add_argument("--base-url-env", required=True)
    parser.add_argument("--token-env", help="explicit credential variable for HTTP bearer auth")
    parser.add_argument("--operation", action="append", required=True, dest="operations")
    parser.add_argument("--effect", action="append", default=[], metavar="OPERATION_ID=EFFECT")
    args = parser.parse_args()
    overrides: dict[str, str] = {}
    for entry in args.effect:
        op_id, separator, effect = entry.partition("=")
        if not separator or not op_id or op_id in overrides:
            parser.error("--effect requires one unique OPERATION_ID=EFFECT")
        overrides[op_id] = effect
    try:
        draft = import_openapi(
            args.spec,
            name=args.name,
            base_url_env=args.base_url_env,
            operations=args.operations,
            effects=overrides,
            token_env=args.token_env,
        )
        # Validate the draft before writing it; credentials are bound only at load time.
        from enterprise_agent.rest import RestManifest

        RestManifest.model_validate(draft)
    except (ValueError, OSError, json.JSONDecodeError) as exc:
        parser.error(str(exc))
    args.out.write_text(json.dumps(draft, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
