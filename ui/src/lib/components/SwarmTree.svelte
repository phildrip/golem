<script lang="ts">
	import { Network, Bot, Cpu, CheckCircle2, AlertCircle, Clock, GitFork, Radio, Lock, X } from '@lucide/svelte';
	import type { SubagentNode, SwarmMessage, ResourceLock, GraphSpec, GraphRunRecord } from '$lib/types';
	import { cn } from '$lib/utils';

	interface Props {
		subagents: SubagentNode[];
		swarmMessages?: SwarmMessage[];
		locks?: ResourceLock[];
		graphSpecs?: GraphSpec[];
		graphRuns?: GraphRunRecord[];
		selectedAgentId?: string | null;
		onSelectAgent?: (agent: SubagentNode) => void;
		onClose?: () => void;
	}

	let {
		subagents,
		swarmMessages = [],
		locks = [],
		graphSpecs = [],
		graphRuns = [],
		selectedAgentId = null,
		onSelectAgent,
		onClose
	}: Props = $props();

	let activeTab = $state<'tree' | 'dag' | 'chat' | 'locks'>('tree');
	let selectedGraphIdx = $state<number>(0);

	// Derive live node status map from active subagent events ("<graph>/<role>·<node>" or "<graph>/<node>")
	let liveNodeState = $derived.by(() => {
		const map: Record<string, { status: string; activations: number; action?: string }> = {};
		for (const sub of subagents) {
			const slash = sub.role.indexOf('/');
			if (slash <= 0) continue;
			const graphName = sub.role.slice(0, slash);
			const rest = sub.role.slice(slash + 1);
			const dot = rest.indexOf('·');
			const nodeId = dot > 0 ? rest.slice(dot + 1) : rest;
			const key = `${graphName}/${nodeId}`;
			const prev = map[key];
			map[key] = {
				status: sub.status === 'running' ? 'running' : sub.status === 'error' ? 'failed' : 'ok',
				activations: (prev?.activations || 0) + 1,
				action: sub.currentAction
			};
		}
		return map;
	});

	let activeGraphItem = $derived.by(() => {
		if (graphRuns.length > 0 && selectedGraphIdx < graphRuns.length) {
			const run = graphRuns[selectedGraphIdx];
			const spec = run.spec || graphSpecs.find((s) => s.name === run.graph);
			return { name: run.graph, spec, run };
		}
		const specIdx = Math.max(0, selectedGraphIdx - graphRuns.length);
		const spec = graphSpecs[specIdx];
		if (spec) {
			return { name: spec.name, spec, run: undefined };
		}
		return null;
	});
</script>

<div class="flex flex-col h-full bg-[#0a0e17] border-l border-slate-800/80 text-xs text-slate-300 w-full md:w-80">
	<div class="flex items-center justify-between px-3 py-2 border-b border-slate-800/80 bg-slate-950/40 gap-2">
		<div class="flex items-center gap-1 p-0.5 rounded-lg bg-slate-900 border border-slate-800 text-[11px] overflow-x-auto no-scrollbar">
			<button
				type="button"
				class={cn(
					"flex items-center gap-1 px-2 py-1.5 min-h-[36px] md:min-h-0 rounded-md transition-colors shrink-0",
					activeTab === 'tree' ? "bg-purple-950/50 text-purple-300 font-medium" : "text-slate-400 hover:text-slate-200"
				)}
				onclick={() => (activeTab = 'tree')}
			>
				<Network class="w-3.5 h-3.5 text-purple-400" />
				<span>Swarm</span>
				{#if subagents.length > 0}
					<span class="px-1 py-0.2 rounded text-[9px] bg-purple-500/20 text-purple-300 font-mono">
						{subagents.length}
					</span>
				{/if}
			</button>

			<button
				type="button"
				class={cn(
					"flex items-center gap-1 px-2 py-1.5 min-h-[36px] md:min-h-0 rounded-md transition-colors shrink-0",
					activeTab === 'dag' ? "bg-emerald-950/50 text-emerald-300 font-medium" : "text-slate-400 hover:text-slate-200"
				)}
				onclick={() => (activeTab = 'dag')}
			>
				<GitFork class="w-3.5 h-3.5 text-emerald-400" />
				<span>DAG</span>
				{#if graphRuns.length + graphSpecs.length > 0}
					<span class="px-1 py-0.2 rounded text-[9px] bg-emerald-500/20 text-emerald-300 font-mono">
						{graphRuns.length || graphSpecs.length}
					</span>
				{/if}
			</button>

			<button
				type="button"
				class={cn(
					"flex items-center gap-1 px-2 py-1.5 min-h-[36px] md:min-h-0 rounded-md transition-colors shrink-0",
					activeTab === 'chat' ? "bg-blue-950/50 text-blue-300 font-medium" : "text-slate-400 hover:text-slate-200"
				)}
				onclick={() => (activeTab = 'chat')}
			>
				<Radio class="w-3.5 h-3.5 text-blue-400" />
				<span>Comms</span>
				{#if swarmMessages.length > 0}
					<span class="px-1.5 py-0.2 rounded-full text-[9px] bg-blue-500 text-white font-mono font-bold animate-pulse">
						{swarmMessages.length}
					</span>
				{/if}
			</button>

			<button
				type="button"
				class={cn(
					"flex items-center gap-1 px-2 py-1.5 min-h-[36px] md:min-h-0 rounded-md transition-colors shrink-0",
					activeTab === 'locks' ? "bg-amber-950/50 text-amber-300 font-medium" : "text-slate-400 hover:text-slate-200"
				)}
				onclick={() => (activeTab = 'locks')}
			>
				<Lock class="w-3.5 h-3.5 text-amber-400" />
				<span>Locks</span>
				{#if locks.length > 0}
					<span class="px-1 py-0.2 rounded text-[9px] bg-amber-500/20 text-amber-300 font-mono">
						{locks.length}
					</span>
				{/if}
			</button>
		</div>

		{#if onClose}
			<button
				type="button"
				onclick={onClose}
				class="md:hidden flex items-center justify-center h-11 w-11 rounded-lg bg-slate-900 border border-slate-800 text-slate-300 hover:text-white shrink-0"
				title="Close Swarm Drawer"
			>
				<X class="w-4 h-4" />
			</button>
		{/if}
	</div>

	<!-- Hierarchy Tab Content -->
	<div class={cn("flex-1 overflow-y-auto p-2.5 space-y-2 no-scrollbar", activeTab !== 'tree' && "hidden")}>
		<!-- Root Main Agent -->
		<div class="p-2.5 rounded-lg border border-slate-800 bg-slate-900/60 shadow-sm">
			<div class="flex items-center justify-between">
				<div class="flex items-center gap-2">
					<div class="p-1 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20">
						<Bot class="w-3.5 h-3.5" />
					</div>
					<div>
						<div class="font-medium text-slate-100 text-xs">Primary Orchestrator</div>
						<div class="text-[10px] text-slate-500 font-mono">root:golem-agent</div>
					</div>
				</div>
				<span class="w-2 h-2 rounded-full bg-emerald-400 animate-pulse" title="Ready / Listening"></span>
			</div>
		</div>

		{#if subagents.length > 0}
			<div class="pl-3.5 border-l-2 border-slate-800/80 space-y-2 my-2">
				{#each subagents as agent (agent.id)}
					<div
						role="button"
						tabindex="0"
						onclick={() => onSelectAgent?.(agent)}
						onkeydown={(e) => e.key === 'Enter' && onSelectAgent?.(agent)}
						class={cn(
							"group p-2.5 rounded-lg border transition-all cursor-pointer",
							selectedAgentId === agent.id
								? "bg-purple-950/30 border-purple-500/50 shadow-sm"
								: "bg-slate-900/40 border-slate-800/80 hover:bg-slate-900/80 hover:border-slate-700"
						)}
					>
						<div class="flex items-center justify-between">
							<div class="flex items-center gap-2">
								<div class="p-1 rounded bg-purple-500/10 text-purple-400 border border-purple-500/20">
									<Cpu class="w-3 h-3" />
								</div>
								<div>
									<div class="font-medium text-slate-200 text-xs">{agent.name}</div>
									<div class="text-[10px] text-slate-400">{agent.role}</div>
								</div>
							</div>

							{#if agent.status === 'running'}
								<span class="flex items-center gap-1 text-[10px] text-amber-400 font-mono">
									<Clock class="w-2.5 h-2.5 animate-spin" />
									<span>exec</span>
								</span>
							{:else if agent.status === 'done'}
								<span class="flex items-center gap-1 text-[10px] text-emerald-400 font-mono">
									<CheckCircle2 class="w-2.5 h-2.5" />
									<span>done</span>
								</span>
							{:else if agent.status === 'error'}
								<span class="flex items-center gap-1 text-[10px] text-rose-400 font-mono">
									<AlertCircle class="w-2.5 h-2.5" />
									<span>err</span>
								</span>
							{/if}
						</div>

						{#if agent.currentAction}
							<div class="mt-2 text-[10px] text-slate-400 bg-slate-950/50 px-2 py-1 rounded border border-slate-800/60 font-mono truncate">
								❯ {agent.currentAction}
							</div>
						{/if}

						<div class="flex items-center justify-between mt-2 pt-1.5 border-t border-slate-800/50 text-[10px] text-slate-500 font-mono">
							<span>Tools: {agent.toolsRun}</span>
							<span>Tokens: {agent.tokensUsed}</span>
						</div>
					</div>
				{/each}
			</div>
		{:else}
			<div class="flex flex-col items-center justify-center p-6 text-center text-slate-500">
				<Network class="w-8 h-8 opacity-20 mb-2" />
				<p class="text-xs">No active subagents in swarm</p>
				<p class="text-[10px] text-slate-600 mt-1">Subagents spawned via task delegation will appear here in realtime.</p>
			</div>
		{/if}
	</div>

	<!-- Swarm Comms Chatter Feed Content -->
	<div class={cn("flex-1 overflow-y-auto p-2.5 space-y-2 no-scrollbar", activeTab !== 'chat' && "hidden")}>
		{#if swarmMessages.length > 0}
			{#each swarmMessages as msg (msg.id)}
				<div class="p-2.5 rounded-lg border border-slate-800 bg-slate-900/60 space-y-1.5 shadow-sm">
					<div class="flex items-center justify-between text-[10px] font-mono">
						<div class="flex items-center gap-1">
							<span class="text-purple-400 font-semibold">{msg.sender}</span>
							{#if msg.recipient}
								<span class="text-slate-600">➔</span>
								<span class="text-blue-400 font-semibold">{msg.recipient}</span>
							{:else if msg.channel}
								<span class="text-slate-600">in</span>
								<span class="text-emerald-400 font-semibold">#{msg.channel}</span>
							{/if}
						</div>
						<span class="text-slate-500">{new Date(msg.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</span>
					</div>

					{#if msg.subject}
						<div class="text-[11px] font-medium text-slate-200">{msg.subject}</div>
					{/if}

					<div class="text-xs text-slate-300 font-mono bg-slate-950/60 p-2 rounded border border-slate-800/80 whitespace-pre-wrap break-words">
						{msg.body}
					</div>
				</div>
			{/each}
		{:else}
			<div class="flex flex-col items-center justify-center p-6 text-center text-slate-500">
				<Radio class="w-8 h-8 opacity-20 mb-2" />
				<p class="text-xs">No chatter in swarm</p>
				<p class="text-[10px] text-slate-600 mt-1">Direct agent messages and channel broadcasts will stream here.</p>
			</div>
		{/if}
	</div>

	<!-- Workflow Graph DAG Tab Content -->
	<div class={cn("flex-1 overflow-y-auto p-2.5 space-y-2.5 no-scrollbar", activeTab !== 'dag' && "hidden")}>
		{#if graphRuns.length + graphSpecs.length > 1}
			<select
				bind:value={selectedGraphIdx}
				class="w-full px-2.5 py-1.5 rounded-lg bg-slate-900 border border-slate-800 text-xs text-slate-200 font-mono"
			>
				{#each graphRuns as r, idx}
					<option value={idx}>{r.graph} ({r.run_id}) [{r.status}]</option>
				{/each}
				{#each graphSpecs as s, idx}
					<option value={graphRuns.length + idx}>{s.name} [spec]</option>
				{/each}
			</select>
		{/if}

		{#if activeGraphItem && activeGraphItem.spec}
			{@const spec = activeGraphItem.spec}
			{@const run = activeGraphItem.run}
			<div class="p-2.5 rounded-lg border border-slate-800 bg-slate-900/60 space-y-1.5">
				<div class="flex items-center justify-between">
					<span class="font-semibold text-slate-100 font-mono text-xs">{spec.name}</span>
					{#if run}
						<span class={cn(
							"px-1.5 py-0.5 rounded text-[10px] font-mono uppercase",
							run.status === 'completed' ? "bg-emerald-500/20 text-emerald-300" :
							run.status === 'running' ? "bg-amber-500/20 text-amber-300 animate-pulse" :
							run.status === 'failed' ? "bg-rose-500/20 text-rose-300" :
							"bg-purple-500/20 text-purple-300"
						)}>{run.status}</span>
					{:else}
						<span class="px-1.5 py-0.5 rounded text-[10px] font-mono bg-slate-800 text-slate-400">SPEC</span>
					{/if}
				</div>
				{#if spec.description}
					<div class="text-[11px] text-slate-400">{spec.description}</div>
				{/if}
				{#if run}
					<div class="flex flex-wrap gap-2 pt-1 text-[10px] font-mono text-slate-400 border-t border-slate-800/60">
						<span>Tokens: {run.tokens || 0}</span>
						{#if run.cached_tokens}
							<span class="text-emerald-400">Cache: {Math.round((run.cache_hit_ratio || 0) * 100)}% ({run.cached_tokens})</span>
						{/if}
						{#if run.activations}
							<span>Visits: {run.activations}</span>
						{/if}
					</div>
					{#if run.error}
						<div class="text-[10px] font-mono text-rose-400 bg-rose-950/40 p-1.5 rounded border border-rose-500/30">
							{run.error}
						</div>
					{/if}
				{/if}
			</div>

			<div class="space-y-1.5">
				{#each spec.nodes as node (node.id)}
					{@const recNode = run?.nodes?.find((n) => n.id === node.id)}
					{@const live = liveNodeState[`${spec.name}/${node.id}`]}
					{@const status = live?.status || (recNode ? (recNode.error === 'did not run' ? 'pending' : recNode.error ? 'failed' : 'ok') : 'pending')}
					{@const visits = live?.activations || recNode?.activations || 0}
					{@const outEdges = (spec.edges || []).filter((e) => e[0] === node.id)}
					<div class={cn(
						"p-2 rounded-lg border font-mono text-[11px] space-y-1",
						status === 'running' ? "border-amber-500/50 bg-amber-950/20" :
						status === 'ok' ? "border-emerald-500/30 bg-emerald-950/10" :
						status === 'failed' ? "border-rose-500/40 bg-rose-950/20" :
						"border-slate-800 bg-slate-900/40"
					)}>
						<div class="flex items-center justify-between">
							<div class="flex items-center gap-1.5">
								{#if status === 'running'}
									<Clock class="w-3 h-3 text-amber-400 animate-spin" />
								{:else if status === 'ok'}
									<CheckCircle2 class="w-3 h-3 text-emerald-400" />
								{:else if status === 'failed'}
									<AlertCircle class="w-3 h-3 text-rose-400" />
								{:else}
									<span class="w-2.5 h-2.5 rounded-full border border-slate-600 inline-block"></span>
								{/if}
								<span class="text-slate-100 font-semibold">{node.id}</span>
								<span class="text-[10px] text-slate-400">[{node.role || node.kind}]</span>
							</div>
							{#if visits > 1}
								<span class="px-1 py-0.2 rounded bg-purple-500/20 text-purple-300 text-[9px]">x{visits}</span>
							{/if}
						</div>

						{#if outEdges.length > 0}
							<div class="flex flex-wrap gap-1 pt-0.5 text-[10px] text-slate-400">
								{#each outEdges as edge}
									<span class="px-1.5 py-0.2 rounded bg-slate-950/70 border border-slate-800">
										➔ {edge[1]}{edge[2] ? ` (${edge[2]})` : ''}
									</span>
								{/each}
							</div>
						{/if}

						{#if recNode?.error && recNode.error !== 'did not run'}
							<div class="text-[10px] text-rose-400 truncate">! {recNode.error}</div>
						{/if}
					</div>
				{/each}
			</div>
		{:else}
			<div class="flex flex-col items-center justify-center p-6 text-center text-slate-500">
				<GitFork class="w-8 h-8 opacity-20 mb-2" />
				<p class="text-xs">No workflow graphs loaded</p>
				<p class="text-[10px] text-slate-600 mt-1">Saved or running ADK workflow graphs render their live DAG topology here.</p>
			</div>
		{/if}
	</div>

	<!-- Resource Locks Tab Content -->
	<div class={cn("flex-1 overflow-y-auto p-2.5 space-y-2 no-scrollbar", activeTab !== 'locks' && "hidden")}>
		{#if locks.length > 0}
			{#each locks as lock (lock.resource)}
				<div class="p-2.5 rounded-lg border border-slate-800 bg-slate-900/60 space-y-1.5 shadow-sm">
					<div class="flex items-center gap-2">
						<Lock class="w-3 h-3 text-amber-400 shrink-0" />
						<span class="text-xs font-mono text-slate-200 truncate">{lock.resource}</span>
					</div>
					<div class="flex items-center justify-between text-[10px] font-mono">
						<span class="text-purple-400">{lock.owner}</span>
						{#if lock.expires_at}
							<span class="text-slate-500">expires {new Date(lock.expires_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}</span>
						{:else}
							<span class="text-slate-500">held</span>
						{/if}
					</div>
				</div>
			{/each}
		{:else}
			<div class="flex flex-col items-center justify-center p-6 text-center text-slate-500">
				<Lock class="w-8 h-8 opacity-20 mb-2" />
				<p class="text-xs">No advisory locks held</p>
				<p class="text-[10px] text-slate-600 mt-1">claim_resource locks appear here so parallel agents avoid clobbering the same file.</p>
			</div>
		{/if}
	</div>
</div>
