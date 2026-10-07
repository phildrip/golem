<script lang="ts">
	import { Plus, X, Terminal, Cpu } from '@lucide/svelte';
	import type { SessionInfo } from '$lib/types';
	import { cn } from '$lib/utils';

	interface Props {
		sessions: SessionInfo[];
		activeId: string | null;
		onSelect: (id: string) => void;
		onCreate: () => void;
		onClose: (id: string) => void;
		onOpenImport?: () => void;
	}

	let { sessions, activeId, onSelect, onCreate, onClose, onOpenImport }: Props = $props();
</script>

<div class="flex items-center h-10 bg-[#090d16] border-b border-slate-800/80 px-2 gap-1 overflow-x-auto select-none no-scrollbar">
	<div class="flex items-center gap-1.5 px-3 py-1 mr-2 text-xs font-semibold text-slate-400">
		<Terminal class="w-3.5 h-3.5 text-blue-400" />
		<span>Sessions</span>
	</div>

	{#each sessions as sess (sess.id)}
		<div
			role="button"
			tabindex="0"
			onclick={() => onSelect(sess.id)}
			onkeydown={(e) => e.key === 'Enter' && onSelect(sess.id)}
			class={cn(
				'group relative flex items-center gap-2 h-7 px-3 text-xs rounded-md border transition-all cursor-pointer whitespace-nowrap',
				activeId === sess.id
					? 'bg-slate-900 border-slate-700 text-slate-100 font-medium shadow-sm'
					: 'bg-transparent border-transparent text-slate-400 hover:bg-slate-900/50 hover:text-slate-200'
			)}
		>
			<span class={cn("w-1.5 h-1.5 rounded-full", activeId === sess.id ? "bg-emerald-400 animate-pulse" : "bg-slate-600")}></span>
			<span class="max-w-[130px] truncate">{sess.title || `Session ${sess.id.slice(0, 6)}`}</span>

			<button
				type="button"
				class="opacity-0 group-hover:opacity-100 hover:bg-slate-800 rounded p-0.5 text-slate-400 hover:text-slate-200 transition-opacity ml-1"
				onclick={(e) => {
					e.stopPropagation();
					onClose(sess.id);
				}}
				title="Close session"
			>
				<X class="w-3 h-3" />
			</button>
		</div>
	{/each}

	<button
		type="button"
		onclick={onCreate}
		class="flex items-center gap-1 h-7 px-2 text-xs font-medium text-slate-400 hover:text-slate-200 hover:bg-slate-800/50 rounded-md transition-colors ml-1 border border-dashed border-slate-800 hover:border-slate-700"
		title="New agent session"
	>
		<Plus class="w-3.5 h-3.5" />
		<span>New</span>
	</button>

	{#if onOpenImport}
		<button
			type="button"
			onclick={onOpenImport}
			class="flex items-center gap-1.5 h-7 px-2.5 text-xs font-medium text-emerald-400 hover:text-emerald-300 hover:bg-emerald-950/30 rounded-md transition-colors border border-emerald-900/60 hover:border-emerald-800/80 ml-1"
			title="Import chat history from AGY or Cursor"
		>
			<Cpu class="w-3.5 h-3.5 text-emerald-400" />
			<span>Import</span>
		</button>
	{/if}
</div>
