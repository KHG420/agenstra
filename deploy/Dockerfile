FROM python:3.12-slim

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    AGENT_DEPLOYMENT_CONFIG=/etc/agent/deployment.json \
    PATH=/app/.venv/bin:$PATH

WORKDIR /app
COPY pyproject.toml uv.lock /app/
COPY src /app/src
RUN pip install --no-cache-dir 'uv==0.11.15' \
    && uv sync --locked --extra server --extra mcp --no-dev \
    && useradd --uid 10001 --create-home agent \
    && mkdir -p /data /etc/agent \
    && chown agent:agent /data

USER agent
VOLUME /data
EXPOSE 8091
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8091/readyz', timeout=3)"
CMD ["python", "-m", "agent_capability.serve", "--host", "0.0.0.0", "--port", "8091"]
