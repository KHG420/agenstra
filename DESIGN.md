# Headless web integration design

The framework provides chat and conversation APIs, a native JavaScript SDK and a browser control bridge. Core assets and package exports contain no chat renderer, Web Component, DOM construction or CSS. The host application owns all UI and its lifecycle.

The host selects a conversation ID. The framework loads owner-scoped history, freezes a bounded input when a queued message starts and restores unfinished runs from their persisted checkpoints. Context assembly, selection and restoration remain server responsibilities. The host cannot supply replacement context, history or context IDs through chat requests.

Page observations are separate tool data. The host reports the current page, filter and selected object through the browser bridge. Existing profile and persisted browser fields keep their context names for contract stability, but they do not hold agent conversation context. Page revisions still fence stale actions.

Conversation selection never migrates or cancels an existing task. SDK selection operations are serialized and late polling responses cannot replace the newly selected conversation. Browser commands retain their original authenticated user, run, tab and generation binding.

The demonstration under examples/web-integration owns its chat UI, styling, approval controls and input handling. Its renderer is served only by the example and is not part of the framework API. Demo data and the deterministic model are explicitly identified.
