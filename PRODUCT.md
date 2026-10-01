# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Developers embed Agenstra in existing systems; their signed-in users ask the agent to query data, perform authorized operations and update the current page.

## Product Purpose

Agenstra is a Go capability-using agent framework. Optional web integration supplies headless chat and conversation APIs, framework-owned context restoration and browser command delivery. The host application selects conversations by ID and implements its own chat UI, business APIs and UI handlers.

## Operating Context

The existing headless runtime, API contracts, v1 run database, authorization and release pinning remain usable without web integration. Desktop browser integration is the initial acceptance scope.

## Capabilities and Constraints

The browser SDK uses native JavaScript without DOM rendering, CSS or React/Vue runtime dependencies. Agent context and run checkpoints remain on the server; browser storage contains only the selected conversation ID, tab binding and action receipts. Browser results are client-reported evidence. Unknown action outcomes require reconciliation. Runtime and extension data are separate SQLite databases on a single node.

## Brand Commitments

The integration imposes no visual design. Hosts own their interface and accessibility. The local demo owns its illustrative chat renderer and styles independently of core assets and package exports.

## Product Principles

- Keep context assembly and restoration in the framework; hosts choose conversation IDs.
- Reuse task execution and approvals rather than create a second execution engine.
- Bind commands to an authenticated user, run and specific browser session.
- Publish stable action contracts; browser handlers do not grant permissions.
- Preserve uncertain outcomes and never report accepted commands as completed.
