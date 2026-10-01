# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Developers embed Agenstra in existing systems; their signed-in users ask the agent to query data, perform authorized operations and update the current page.

## Product Purpose

Agenstra is a Go capability-using agent framework. Optional web integration supplies reusable chat, conversation handling and browser command delivery. The host application maps its own business APIs and UI handlers.

## Operating Context

The existing headless runtime, API contracts, v1 run database, authorization and release pinning remain usable without web integration. Desktop browser integration is the initial acceptance scope.

## Capabilities and Constraints

The browser SDK and Web Component use native JavaScript without React/Vue runtime dependencies. Browser results are client-reported evidence. Unknown action outcomes require reconciliation. Runtime and extension data are separate SQLite databases on a single node.

## Brand Commitments

Retain the incumbent admin interface's quiet teal, ink, warm white surfaces, system typography and explicit keyboard focus. Embedded components allow host CSS-variable overrides.

## Product Principles

- Reuse task execution and approvals rather than create a second execution engine.
- Bind commands to an authenticated user, run and specific browser session.
- Publish stable action contracts; browser handlers do not grant permissions.
- Preserve uncertain outcomes and never report accepted commands as completed.
