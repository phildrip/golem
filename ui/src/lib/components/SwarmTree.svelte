<script lang="ts">
	import { Network, Bot, Cpu, CheckCircle2, AlertCircle, Clock, ChevronRight } from '@lucide/svelte';
	import type { SubagentNode } from '$lib/types';
	import { cn } from '$lib/utils';

	interface Props {
		subagents: SubagentNode[];
		selectedAgentId?: string | null;
		onSelectAgent?: (agent: SubagentNode) => void;
	}

	let { subagents, selectedAgentId = null, onSelectAgent }: Props = $props();
</script>

<div class="flex flex-col h-full bg-[#0a0e17] border-l border-slate-800/80 text-xs text-slate-300 w-80">
	<div class="flex items-center justify-between px-3.5 py-2.5 border-b border-slate-800/80 bg-slate-950/40">
		<div class="flex items-center gap-2 font-semibold text-slate-200">
			<Network class="w-4 h-4 text-purple-400" />
			<span>Swarm Hierarchy</span>
		</div>
		<span class="px-1.5 py-0.5 rounded text-[10px] font-mono bg-purple-500/10 text-purple-400 border border-purple-500/20">
			{subagents.length} Active
		</span>
	</div>

	<div class="flex-1 overflow-y-auto p-2.5 space-y-2 no-scrollbar">
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
</div>
