<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Bot, Network, Database, GitPullRequest, Settings, Terminal, Wifi, WifiOff, Zap } from '@lucide/svelte';
	import SessionTabs from '$lib/components/SessionTabs.svelte';
	import ChatArea from '$lib/components/ChatArea.svelte';
	import PromptInput from '$lib/components/PromptInput.svelte';
	import SwarmTree from '$lib/components/SwarmTree.svelte';
	import MemoryExplorer from '$lib/components/MemoryExplorer.svelte';
	import PatchInspector from '$lib/components/PatchInspector.svelte';
	import ImportModal from '$lib/components/ImportModal.svelte';
	import ProfileModal from '$lib/components/ProfileModal.svelte';
	import ApprovalBanner from '$lib/components/ApprovalBanner.svelte';
	import { realtime, getProfiles, getGraphs } from '$lib/client';
	import { fetchAuthStatus, login } from '$lib/auth';
	import {
		sessions,
		activeSessionId,
		messages,
		subagents,
		memory,
		patches,
		swarmMessages,
		swarmLocks,
		pendingApprovals,
		isGenerating,
		serverConnected,
		activeProfile,
		profilesList,
		userConfigPath,
		workspaceConfigPath
	} from '$lib/stores';
	import type { SessionInfo, ChatMessage, WSEvent, SubagentNode, MemoryItem, PatchProposal, SwarmMessage, ResourceLock, SwarmLockEvent, ProfilesResponse, ApprovalPrompt, GraphSpec, GraphRunRecord, TokenUsagePayload } from '$lib/types';

	let sessionTokenUsage = $state<Record<string, TokenUsagePayload>>({});
	let currentTokenUsage = $derived($activeSessionId ? sessionTokenUsage[$activeSessionId] : undefined);

	let activeSession = $derived(
		$sessions.find((s) => s.id === $activeSessionId) || null
	);

	let currentMessages = $derived(
		$activeSessionId && $messages[$activeSessionId] ? $messages[$activeSessionId] : []
	);

	let currentSubagents = $derived(
		$activeSessionId && $subagents[$activeSessionId] ? $subagents[$activeSessionId] : []
	);

	let currentSwarmMessages = $derived(
		$activeSessionId && $swarmMessages[$activeSessionId] ? $swarmMessages[$activeSessionId] : []
	);

	let currentSwarmLocks = $derived(
		$activeSessionId && $swarmLocks[$activeSessionId] ? $swarmLocks[$activeSessionId] : []
	);

	let currentMemory = $derived(
		$activeSessionId && $memory[$activeSessionId] ? $memory[$activeSessionId] : []
	);

	let currentPatches = $derived(
		$activeSessionId && $patches[$activeSessionId] ? $patches[$activeSessionId] : []
	);

	let currentGenerating = $derived(
		$activeSessionId ? Boolean($isGenerating[$activeSessionId]) : false
	);

	let currentApproval = $derived(
		$activeSessionId ? ($pendingApprovals[$activeSessionId] ?? null) : null
	);

	let showMemoryExplorer = $state(false);
	let showPatchInspector = $state(false);
	let showImportModal = $state(false);
	let showProfileModal = $state(false);
	let showSwarmDrawer = $state(false);
	let graphSpecs = $state<GraphSpec[]>([]);
	let graphRuns = $state<GraphRunRecord[]>([]);

	// Mobile token coalescing (~30fps / 33ms) so high-throughput streams stay responsive on phones (golem-2yu.5).
	const CHUNK_COALESCE_MS = 33;
	let pendingChunks: Map<string, { text: string; timestamp?: string }> = new Map();
	let chunkFlushTimer: ReturnType<typeof setTimeout> | null = null;

	function flushPendingChunks(targetSessionId?: string) {
		if (chunkFlushTimer && !targetSessionId) {
			clearTimeout(chunkFlushTimer);
			chunkFlushTimer = null;
		}
		const entries = targetSessionId
			? (pendingChunks.has(targetSessionId) ? [[targetSessionId, pendingChunks.get(targetSessionId)!] as const] : [])
			: Array.from(pendingChunks.entries());
		if (entries.length === 0) return;

		for (const [sessId, buf] of entries) {
			pendingChunks.delete(sessId);
			if (!buf.text) continue;
			const msg = getOrCreateAssistantMessage(sessId, buf.timestamp);
			msg.content += buf.text;
			$messages = { ...$messages, [sessId]: [...($messages[sessId] || [])] };
		}
	}

	let unsubscribeWS: (() => void) | null = null;
	let unsubscribeConn: (() => void) | null = null;

	function handleSessionImported(session: SessionInfo) {
		$sessions = [...$sessions, session];
		$activeSessionId = session.id;
		showImportModal = false;
		if (showMemoryExplorer) {
			fetchSessionMemory(session.id);
		}
	}

	// Auth gate: the SPA shell loads unauthenticated, so ask the server whether a
	// token is needed before opening the WebSocket or fetching any session data.
	let authState = $state<'checking' | 'locked' | 'open'>('checking');
	let tokenInput = $state('');
	let authError = $state('');
	let authSubmitting = $state(false);

	async function checkAuthThenConnect() {
		try {
			const status = await fetchAuthStatus();
			if (status.authRequired && !status.authenticated) {
				authState = 'locked';
				return;
			}
		} catch {
			// Server unreachable: fall through so the normal disconnected state shows.
		}
		authState = 'open';
		realtime.connect();
		fetchSessions();
		fetchProfiles();
		fetchGraphs();
	}

	async function submitToken(event: Event) {
		event.preventDefault();
		const token = tokenInput.trim();
		if (!token || authSubmitting) return;
		authSubmitting = true;
		authError = '';
		const ok = await login(token);
		authSubmitting = false;
		if (ok) {
			// Reload rather than resume in place: the cookie now authenticates every
			// fetch and the WebSocket, so a clean boot is simpler than re-seeding state.
			window.location.reload();
			return;
		}
		authError = 'That token was not accepted.';
	}

	onMount(() => {
		void checkAuthThenConnect();

		unsubscribeConn = realtime.onConnectionChange((connected) => {
			$serverConnected = connected;
			if (connected) {
				fetchSessions();
				fetchProfiles();
				fetchGraphs();
				if ($activeSessionId) {
					fetchSessionMemory($activeSessionId);
					void refreshPendingApproval($activeSessionId);
				}
			} else {
				// Clear stuck generation states on disconnect
				if ($activeSessionId && $isGenerating[$activeSessionId]) {
					$isGenerating = { ...$isGenerating, [$activeSessionId]: false };
				}
			}
		});

		unsubscribeWS = realtime.subscribe((event: WSEvent) => {
			handleRealtimeEvent(event);
		});
	});

	onDestroy(() => {
		flushPendingChunks();
		if (unsubscribeWS) unsubscribeWS();
		if (unsubscribeConn) unsubscribeConn();
		realtime.disconnect();
	});

	async function fetchGraphs() {
		try {
			const res = await getGraphs();
			graphSpecs = res.specs || [];
			graphRuns = res.runs || [];
		} catch {
			// ignore
		}
	}

	async function fetchSessions() {
		try {
			const res = await fetch('/api/sessions');
			if (res.ok) {
				const list = (await res.json()) as SessionInfo[];
				$sessions = list;
				if (list.length > 0) {
					if (!$activeSessionId || !list.some((s) => s.id === $activeSessionId)) {
						$activeSessionId = list[0].id;
					}
					fetchSessionMemory($activeSessionId);
				} else {
					createNewSession();
				}
			}
		} catch {
			// server might not be running yet
		}
	}

	async function createNewSession() {
		try {
			const res = await fetch('/api/sessions', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ title: `Session ${$sessions.length + 1}` })
			});
			if (res.ok) {
				const created = (await res.json()) as SessionInfo;
				$sessions = [...$sessions, created];
				$activeSessionId = created.id;
				fetchSessionMemory(created.id);
			}
		} catch (err) {
			console.error('Failed to create session:', err);
		}
	}

	async function fetchSessionMemory(id: string) {
		try {
			const res = await fetch(`/api/sessions/${id}/memory`);
			if (res.ok) {
				const events = (await res.json()) as MemoryItem[];
				$memory = { ...$memory, [id]: events };

				// Reconstruct messages from fractal memory events if chat messages list is empty
				if (!$messages[id] || $messages[id].length === 0) {
					const reconstructed: ChatMessage[] = [];
					for (const ev of events) {
						if (ev.kind === 'user') {
							reconstructed.push({
								id: `user-${ev.id}`,
								role: 'user',
								content: ev.text,
								timestamp: ev.timestamp
							});
						} else if (ev.kind === 'talk') {
							reconstructed.push({
								id: `asst-${ev.id}`,
								role: 'assistant',
								content: ev.text,
								timestamp: ev.timestamp
							});
						}
					}
					if (reconstructed.length > 0) {
						$messages = { ...$messages, [id]: reconstructed };
					}
				}
			}
		} catch (err) {
			console.error('Failed to load session memory:', err);
		}
	}

	const ALLOWED_OPS = new Set(['SWAP', 'DEL', 'INS_PRE', 'INS_POST', 'SWAP.BLK', 'DEL.BLK', 'INS.BLK.PRE', 'INS.BLK.POST']);

	// answerApproval delivers the user's decision on a sandbox confirmation and
	// resumes the paused turn server-side.
	async function answerApproval(approved: boolean) {
		const sessId = $activeSessionId;
		const prompt = currentApproval;
		if (!sessId || !prompt) return;

		$pendingApprovals = { ...$pendingApprovals, [sessId]: null };

		const res = await fetch(`/api/sessions/${sessId}/approval`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ call_id: prompt.call_id, approved })
		});
		if (!res.ok) {
			// Put the prompt back: the answer never reached the agent.
			$pendingApprovals = { ...$pendingApprovals, [sessId]: prompt };
			$isGenerating = { ...$isGenerating, [sessId]: false };
		}
	}

	// refreshPendingApproval re-renders a prompt that outlived a reconnect.
	async function refreshPendingApproval(sessId: string) {
		try {
			const res = await fetch(`/api/sessions/${sessId}/approval`);
			if (!res.ok) return;
			const data = (await res.json()) as { pending?: boolean } & ApprovalPrompt;
			$pendingApprovals = {
				...$pendingApprovals,
				[sessId]: data.pending ? data : null
			};
		} catch {
			// Offline: the next event or reload will resync.
		}
	}

	async function applyPatch(patch: PatchProposal) {
		if (!ALLOWED_OPS.has(patch.op)) {
			throw new Error(`Invalid patch operation: ${patch.op}`);
		}
		if (!patch.filePath || !patch.anchor) {
			throw new Error('Patch must contain both filePath and anchor');
		}

		const res = await fetch('/api/patch', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({
				file_path: patch.filePath,
				op: patch.op,
				anchor: patch.anchor,
				content: patch.content,
				dry_run: false
			})
		});
		if (!res.ok) {
			const errData = await res.json().catch(() => ({ error: 'Failed to apply patch' }));
			throw new Error(errData.error || res.statusText);
		}
		// Remove applied patch from list
		if ($activeSessionId) {
			const list = $patches[$activeSessionId] || [];
			$patches = {
				...$patches,
				[$activeSessionId]: list.filter((p) => p !== patch)
			};
		}
	}

	function rejectPatch(patch: PatchProposal) {
		if ($activeSessionId) {
			const list = $patches[$activeSessionId] || [];
			$patches = {
				...$patches,
				[$activeSessionId]: list.filter((p) => p !== patch)
			};
		}
	}

	async function closeSession(id: string) {
		try {
			const res = await fetch(`/api/sessions/${id}`, { method: 'DELETE' });
			if (res.ok) {
				$sessions = $sessions.filter((s) => s.id !== id);
				if ($activeSessionId === id) {
					$activeSessionId = $sessions.length > 0 ? $sessions[0].id : null;
				}
			}
		} catch (err) {
			console.error('Failed to delete session:', err);
		}
	}

	async function submitPrompt(prompt: string) {
		if (!$activeSessionId) {
			await createNewSession();
		}
		const sessId = $activeSessionId;
		if (!sessId) return;

		// Append user message locally
		const userMsg: ChatMessage = {
			id: `user-${Date.now()}`,
			role: 'user',
			content: prompt,
			timestamp: new Date().toISOString()
		};

		$messages = {
			...$messages,
			[sessId]: [...($messages[sessId] || []), userMsg]
		};

		$isGenerating = { ...$isGenerating, [sessId]: true };

		try {
			const res = await fetch(`/api/sessions/${sessId}/prompt`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ prompt })
			});
			if (!res.ok) {
				const errData = await res.json().catch(() => ({ error: 'HTTP request failed' }));
				const errMsg: ChatMessage = {
					id: `err-${Date.now()}`,
					role: 'assistant',
					content: `⚠️ Error from server: ${errData.error || res.statusText}`,
					timestamp: new Date().toISOString()
				};
				$messages = {
					...$messages,
					[sessId]: [...($messages[sessId] || []), errMsg]
				};
				$isGenerating = { ...$isGenerating, [sessId]: false };
			}
		} catch (err: any) {
			console.error('Failed to send prompt:', err);
			const errMsg: ChatMessage = {
				id: `err-${Date.now()}`,
				role: 'assistant',
				content: `⚠️ Network error: Could not reach Golem server. ${err?.message || ''}`,
				timestamp: new Date().toISOString()
			};
			$messages = {
				...$messages,
				[sessId]: [...($messages[sessId] || []), errMsg]
			};
			$isGenerating = { ...$isGenerating, [sessId]: false };
		}
	}

	function getOrCreateAssistantMessage(sessId: string, timestamp?: string): ChatMessage {
		const list = $messages[sessId] || [];
		const last = list[list.length - 1];
		if (last && last.role === 'assistant') {
			return last;
		}
		const newMsg: ChatMessage = {
			id: `asst-${Date.now()}-${Math.random().toString(36).substring(2, 6)}`,
			role: 'assistant',
			content: '',
			toolCalls: [],
			timestamp: timestamp || new Date().toISOString()
		};
		$messages = { ...$messages, [sessId]: [...list, newMsg] };
		return newMsg;
	}

	function handleRealtimeEvent(event: WSEvent) {
		// Handle global events not bound to a specific session
		if (event.type === 'profile_changed') {
			const payload = event.payload as ProfilesResponse;
			if (payload.active_profile) $activeProfile = payload.active_profile;
			if (payload.profiles) $profilesList = payload.profiles;
			return;
		}

		const sessId = event.session_id;
		if (!sessId) {
			// Ignore other events not bound to a session to prevent state bleed
			return;
		}

		switch (event.type) {
			case 'user_prompt': {
				const payload = event.payload as { prompt?: string };
				if (payload.prompt) {
					const list = $messages[sessId] || [];
					const last = list[list.length - 1];
					if (!last || last.role !== 'user' || last.content !== payload.prompt) {
						const newMsg: ChatMessage = {
							id: `user-${Date.now()}`,
							role: 'user',
							content: payload.prompt,
							timestamp: event.timestamp || new Date().toISOString()
						};
						$messages = { ...$messages, [sessId]: [...list, newMsg] };
					}
					$isGenerating = { ...$isGenerating, [sessId]: true };
				}
				break;
			}

			case 'agent_chunk': {
				const payload = event.payload as { delta?: string; token?: string };
				const text = payload.delta || payload.token || '';
				if (!text) break;

				const prev = pendingChunks.get(sessId);
				if (prev) {
					prev.text += text;
				} else {
					pendingChunks.set(sessId, { text, timestamp: event.timestamp });
				}
				if (!chunkFlushTimer) {
					chunkFlushTimer = setTimeout(() => {
						chunkFlushTimer = null;
						flushPendingChunks();
					}, CHUNK_COALESCE_MS);
				}
				break;
			}

			case 'agent_done': {
				flushPendingChunks(sessId);
				const payload = event.payload as { response?: string; error?: string; awaiting_approval?: boolean };
				if (payload.awaiting_approval) {
					// Paused on a confirmation, not finished: the banner is up and
					// answering it resumes this same turn.
					$isGenerating = { ...$isGenerating, [sessId]: true };
					break;
				}
				$isGenerating = { ...$isGenerating, [sessId]: false };
				if (payload.error) {
					const msg = getOrCreateAssistantMessage(sessId, event.timestamp);
					if (msg.content) {
						msg.content += `\n\n⚠️ **Error:** ${payload.error}`;
					} else {
						msg.content = `⚠️ **Error:** ${payload.error}`;
					}
					$messages = { ...$messages, [sessId]: [...($messages[sessId] || [])] };
				} else if (payload.response) {
					const msg = getOrCreateAssistantMessage(sessId, event.timestamp);
					if (!msg.content) {
						msg.content = payload.response;
						$messages = { ...$messages, [sessId]: [...($messages[sessId] || [])] };
					}
				}
				fetchSessionMemory(sessId);
				fetchGraphs();
				break;
			}

			case 'token_usage': {
				const payload = event.payload as TokenUsagePayload;
				sessionTokenUsage = { ...sessionTokenUsage, [sessId]: payload };
				break;
			}

			case 'resume_failed': {
				// Gap exceeded the server's 500-event ring buffer: refetch full session transcript.
				pendingChunks.delete(sessId);
				$messages = { ...$messages, [sessId]: [] };
				fetchSessionMemory(sessId);
				void refreshPendingApproval(sessId);
				break;
			}

			case 'approval_request': {
				flushPendingChunks(sessId);
				// The sandbox stopped the turn: a tool call is waiting for a human.
				const payload = event.payload as ApprovalPrompt;
				$pendingApprovals = { ...$pendingApprovals, [sessId]: payload };
				$isGenerating = { ...$isGenerating, [sessId]: true };
				break;
			}

			case 'tool_start': {
				flushPendingChunks(sessId);
				const payload = event.payload as { tool?: string; command?: string; args?: Record<string, unknown>; tool_call_id?: string };
				const toolCall = {
					id: payload.tool_call_id || `tool-${Date.now()}-${Math.random().toString(36).substring(2, 6)}`,
					toolName: payload.tool || 'tool',
					args: payload.args || (payload.command ? { command: payload.command } : {}),
					status: 'running' as const
				};

				const msg = getOrCreateAssistantMessage(sessId, event.timestamp);
				msg.toolCalls = [...(msg.toolCalls || []), toolCall];
				$messages = { ...$messages, [sessId]: [...($messages[sessId] || [])] };
				break;
			}

			case 'tool_done': {
				const payload = event.payload as { tool?: string; output?: string; tool_call_id?: string; exit_code?: number };
				const list = $messages[sessId] || [];
				// Search backwards through assistant messages to find the running tool call
				for (let i = list.length - 1; i >= 0; i--) {
					const msg = list[i];
					if (msg.role === 'assistant' && msg.toolCalls && msg.toolCalls.length > 0) {
						const t = payload.tool_call_id
							? msg.toolCalls.find((x) => x.id === payload.tool_call_id)
							: msg.toolCalls.find((x) => x.toolName === payload.tool && x.status === 'running') || msg.toolCalls[msg.toolCalls.length - 1];
						if (t) {
							t.status = (payload.exit_code === 0 || payload.exit_code === undefined) ? 'done' : 'error';
							t.output = payload.output || '';
							$messages = { ...$messages, [sessId]: [...list] };
							break;
						}
					}
				}
				break;
			}

			case 'subagent_start': {
				const payload = event.payload as { id?: string; name?: string; role: string; model: string };
				const subId = payload.id || `subagent-${payload.role || 'worker'}-${Date.now()}`;
				const subName = payload.name || payload.role || 'Subagent';
				const newNode: SubagentNode = {
					id: subId,
					sessionId: sessId,
					name: subName,
					role: payload.role,
					model: payload.model,
					status: 'running',
					tokensUsed: 0,
					toolsRun: 0,
					startedAt: event.timestamp
				};
				const current = $subagents[sessId] || [];
				$subagents = { ...$subagents, [sessId]: [...current, newNode] };
				break;
			}

			case 'subagent_tool': {
				const payload = event.payload as { id?: string; role?: string; tool: string };
				const current = $subagents[sessId] || [];
				const target = current.find((a) => (payload.id && a.id === payload.id) || (payload.role && a.role === payload.role));
				if (target) {
					target.toolsRun += 1;
					target.currentAction = `Running tool ${payload.tool}`;
					$subagents = { ...$subagents, [sessId]: [...current] };
				}
				break;
			}

			case 'subagent_done': {
				const payload = event.payload as { id?: string; role?: string; tokens?: number };
				const current = $subagents[sessId] || [];
				const target = current.find((a) => (payload.id && a.id === payload.id) || (payload.role && a.role === payload.role));
				if (target) {
					target.status = 'done';
					if (payload.tokens) target.tokensUsed = payload.tokens;
					target.currentAction = 'Completed task';
					$subagents = { ...$subagents, [sessId]: [...current] };
				}
				fetchGraphs();
				break;
			}

			case 'memory_added': {
				const payload = event.payload as MemoryItem;
				const current = $memory[sessId] || [];
				$memory = { ...$memory, [sessId]: [...current, payload] };
				break;
			}

			case 'patch_proposed': {
				const payload = event.payload as PatchProposal;
				const current = $patches[sessId] || [];
				$patches = { ...$patches, [sessId]: [...current, payload] };
				break;
			}

			case 'swarm_message': {
				const payload = event.payload as SwarmMessage;
				const current = $swarmMessages[sessId] || [];
				$swarmMessages = { ...$swarmMessages, [sessId]: [...current, payload] };
				break;
			}

			case 'swarm_lock': {
				const payload = event.payload as SwarmLockEvent;
				const current = $swarmLocks[sessId] || [];
				const next =
					payload.type === 'released'
						? current.filter((l) => l.resource !== payload.lock.resource)
						: [...current.filter((l) => l.resource !== payload.lock.resource), payload.lock];
				$swarmLocks = { ...$swarmLocks, [sessId]: next };
				break;
			}
		}
	}
</script>

<div class="h-[100dvh] w-full flex flex-col bg-[#0b0f19] text-slate-100 overflow-hidden font-sans">
	{#if authState === 'locked'}
		<div class="fixed inset-0 z-[100] flex items-center justify-center bg-[#0b0f19] p-6">
			<form class="w-full max-w-sm flex flex-col gap-3" onsubmit={submitToken}>
				<div class="flex items-center gap-2 font-bold tracking-tight text-white">
					<div class="h-7 w-7 rounded-lg bg-blue-600 flex items-center justify-center">
						<Bot class="w-4 h-4" />
					</div>
					<span>Golem</span>
				</div>
				<p class="text-xs text-slate-400 leading-relaxed">
					This server requires an access token. It is printed in the terminal running
					<code class="font-mono text-slate-300">golem -serve</code> and stored in
					<code class="font-mono text-slate-300">~/.golem/config.json</code>.
				</p>
				<input
					bind:value={tokenInput}
					type="password"
					autocomplete="current-password"
					placeholder="Access token"
					class="w-full px-3 py-2.5 min-h-[44px] rounded-lg bg-slate-900 border border-slate-800 text-slate-100 text-base placeholder:text-slate-600 focus:outline-none focus:border-blue-600"
				/>
				{#if authError}
					<p class="text-xs text-red-400">{authError}</p>
				{/if}
				<button
					type="submit"
					disabled={authSubmitting || !tokenInput.trim()}
					class="w-full px-3 py-2.5 min-h-[44px] rounded-lg bg-blue-600 hover:bg-blue-500 disabled:opacity-50 text-white text-sm font-medium transition-colors"
				>
					{authSubmitting ? 'Checking…' : 'Unlock'}
				</button>
			</form>
		</div>
	{/if}

	<!-- Top Navigation Bar -->
	<header class="flex items-center justify-between min-h-[48px] px-2.5 sm:px-4 bg-[#090d16] border-b border-slate-800/80 gap-2">
		<div class="flex items-center gap-2 shrink-0">
			<div class="flex items-center gap-2 font-bold tracking-tight text-sm text-white">
				<div class="h-7 w-7 rounded-lg bg-blue-600 flex items-center justify-center text-white shadow-sm">
					<Bot class="w-4 h-4" />
				</div>
				<span>Golem</span>
			</div>
			<span class="hidden sm:inline text-xs px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 font-mono">v0.2.0</span>
		</div>

		<!-- Status & Indicators -->
		<div class="flex items-center gap-1.5 sm:gap-2 overflow-x-auto no-scrollbar">
			<!-- Mobile Swarm & Graph DAG Toggle (< md) -->
			<button
				type="button"
				onclick={() => {
					showSwarmDrawer = !showSwarmDrawer;
					if (showSwarmDrawer) fetchGraphs();
				}}
				class="md:hidden flex items-center justify-center gap-1.5 min-h-[44px] min-w-[44px] px-2.5 rounded-md bg-slate-900 hover:bg-slate-800 border border-slate-800 text-xs text-purple-300 font-medium transition-colors"
				title="Swarm & Workflow Graphs"
			>
				<Network class="w-4 h-4 text-purple-400" />
				<span class="hidden xs:inline">Swarm</span>
			</button>

			<!-- Profile Settings Button -->
			<button
				type="button"
				onclick={() => (showProfileModal = true)}
				class="flex items-center justify-center gap-1.5 min-h-[44px] md:min-h-0 px-2.5 py-1 rounded-md bg-slate-900 hover:bg-slate-800 border border-slate-800 text-xs text-purple-300 font-medium transition-colors"
				title="Manage models, providers, and profiles"
			>
				<Settings class="w-3.5 h-3.5 text-purple-400" />
				<span class="font-mono text-[11px]">{$activeProfile || 'default'}</span>
			</button>

			<!-- Fractal Memory Toggle -->
			<button
				type="button"
				onclick={() => {
					showMemoryExplorer = !showMemoryExplorer;
					if (showMemoryExplorer && $activeSessionId) {
						fetchSessionMemory($activeSessionId);
					}
				}}
				class="flex items-center justify-center gap-1.5 min-h-[44px] md:min-h-0 px-2.5 py-1 rounded-md bg-slate-900 hover:bg-slate-800 border border-slate-800 text-xs text-slate-300 font-medium transition-colors"
				title="Explore bbolt fractal memory"
			>
				<Database class="w-3.5 h-3.5 text-emerald-400" />
				<span class="hidden sm:inline">Memory</span>
				{#if currentMemory.length > 0}
					<span class="px-1 py-0.2 rounded bg-emerald-500/20 text-emerald-400 text-[10px] font-mono">
						{currentMemory.length}
					</span>
				{/if}
			</button>

			<!-- Hashline Patch Inspector Toggle -->
			<button
				type="button"
				onclick={() => (showPatchInspector = !showPatchInspector)}
				class="flex items-center justify-center gap-1.5 min-h-[44px] md:min-h-0 px-2.5 py-1 rounded-md bg-slate-900 hover:bg-slate-800 border border-slate-800 text-xs text-slate-300 font-medium transition-colors"
				title="Inspect hashline patches"
			>
				<GitPullRequest class="w-3.5 h-3.5 text-blue-400" />
				<span class="hidden sm:inline">Patches</span>
				{#if currentPatches.length > 0}
					<span class="px-1.5 py-0.2 rounded-full bg-blue-500 text-white text-[10px] font-mono font-bold animate-pulse">
						{currentPatches.length}
					</span>
				{/if}
			</button>

			<!-- Prompt Cache Hit/Miss Badge -->
			{#if currentTokenUsage && currentTokenUsage.prompt_tokens > 0}
				<div
					class="flex items-center gap-1.5 px-2.5 py-1 rounded-md border text-[11px] font-mono shrink-0 {currentTokenUsage.cached_tokens > 0 ? 'bg-emerald-950/40 border-emerald-800/60 text-emerald-300' : 'bg-amber-950/40 border-amber-800/60 text-amber-300'}"
					title="{currentTokenUsage.cached_tokens} cached / {currentTokenUsage.prompt_tokens} prompt tokens ({Math.round(currentTokenUsage.cache_hit_ratio * 100)}%)"
				>
					<Zap class="w-3.5 h-3.5 {currentTokenUsage.cached_tokens > 0 ? 'text-emerald-400' : 'text-amber-400'}" />
					{#if currentTokenUsage.cached_tokens > 0}
						<span>⚡ {Math.round(currentTokenUsage.cache_hit_ratio * 100)}% Cache Hit</span>
					{:else}
						<span>⚡ 0% Cache Miss</span>
					{/if}
				</div>
			{/if}

			<div class="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-slate-900 border border-slate-800 text-[11px] font-mono shrink-0">
				{#if $serverConnected}
					<Wifi class="w-3.5 h-3.5 text-emerald-400" />
					<span class="hidden sm:inline text-emerald-400">Live Multiplexer</span>
				{:else}
					<WifiOff class="w-3.5 h-3.5 text-rose-400" />
					<span class="hidden sm:inline text-rose-400">Disconnected</span>
				{/if}
			</div>
		</div>
	</header>

	<!-- Session Tabs Bar -->
	<SessionTabs
		sessions={$sessions}
		activeId={$activeSessionId}
		onSelect={(id) => {
			$activeSessionId = id;
			if (showMemoryExplorer) fetchSessionMemory(id);
		}}
		onCreate={createNewSession}
		onClose={closeSession}
		onOpenImport={() => (showImportModal = true)}
	/>

	<!-- Main Workspace Split View -->
	<div class="flex-1 flex overflow-hidden relative">
		<!-- Central Chat & Turn Execution Area -->
		<main class="flex-1 flex flex-col bg-[#0b0f19] min-w-0">
			<ChatArea
				messages={currentMessages}
				isGenerating={currentGenerating}
			/>
			{#if currentApproval}
				<ApprovalBanner
					prompt={currentApproval}
					onApprove={() => answerApproval(true)}
					onDeny={() => answerApproval(false)}
				/>
			{/if}
			<PromptInput
				disabled={currentGenerating}
				onSubmit={submitPrompt}
			/>
		</main>

		<!-- Right Drawer: Swarm Hierarchy, Graph DAG & Comms (desktop side-by-side, mobile full-bleed sheet) -->
		<aside class={showSwarmDrawer ? "fixed inset-0 z-50 md:static md:block flex-shrink-0" : "hidden md:block flex-shrink-0"}>
			<SwarmTree
				subagents={currentSubagents}
				swarmMessages={currentSwarmMessages}
				locks={currentSwarmLocks}
				{graphSpecs}
				{graphRuns}
				onClose={() => (showSwarmDrawer = false)}
			/>
		</aside>
	</div>

	<!-- Fractal Memory Drawer -->
	{#if showMemoryExplorer}
		<MemoryExplorer
			memoryItems={currentMemory}
			onClose={() => (showMemoryExplorer = false)}
			onRefresh={() => $activeSessionId && fetchSessionMemory($activeSessionId)}
		/>
	{/if}

	<!-- Hashline Patch Inspector Drawer -->
	{#if showPatchInspector}
		<PatchInspector
			patches={currentPatches}
			onClose={() => (showPatchInspector = false)}
			onApplyPatch={applyPatch}
			onRejectPatch={rejectPatch}
		/>
	{/if}

	<!-- Historical Chat Import Modal -->
	<ImportModal
		open={showImportModal}
		onClose={() => (showImportModal = false)}
		onImported={handleSessionImported}
	/>

	<!-- Profile Settings Modal -->
	<ProfileModal
		open={showProfileModal}
		onClose={() => (showProfileModal = false)}
	/>
</div>
