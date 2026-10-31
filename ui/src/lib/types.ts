export type WSEventType =
	| 'session_created'
	| 'session_deleted'
	| 'session_active'
	| 'user_prompt'
	| 'agent_chunk'
	| 'agent_done'
	| 'tool_start'
	| 'tool_done'
	| 'subagent_start'
	| 'subagent_tool'
	| 'subagent_done'
	| 'memory_added'
	| 'patch_proposed'
	| 'patch_applied'
	| 'swarm_message'
	| 'swarm_lock'
	| 'approval_request'
	| 'token_usage'
	| 'profile_changed'
	| 'resume_failed';

export interface TokenUsagePayload {
	prompt_tokens: number;
	candidates_tokens: number;
	total_tokens: number;
	cached_tokens: number;
	cache_hit_ratio: number;
}

// ApprovalPrompt is a sandbox confirmation waiting for a human: the exact
// command or patch, plus why the policy asked.
export interface ApprovalPrompt {
	call_id: string;
	tool: string;
	hint: string;
	action: string;
	reason?: string;
}

export interface SwarmMessage {
	id: string;
	session_id?: string;
	sender: string;
	recipient?: string;
	channel?: string;
	subject?: string;
	body: string;
	priority?: 'normal' | 'urgent';
	timestamp: string;
}

export interface ResourceLock {
	resource: string;
	owner: string;
	session_id?: string;
	acquired_at: string;
	expires_at?: string;
}

export interface SwarmLockEvent {
	type: 'claimed' | 'released';
	lock: ResourceLock;
}

export interface WSEvent<T = unknown> {
	type: WSEventType;
	session_id?: string;
	seq?: number;
	timestamp: string;
	payload: T;
}

export interface GraphNode {
	id: string;
	kind: 'agent' | 'tool' | 'join';
	role?: string;
	model?: string;
	tool?: string;
}

export interface GraphSpec {
	name: string;
	description?: string;
	max_concurrency?: number;
	budgets?: {
		max_nodes?: number;
		max_wall_seconds?: number;
		max_tokens?: number;
	};
	nodes: GraphNode[];
	edges: string[][];
}

export interface GraphNodeResult {
	id: string;
	role?: string;
	kind: string;
	text?: string;
	tokens?: number;
	cached_tokens?: number;
	activations?: number;
	duration?: number;
	error?: string;
}

export interface GraphRunRecord {
	run_id: string;
	graph: string;
	spec?: GraphSpec;
	status: 'running' | 'waiting' | 'completed' | 'failed';
	nodes?: GraphNodeResult[];
	tokens?: number;
	cached_tokens?: number;
	cache_hit_ratio?: number;
	activations?: number;
	duration?: number;
	error?: string;
	updated_at?: string;
}

export interface GraphsResponse {
	specs: GraphSpec[];
	runs: GraphRunRecord[];
}

export interface SessionInfo {
	id: string;
	title: string;
	created_at: string;
	updated_at: string;
	is_active: boolean;
	turn_count: number;
}

export interface DiscoveredSession {
	id: string;
	format: 'agy' | 'cursor' | 'opencode';
	title: string;
	path: string;
	workspace?: string;
	created_at: string;
	updated_at: string;
	event_count: number;
	matches_workspace: boolean;
}

export interface ChatMessage {
	id: string;
	role: 'user' | 'assistant' | 'system';
	content: string;
	thinking?: string;
	timestamp: string;
	toolCalls?: ToolCall[];
}

export interface ToolCall {
	id: string;
	toolName: string;
	args: Record<string, unknown>;
	status: 'running' | 'done' | 'error';
	output?: string;
	durationMs?: number;
}

export interface SubagentNode {
	id: string;
	sessionId: string;
	name: string;
	role: string;
	model: string;
	status: 'running' | 'idle' | 'done' | 'error';
	parentAgentId?: string;
	currentAction?: string;
	tokensUsed: number;
	toolsRun: number;
	startedAt: string;
	completedAt?: string;
}

export interface MemoryItem {
	id: number;
	kind: 'user' | 'talk' | 'tool' | 'echo' | 'work';
	text: string;
	timestamp: string;
	size: number;
}

export interface PatchProposal {
	filePath: string;
	op: 'SWAP' | 'DEL' | 'INS_PRE' | 'INS_POST' | 'SWAP.BLK' | 'DEL.BLK' | 'INS.BLK.PRE' | 'INS.BLK.POST';
	anchor: string;
	content: string;
	originalLines?: string[];
	newLines?: string[];
	status: 'pending' | 'applied' | 'rejected';
}

export interface CompactorConfig {
	model?: string;
	provider?: string;
	base_url?: string;
	api_key?: string;
	api_key_env?: string;
	target_bytes?: number;
}

export interface ProfileConfig {
	provider?: string;
	model?: string;
	base_url?: string;
	api_key?: string;
	api_key_env?: string;
	memory_dir?: string;
	compactor?: CompactorConfig;
}

export interface ProfilesResponse {
	active_profile: string;
	profiles: Record<string, ProfileConfig>;
	user_config_path?: string;
	workspace_config_path?: string;
}

export interface SaveProfileRequest {
	name: string;
	scope?: 'workspace' | 'user';
	provider?: string;
	model?: string;
	base_url?: string;
	api_key?: string;
	api_key_env?: string;
	compactor?: CompactorConfig;
	set_active?: boolean;
}

export interface ModelsResponse {
	provider: string;
	models: string[];
}

