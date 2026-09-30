"""Run the authenticated Agent HTTP host."""

import argparse
import os
from pathlib import Path

import uvicorn

from agenstra.deployment import load_deployment
from agenstra.host import AgentHost
from agenstra.model import HttpJsonDecisionModel
from agenstra.server import create_app
from agenstra.storage import SQLiteStore


def main() -> None:
    parser = argparse.ArgumentParser(description="Serve Agent runs")
    parser.add_argument("--config", type=Path, default=os.environ.get("AGENT_DEPLOYMENT_CONFIG"))
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8091)
    args = parser.parse_args()
    if args.config is None:
        parser.error("--config or AGENT_DEPLOYMENT_CONFIG is required")
    deployment = load_deployment(args.config)
    model = HttpJsonDecisionModel(
        model=os.environ["AGENT_MODEL"],
        base_url=os.environ["AGENT_MODEL_BASE_URL"],
        api_key=os.environ["AGENT_MODEL_API_KEY"],
        timeout_seconds=deployment.config.settings.model_timeout_seconds,
    )
    host = AgentHost(
        store=SQLiteStore(deployment.database_path),
        provider_factory=deployment.provider_factory,
        model=model,
        policy_resolver=deployment.policy_resolver,
        release_resolver=deployment.release_resolver if deployment.registry else None,
        release_provider_factory=(
            deployment.release_provider_factory if deployment.registry else None
        ),
        settings=deployment.config.settings,
    )
    app = create_app(host, deployment.authenticate, on_shutdown=model.aclose, deployment=deployment)
    uvicorn.run(app, host=args.host, port=args.port)


if __name__ == "__main__":
    main()
