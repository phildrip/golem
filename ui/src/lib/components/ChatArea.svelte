<script lang="ts">
	import { Bot, User, Wrench, ChevronDown, ChevronRight, CheckCircle2, AlertCircle, Loader2 } from '@lucide/svelte';
	import type { ChatMessage, ToolCall } from '$lib/types';
	import { cn } from '$lib/utils';

	interface Props {
		messages: ChatMessage[];
		isGenerating?: boolean;
	}

	let { messages, isGenerating = false }: Props = $props();

	let expandedThoughts = $state<Record<string, boolean>>({});
	let expandedTools = $state<Record<string, boolean>>({});

	function toggleThought(id: string) {
		expandedThoughts[id] = !expandedThoughts[id];
	}

	function toggleTool(id: string) {
		expandedTools[id] = !expandedTools[id];
	}
</script>

<div class="flex-1 overflow-y-auto p-4 space-y-4 no-scrollbar">
	{#if messages.length === 0}
		<div class="h-full flex flex-col items-center justify-center text-center p-8 text-slate-500">
			<Bot class="w-12 h-12 text-blue-500/30 mb-3" />
			<h3 class="text-base font-semibold text-slate-300">Ready for instructions</h3>
			<p class="text-xs text-slate-500 max-w-sm mt-1">
				Send a message to start an autonomous session. Agents coordinate tools, memory, and subagents.
			</p>
		</div>
	{/if}

	{#each messages as msg (msg.id)}
		<div class={cn("flex flex-col gap-2 max-w-4xl mx-auto", msg.role === 'user' ? "items-end" : "items-start")}>
			<div class="flex items-center gap-2 text-[10px] text-slate-500 font-mono px-1">
				{#if msg.role === 'user'}
					<span>User</span>
					<div class="p-0.5 rounded bg-blue-500/20 text-blue-400">
						<User class="w-3 h-3" />
					</div>
				{:else}
					<div class="p-0.5 rounded bg-emerald-500/20 text-emerald-400">
						<Bot class="w-3 h-3" />
					</div>
					<span>Golem Agent</span>
				{/if}
				<span>•</span>
				<span>{new Date(msg.timestamp).toLocaleTimeString()}</span>
			</div>

			<!-- Thinking Collapsible -->
			{#if msg.thinking}
				<div class="w-full rounded-lg border border-slate-800/80 bg-slate-950/40 text-xs overflow-hidden">
					<button
						type="button"
						onclick={() => toggleThought(msg.id)}
						class="w-full flex items-center justify-between px-3 py-1.5 bg-slate-900/50 hover:bg-slate-900 text-slate-400 text-left"
					>
						<span class="flex items-center gap-1.5 font-mono text-[11px]">
							💭 Chain of Thought
						</span>
						{#if expandedThoughts[msg.id]}
							<ChevronDown class="w-3.5 h-3.5" />
						{:else}
							<ChevronRight class="w-3.5 h-3.5" />
						{/if}
					</button>

					{#if expandedThoughts[msg.id]}
						<div class="p-3 text-slate-400 font-mono text-[11px] whitespace-pre-wrap leading-relaxed border-t border-slate-800/60 bg-slate-950/80">
							{msg.thinking}
						</div>
					{/if}
				</div>
			{/if}

			<!-- Tool Calls -->
			{#if msg.toolCalls && msg.toolCalls.length > 0}
				<div class="w-full space-y-1.5">
					{#each msg.toolCalls as tool (tool.id)}
						<div class="rounded-lg border border-slate-800/80 bg-slate-950/60 overflow-hidden text-xs">
							<button
								type="button"
								onclick={() => toggleTool(tool.id)}
								class="w-full flex items-center justify-between px-3 py-2 bg-slate-900/60 hover:bg-slate-900 transition-colors"
							>
								<div class="flex items-center gap-2 font-mono text-xs">
									<Wrench class="w-3.5 h-3.5 text-amber-400" />
									<span class="font-medium text-slate-200">{tool.toolName}</span>
								</div>

								<div class="flex items-center gap-2">
									{#if tool.status === 'running'}
										<span class="flex items-center gap-1 text-[10px] text-amber-400 font-mono">
											<Loader2 class="w-3 h-3 animate-spin" />
											<span>executing</span>
										</span>
									{:else if tool.status === 'done'}
										<span class="flex items-center gap-1 text-[10px] text-emerald-400 font-mono">
											<CheckCircle2 class="w-3 h-3" />
											{#if tool.durationMs}<span>{tool.durationMs}ms</span>{/if}
										</span>
									{:else}
										<span class="flex items-center gap-1 text-[10px] text-rose-400 font-mono">
											<AlertCircle class="w-3 h-3" />
											<span>error</span>
										</span>
									{/if}

									{#if expandedTools[tool.id]}
										<ChevronDown class="w-3.5 h-3.5 text-slate-500" />
									{:else}
										<ChevronRight class="w-3.5 h-3.5 text-slate-500" />
									{/if}
								</div>
							</button>

							{#if expandedTools[tool.id]}
								<div class="p-3 bg-slate-950/90 border-t border-slate-800/80 font-mono text-[11px] space-y-2">
									<div>
										<div class="text-[10px] text-slate-500 uppercase tracking-wider mb-1">Arguments</div>
										<pre class="p-2 rounded bg-slate-900 border border-slate-800 text-slate-300 overflow-x-auto">{JSON.stringify(tool.args, null, 2)}</pre>
									</div>
									{#if tool.output}
										<div>
											<div class="text-[10px] text-slate-500 uppercase tracking-wider mb-1">Result</div>
											<pre class="p-2 rounded bg-slate-900 border border-slate-800 text-slate-300 overflow-x-auto whitespace-pre-wrap">{tool.output}</pre>
										</div>
									{/if}
								</div>
							{/if}
						</div>
					{/each}
				</div>
			{/if}

			<!-- Message Content Body -->
			{#if msg.content}
				<div class={cn(
					"rounded-xl px-4 py-3 text-sm leading-relaxed max-w-2xl whitespace-pre-wrap shadow-sm",
					msg.role === 'user'
						? "bg-blue-600 text-white rounded-br-sm"
						: "bg-slate-900 border border-slate-800/80 text-slate-200 rounded-bl-sm"
				)}>
					{msg.content}
				</div>
			{/if}
		</div>
	{/each}

	{#if isGenerating}
		<div class="flex items-center gap-2 text-xs text-slate-400 font-mono max-w-4xl mx-auto pl-1">
			<Loader2 class="w-3.5 h-3.5 animate-spin text-blue-400" />
			<span>Golem is thinking...</span>
		</div>
	{/if}
</div>
