"""Run one task against a trusted pack and a JSON-capable model endpoint."""

import argparse
import asyncio
import json
import os
from pathlib import Path

from agent_capability.loader import open_pack
from agent_capability.model import HttpJsonDecisionModel
from agent_capability.runtime import AgentRuntime


async def _run(
    pack_path: Path,
    instruction: str | None,
    *,
    inspect: bool,
    grants: frozenset[str],
) -> int:
    async with open_pack(pack_path) as pack:
        if inspect:
            print(
                json.dumps(
                    {
                        "capabilities": [item.model_view() for item in pack.capabilities.values()],
                        "skills": [item.description.model_dump() for item in pack.skills.values()],
                    },
                    ensure_ascii=False,
                    indent=2,
                )
            )
            return 0
        if not instruction:
            raise ValueError("--instruction is required unless --inspect is used")
        model = HttpJsonDecisionModel(
            model=os.environ["AGENT_MODEL"],
            base_url=os.environ["AGENT_MODEL_BASE_URL"],
            api_key=os.environ["AGENT_MODEL_API_KEY"],
        )
        try:
            result = await AgentRuntime(
                pack=pack,
                model=model,
                granted_capabilities=grants,
            ).run(instruction)
            print(json.dumps(result.model_dump(mode="json"), ensure_ascii=False, indent=2))
            return 0 if result.status == "completed" else 2
        finally:
            await model.aclose()


def main() -> None:
    parser = argparse.ArgumentParser(description="Run one Agent task")
    parser.add_argument("--pack", type=Path, required=True)
    parser.add_argument("--instruction")
    parser.add_argument(
        "--inspect", action="store_true", help="Inspect catalog without calling an LLM"
    )
    parser.add_argument(
        "--grant-capability",
        action="append",
        default=[],
        help="Host authorization for an exact compute/write capability name",
    )
    args = parser.parse_args()
    raise SystemExit(
        asyncio.run(
            _run(
                args.pack,
                args.instruction,
                inspect=args.inspect,
                grants=frozenset(args.grant_capability),
            )
        )
    )


if __name__ == "__main__":
    main()
