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
	| 'swarm_lock';

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

export interface WSEvent<T = unknown> {
	type: WSEventType;
	session_id?: string;
	timestamp: string;
	payload: T;
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
	format: 'agy' | 'cursor';
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

