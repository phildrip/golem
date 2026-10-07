<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Bot, Network, Database, GitPullRequest, Settings, Terminal, Wifi, WifiOff } from '@lucide/svelte';
	import SessionTabs from '$lib/components/SessionTabs.svelte';
	import ChatArea from '$lib/components/ChatArea.svelte';
	import PromptInput from '$lib/components/PromptInput.svelte';
	import SwarmTree from '$lib/components/SwarmTree.svelte';
	import MemoryExplorer from '$lib/components/MemoryExplorer.svelte';
	import PatchInspector from '$lib/components/PatchInspector.svelte';
	import { realtime } from '$lib/client';
	import {
		sessions,
		activeSessionId,
		messages,
		subagents,
		memory,
		patches,
		isGenerating,
		serverConnected
	} from '$lib/stores';
	import type { SessionInfo, ChatMessage, WSEvent, SubagentNode, MemoryItem, PatchProposal } from '$lib/types';

	let activeSession = $derived(
		$sessions.find((s) => s.id === $activeSessionId) || null
	);

	let currentMessages = $derived(
		$activeSessionId && $messages[$activeSessionId] ? $messages[$activeSessionId] : []
	);

	let currentSubagents = $derived(
		$activeSessionId && $subagents[$activeSessionId] ? $subagents[$activeSessionId] : []
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

	let showMemoryExplorer = $state(false);
	let showPatchInspector = $state(false);

	let unsubscribeWS: (() => void) | null = null;

	onMount(() => {
		realtime.connect();
		$serverConnected = realtime.isConnected;

		unsubscribeWS = realtime.subscribe((event: WSEvent) => {
			$serverConnected = true;
			handleRealtimeEvent(event);
		});

		fetchSessions();
	});

	onDestroy(() => {
		if (unsubscribeWS) unsubscribeWS();
		realtime.disconnect();
	});

	async function fetchSessions() {
		try {
			const res = await fetch('/api/sessions');
			if (res.ok) {
				const list = (await res.json()) as SessionInfo[];
				$sessions = list;
				if (list.length > 0 && !$activeSessionId) {
					$activeSessionId = list[0].id;
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
			}
		} catch (err) {
			console.error('Failed to load session memory:', err);
		}
	}

	const ALLOWED_OPS = new Set(['SWAP', 'DEL', 'INS_PRE', 'INS_POST', 'SWAP.BLK', 'DEL.BLK', 'INS.BLK.PRE', 'INS.BLK.POST']);

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

	function handleRealtimeEvent(event: WSEvent) {
		const sessId = event.session_id;
		if (!sessId) {
			// Ignore events not bound to a session to prevent state bleed across parallel sessions
			return;
		}

		switch (event.type) {
			case 'agent_chunk': {
				const payload = event.payload as { delta?: string };
				if (!payload.delta) break;

				const list = $messages[sessId] || [];
				const last = list[list.length - 1];
				if (last && last.role === 'assistant') {
					last.content += payload.delta;
					$messages = { ...$messages, [sessId]: [...list] };
				} else {
					const newMsg: ChatMessage = {
						id: `asst-${Date.now()}`,
						role: 'assistant',
						content: payload.delta,
						timestamp: event.timestamp
					};
					$messages = { ...$messages, [sessId]: [...list, newMsg] };
				}
				break;
			}

			case 'agent_done': {
				$isGenerating = { ...$isGenerating, [sessId]: false };
				break;
			}

			case 'tool_start': {
				const payload = event.payload as { tool: string; args: Record<string, unknown>; tool_call_id?: string };
				const list = $messages[sessId] || [];
				const last = list[list.length - 1];
				const toolCall = {
					id: payload.tool_call_id || `tool-${Date.now()}-${Math.random().toString(36).substring(2, 6)}`,
					toolName: payload.tool,
					args: payload.args,
					status: 'running' as const
				};

				if (last && last.role === 'assistant') {
					last.toolCalls = [...(last.toolCalls || []), toolCall];
					$messages = { ...$messages, [sessId]: [...list] };
				}
				break;
			}

			case 'tool_done': {
				const payload = event.payload as { tool: string; output: string; tool_call_id?: string };
				const list = $messages[sessId] || [];
				const last = list[list.length - 1];
				if (last && last.role === 'assistant' && last.toolCalls) {
					// Match by tool_call_id if provided, else fallback to latest running tool with matching name
					const t = payload.tool_call_id
						? last.toolCalls.find((x) => x.id === payload.tool_call_id)
						: last.toolCalls.find((x) => x.toolName === payload.tool && x.status === 'running');
					if (t) {
						t.status = 'done';
						t.output = payload.output;
						$messages = { ...$messages, [sessId]: [...list] };
					}
				}
				break;
			}

			case 'subagent_start': {
				const payload = event.payload as { id: string; name: string; role: string; model: string };
				const newNode: SubagentNode = {
					id: payload.id,
					sessionId: sessId,
					name: payload.name,
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
				const payload = event.payload as { id: string; tool: string };
				const current = $subagents[sessId] || [];
				const target = current.find((a) => a.id === payload.id);
				if (target) {
					target.toolsRun += 1;
					target.currentAction = `Running tool ${payload.tool}`;
					$subagents = { ...$subagents, [sessId]: [...current] };
				}
				break;
			}

			case 'subagent_done': {
				const payload = event.payload as { id: string; tokens?: number };
				const current = $subagents[sessId] || [];
				const target = current.find((a) => a.id === payload.id);
				if (target) {
					target.status = 'done';
					if (payload.tokens) target.tokensUsed = payload.tokens;
					target.currentAction = 'Completed task';
					$subagents = { ...$subagents, [sessId]: [...current] };
				}
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
		}
	}
</script>

<div class="h-screen w-screen flex flex-col bg-[#0b0f19] text-slate-100 overflow-hidden font-sans">
	<!-- Top Navigation Bar -->
	<header class="flex items-center justify-between h-12 px-4 bg-[#090d16] border-b border-slate-800/80">
		<div class="flex items-center gap-3">
			<div class="flex items-center gap-2 font-bold tracking-tight text-sm text-white">
				<div class="h-7 w-7 rounded-lg bg-blue-600 flex items-center justify-center text-white shadow-sm">
					<Bot class="w-4 h-4" />
				</div>
				<span>Golem</span>
			</div>
			<span class="text-xs px-2 py-0.5 rounded-full bg-slate-800 text-slate-400 font-mono">v0.2.0</span>
		</div>

		<!-- Status & Indicators -->
		<div class="flex items-center gap-2">
			<!-- Fractal Memory Toggle -->
			<button
				type="button"
				onclick={() => {
					showMemoryExplorer = !showMemoryExplorer;
					if (showMemoryExplorer && $activeSessionId) {
						fetchSessionMemory($activeSessionId);
					}
				}}
				class="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-slate-900 hover:bg-slate-800 border border-slate-800 text-xs text-slate-300 font-medium transition-colors"
				title="Explore bbolt fractal memory"
			>
				<Database class="w-3.5 h-3.5 text-emerald-400" />
				<span>Memory</span>
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
				class="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-slate-900 hover:bg-slate-800 border border-slate-800 text-xs text-slate-300 font-medium transition-colors"
				title="Inspect hashline patches"
			>
				<GitPullRequest class="w-3.5 h-3.5 text-blue-400" />
				<span>Patches</span>
				{#if currentPatches.length > 0}
					<span class="px-1.5 py-0.2 rounded-full bg-blue-500 text-white text-[10px] font-mono font-bold animate-pulse">
						{currentPatches.length}
					</span>
				{/if}
			</button>

			<div class="flex items-center gap-1.5 px-2.5 py-1 rounded-md bg-slate-900 border border-slate-800 text-[11px] font-mono">
				{#if $serverConnected}
					<Wifi class="w-3.5 h-3.5 text-emerald-400" />
					<span class="text-emerald-400">Live Multiplexer</span>
				{:else}
					<WifiOff class="w-3.5 h-3.5 text-rose-400" />
					<span class="text-rose-400">Disconnected</span>
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
	/>

	<!-- Main Workspace Split View -->
	<div class="flex-1 flex overflow-hidden">
		<!-- Central Chat & Turn Execution Area -->
		<main class="flex-1 flex flex-col bg-[#0b0f19] min-w-0">
			<ChatArea
				messages={currentMessages}
				isGenerating={currentGenerating}
			/>
			<PromptInput
				disabled={currentGenerating}
				onSubmit={submitPrompt}
			/>
		</main>

		<!-- Right Drawer: Swarm Hierarchy -->
		<aside class="flex-shrink-0">
			<SwarmTree
				subagents={currentSubagents}
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
</div>
