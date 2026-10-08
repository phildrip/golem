import { writable } from 'svelte/store';
import type { SessionInfo, ChatMessage, SubagentNode, MemoryItem, PatchProposal, SwarmMessage, ResourceLock, ProfileConfig } from './types';

export const sessions = writable<SessionInfo[]>([]);
export const activeSessionId = writable<string | null>(null);

export const messages = writable<Record<string, ChatMessage[]>>({});
export const subagents = writable<Record<string, SubagentNode[]>>({});
export const memory = writable<Record<string, MemoryItem[]>>({});
export const patches = writable<Record<string, PatchProposal[]>>({});
export const swarmMessages = writable<Record<string, SwarmMessage[]>>({});
export const swarmLocks = writable<Record<string, ResourceLock[]>>({});

// Track generation state per session ID so concurrent tabs do not block each other
export const isGenerating = writable<Record<string, boolean>>({});
export const serverConnected = writable<boolean>(false);

// Active profile & profiles catalog
export const activeProfile = writable<string>('default');
export const profilesList = writable<Record<string, ProfileConfig>>({});
export const userConfigPath = writable<string>('');
export const workspaceConfigPath = writable<string>('');

