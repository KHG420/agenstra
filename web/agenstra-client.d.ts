export type JSONObject = { [key: string]: unknown };
export interface BrowserCommand { id: string; run_id: string; session_id: string; generation: number; action: string; arguments: JSONObject; status: string; context_revision: number; expires_at: number }
export interface RunSource { pack_id: string; capabilities: string[] }
export interface ProjectBinding extends RunSource { release: string; subject: string }
export interface Run { run_id: string; status: string; revision: number; state: JSONObject & { runtime: JSONObject; project_sources?: ProjectBinding[] } }
export interface RunEvent { sequence: number; created_at: number; event: JSONObject & { kind: string } }
export interface RunProgressSnapshot { run: Run; events: RunEvent[]; cursor: number }
export interface ChatConversation { id: string; integration_id: string; created_at: number }
export interface ChatInput { field?: string; prompt?: string; text: string }
export interface ChatMessage { id: string; conversation_id: string; client_id: string; text: string; sources?: RunSource[]; run_id: string; status: string; answer_markdown?: string; input_history?: ChatInput[]; run?: Run }
export interface ConversationSnapshot { conversation: ChatConversation; messages: ChatMessage[] }
export interface Memory {
  id: string; scope: "user" | "pack"; pack_id: string; key: string; value: string;
  kind: "preference" | "constraint" | "convention"; status: "active" | "candidate" | "forgotten";
  origin: "manual" | "explicit" | "inferred"; revision: number; evidence_count: number;
  source_id: string; quote: string; created_at: number; updated_at: number;
}
export interface MemoryUpdate {
  scope: "user" | "pack"; key: string; value: string; kind: Memory["kind"]; revision: number;
}
export interface MemoryHistory {
  revisions: Memory[];
  evidence: { source_id: string; quote: string; value: string; mode: string; created_at: number }[];
}
export interface ClientOptions {
  endpoint?: string; integration: string; browser?: boolean; handlerVersion?: string;
  getSession(): Promise<string | { token: string; expires_at?: number }>;
  /** Current page data for browser actions, never the agent's conversation context. */
  getPageObservation?(): JSONObject | Promise<JSONObject>;
  fetch?: typeof fetch; storage?: Storage | null; pollInterval?: number;
  onListenerError?(error: unknown): void;
}
export class AgenstraError extends Error { code: string; status: number; clientId?: string; requestId?: string }
/** Only for a failure whose lack of committed side effects the host can prove. */
export class AgenstraActionError extends Error { constructor(code: string, message?: string); code: string }
export class AgenstraClient {
  constructor(options: ClientOptions);
  options: ClientOptions;
  id(): string;
  registerActions(actions: Record<string, (args: JSONObject, context: { commandId: string; runId: string }) => JSONObject | Promise<JSONObject>>): this;
  on(name: string, callback: (value: any) => void): () => void;
  connectBrowser(): Promise<unknown>;
  /** Stop old tasks and verify actual business state before acknowledging unknown results. Does not replay actions or delete history. */
  recoverBrowser(options?: { acknowledgeUnknown?: boolean }): Promise<unknown>;
  updatePageObservation(observation: JSONObject): Promise<void>;
  /** Framework-owned memory management; authenticated owner and pack are resolved server-side. */
  listMemories(options?: { limit?: number; offset?: number }): Promise<Memory[]>;
  getMemory(id: string): Promise<Memory>;
  setMemory(update: MemoryUpdate): Promise<Memory>;
  deleteMemory(id: string, revision: number): Promise<Memory>;
  memoryHistory(id: string): Promise<MemoryHistory>;
  listConversations(): Promise<ChatConversation[]>;
  createConversation(): Promise<ChatConversation>;
  /** Select a framework-owned conversation. Context restoration is server-side. */
  selectConversation(id: string): Promise<ChatConversation>;
  getConversation(): Promise<ChatConversation>;
  snapshot(): Promise<ConversationSnapshot>;
  send(text: string, options?: { clientId?: string; sources?: RunSource[] }): Promise<ChatMessage>;
  watchConversation(callback: (snapshot: ConversationSnapshot) => void): () => void;
  getRun(id: string): Promise<Run>;
  getRunEvents(id: string, options?: { after?: number; limit?: number }): Promise<RunEvent[]>;
  /** Applied at a safe boundary; reuse requestId when retrying a lost response. */
  steerRun(id: string, text: string, revision: number, options?: { requestId?: string }): Promise<Run>;
  watchRun(id: string, callback: (snapshot: RunProgressSnapshot) => void, options?: { after?: number }): () => void;
  supplyInput(id: string, field: string, text: string, revision: number): Promise<Run>;
  approve(id: string, invocation: JSONObject, revision: number, approved: boolean): Promise<Run>;
  cancelMessage(id: string): Promise<ChatMessage>;
  reconcile(id: string, revision: number): Promise<Run>;
  /** Requires a server-side verifier for the original business invocation. */
  reconcileInvocation(id: string, invocation: JSONObject, revision: number): Promise<Run>;
  run(instruction: string, options?: { requestId?: string; sources?: RunSource[] }): Promise<Run>;
  destroy(options?: { closeSession?: boolean }): Promise<void>;
}
export function createAgenstraClient(options: ClientOptions): AgenstraClient;
