# Headless web integration design

The framework provides chat and conversation APIs, a native JavaScript SDK and a browser control bridge. The headless client contains no renderer or CSS. A separate optional `@agenstra/web/chat` entry supplies the standard Web Component, while `@agenstra/web/session` supplies server-side ticket exchange. The host owns the client lifecycle and can use a custom interface.

The host selects a conversation ID. The framework loads owner-scoped history, freezes a bounded input when a queued message starts and restores unfinished runs from their persisted checkpoints. Context assembly, selection and restoration remain server responsibilities. The host cannot supply replacement context, history or context IDs through chat requests.

Page observations are separate tool data. The host reports the current page, filter and selected object through the browser bridge. Existing profile and persisted browser fields keep their context names for contract stability, but they do not hold agent conversation context. Page revisions still fence stale actions.

Conversation selection never migrates or cancels an existing task. SDK selection operations are serialized and late polling responses cannot replace the newly selected conversation. Browser commands retain their original authenticated user, run, tab and generation binding.

The demonstration under examples/web-integration uses the optional standard chat component for approval controls, input handling and diagnostics. Its business page remains example-owned. Demo data and the deterministic model are explicitly identified.

The standard chat component shows framework write receipts separately from the model answer and task status, including when answer generation fails or a task stops. Accepted submissions remain distinct from successful operations. Failed tasks with receipts direct users to those outcomes and discourage resubmitting successful actions. Receipt text uses the existing notice layout and plain DOM text.

The management onboarding preserves its existing palette and layout. OpenAPI selection and MCP discovery are primary actions; raw contracts remain advanced details. Publishing is followed by explicit user selection, capability grants and model-data consent. Connection checks and business task acceptance are displayed separately.
