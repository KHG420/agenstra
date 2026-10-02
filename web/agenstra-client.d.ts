export type JSONObject = { [key: string]: unknown };
export interface BrowserCommand { id: string; run_id: string; session_id: string; generation: number; action: string; arguments: JSONObject; status: string; context_revision: number; expires_at: number }
export interface RunSource { pack_id: string; capabilities: string[] }
export interface ProjectBinding extends RunSource { release: string; subject: string }
export interface Run { run_id: string; status: string; revision: number; state: JSONObject & { runtime: RuntimeState; project_sources?: ProjectBinding[] }; telemetry?: RunTelemetry }
export interface RunEvent { sequence: number; created_at: number; event: JSONObject & { kind: string; metrics?: ModelCallMetrics; context?: ContextTelemetry | null; budget?: RunBudget; progress?: RunProgress; attempt?: number; retry_at?: number } }
export interface RunProgressSnapshot { run: Run; telemetry: RunTelemetry | null; events: RunEvent[]; cursor: number }
export interface ChatConversation { id: string; integration_id: string; created_at: number }
export interface ChatInput { field?: string; prompt?: string; text: string }
export interface ChatMessage { id: string; conversation_id: string; client_id: string; text: string; sources?: RunSource[]; run_id: string; status: string; answer_markdown?: string; result_refs?: ResultObjectRef[]; input_history?: ChatInput[]; context_selection?: ConversationContextSelection; run?: Run }
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
  /** Accept profile-generated handler types; the server validates each action's input contract. */
  registerActions<T extends { [K in keyof T]: (args: never, context: { commandId: string; runId: string }) => JSONObject | Promise<JSONObject> }>(actions: T): this;
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
  setContextPolicy(id: string, policy: ContextPolicy, revision: number, options?: { requestId?: string }): Promise<Run>;
  getRuntimeInfo(): Promise<RuntimeInfo>;
  cancelRun(id: string): Promise<Run>;
  resumeRun(id: string): Promise<Run>;
  getArtifact(id: string, artifactId: string): Promise<JSONObject>;
  getRunTelemetry(id: string): Promise<RunTelemetry>;
  getRunDiagnostics(id: string): Promise<RunDiagnostics>;
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

export interface ContextPolicy { trigger_ratio: number; target_ratio: number }
export interface HostSettings {
 max_conversation_history_messages?: number; max_conversation_history_characters?: number; max_conversation_messages?: number;
 context_policy: ContextPolicy;
 model_context_window_tokens?: number; max_model_input_tokens?: number; model_output_reserve_tokens?: number; model_protocol_reserve_tokens?: number;
 lease_seconds: number; max_model_rounds: number; max_tool_calls: number; max_poll_calls: number; max_run_seconds: number;
 max_context_characters: number; max_context_capabilities?: number; max_artifact_bytes: number; max_active_artifact_bytes: number; max_state_bytes: number;
 model_timeout_seconds: number; invocation_timeout_seconds: number; max_invocation_attempts: number; retry_interval_seconds: number;
 approval_seconds: number; max_concurrent_runs: number; max_model_tokens?: number; max_model_output_tokens?: number;
 model_token_limit_field?: "" | "max_tokens" | "max_completion_tokens"; max_stagnant_rounds?: number; max_concurrent_tools?: number;
}
export interface EffectiveRunConfig { version: 1; source: "run_snapshot" | "current_host"; settings: HostSettings; model: ModelInfo }
export interface ModelInfo { protocol_reserve_tokens: number; name?: string; context_window_tokens: number | null; max_input_tokens: number | null; max_output_tokens: number | null }
export interface ContextTelemetry {
 policy: ContextPolicy; strategy: "projection"; policy_unit: "characters" | "tokens"; projection_reason: "none" | "soft_threshold" | "hard_limit"; target_met: boolean;
 input_tokens: number | null; reported_input_tokens: number | null; token_measurement_source?: "tokenizer" | "utf8_bytes_estimate"; model_context_window_tokens: number | null; effective_input_token_limit: number | null; reserved_output_tokens: number | null; tokens_remaining: number | null; token_utilization: number | null;
 schema: "agenstra.context-telemetry.v1"; projection_id: string; round: number; measured_at: number;
 input_characters: number; character_limit: number; characters_remaining: number; character_utilization: number; over_limit: boolean;
 candidate_characters: number; components: Record<string, number>;
 omissions: { observations: number; arguments: number; fact_paths: number; skills: number; memories: number; deferred_schemas: number };
}
export interface CounterBudget { used: number; limit: number; remaining: number }
export interface ModelUsage { requests: number; input_tokens: number; output_tokens: number; budget_tokens: number; estimated_requests: number; estimated_cost_usd: number; cost_available: boolean }
export interface ModelCallMetrics {
 round: number; purpose?: "decision" | "memory_extraction"; source_id?: string; reservation?: boolean;
 attempts: number; input_tokens: number; output_tokens: number; usage_available: boolean;
 estimated_input_tokens: number; estimated_output_tokens: number; estimated_cost_usd?: number; elapsed_ms: number;
 finish_reason?: string; request_id?: string; error_code?: string;
}
export interface RunBudget {
 model_rounds: CounterBudget; tool_calls: CounterBudget; poll_calls: CounterBudget;
 tokens: { limit: number | null; remaining: number | null; reported_tokens: number; estimated_tokens: number; reserved_tokens: number; unknown_tokens: number; charged_tokens: number };
 usage: ModelUsage; usage_by_purpose: Record<string, ModelUsage>; deadline: number; seconds_remaining: number;
}
export interface RunTelemetry { execution: ExecutionTelemetry; host_operations: { lease_seconds: number; max_concurrent_runs: number }; context_policy: ContextPolicy; schema: "agenstra.run-telemetry.v1"; run_id: string; run_revision: number; observed_at: number; context: ContextTelemetry | null; effective_config: EffectiveRunConfig; budget: RunBudget }
export interface ProgressItem { capability: string; call_ref?: string; status: string; fact_id?: string | null; error_code?: string | null }
export interface RunProgress { completed?: ProgressItem[]; pending?: ProgressItem[]; blocked?: ProgressItem[]; completed_count: number; blocked_count: number; omitted_items: number; no_progress_rounds: number; stagnation_warning?: boolean }
export interface Invocation { invocation_id: string; call: { call_ref: string; capability: string; arguments: JSONObject; reason: string }; status: string; arguments_sha256: string; attempts: number; error_code: string | null; fact_id: string | null; approval_expires_at: number | null; operation: JSONObject | null; receipt?: InvocationReceipt }
export interface InvocationReceipt { invocation_id: string; capability: string; effect: string; arguments_sha256: string; status: string; error_code?: string; fact_id?: string; result_sha256?: string; result_bytes?: number; result_error_code?: string; operation_id?: string; operation_status?: string; reconciled?: boolean }
export interface RuntimeState extends JSONObject { run_id: string; status: string; rounds_used: number; tool_calls_used: number; poll_calls_used: number; pending: Invocation[]; invocation_receipts?: InvocationReceipt[]; model_usage: ModelUsage; model_calls?: ModelCallMetrics[]; context_telemetry?: ContextTelemetry; input_field?: string | null; input_prompt?: string | null; input_schema?: RequestedInputSchema; result_refs?: ResultObjectRef[] }
export interface RequestedInputSchema { type: "string" | "enum" | "date"; enum?: string[]; min_length?: number; max_length?: number }
export interface ResultObjectRef { fact_id: string; path: (string | number)[]; id: string; label?: string; entity_type?: string }

export interface ExecutionTelemetry { stage: string; started_at: number | null; retry_at: number | null; wait_reason: string | null; next_wake_at: number | null; cancel_requested: boolean; active: { invocation_id: string; capability: string; status: string; attempts: number; approval_expires_at: number | null; operation_deadline: number | null }[] }
export interface RuntimeInfo { schema: "agenstra.runtime-info.v1"; settings: HostSettings; model: ModelInfo; features: Record<string, boolean>; granted_capabilities: string[] }
export interface ConversationContextSelection { history_limit: number; part_character_limit: number; included_messages: number; omitted_messages: number; truncated_parts: number; input_characters: number }

export interface RunDiagnostics { schema: "agenstra.run-diagnostics.v1"; run_id: string; status: string; revision: number; elapsed_ms: number; findings: { category: string; code: string; capability?: string; message: string; next_action: string }[]; progress: RunProgress | null; budget: RunBudget }
