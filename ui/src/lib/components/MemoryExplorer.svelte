<script lang="ts">
	import { Database, Search, ZoomIn, Calendar, Hash, ArrowUpDown, RefreshCw, X } from '@lucide/svelte';
	import type { MemoryItem } from '$lib/types';
	import { cn } from '$lib/utils';

	interface Props {
		memoryItems: MemoryItem[];
		onClose: () => void;
		onRefresh?: () => void;
	}

	let { memoryItems, onClose, onRefresh }: Props = $props();

	let searchQuery = $state('');
	let selectedKind = $state<string>('all');
	let zoomResolution = $state<number>(1); // 1 = Verbatim, 10 = Mid, 100 = Macro
	let selectedItem = $state<MemoryItem | null>(null);

	let maxRenderCount = $state<number>(100);

	let filteredItems = $derived.by(() => {
		// 1. Filter by kind and search query
		let items = memoryItems.filter((item) => {
			const matchesKind = selectedKind === 'all' || item.kind === selectedKind;
			const matchesSearch =
				searchQuery === '' ||
				item.text.toLowerCase().includes(searchQuery.toLowerCase()) ||
				String(item.id).includes(searchQuery);
			return matchesKind && matchesSearch;
		});

		// 2. Multi-resolution fractal zoom levels:
		// 100x Macro: only high-level user prompts and work milestones
		// 10x Mid: user, talk, and work summaries (suppress noisy tool/echo bytes)
		// 1x Verbatim: full event stream
		if (zoomResolution === 100) {
			items = items.filter((item) => item.kind === 'user' || item.kind === 'work');
		} else if (zoomResolution === 10) {
			items = items.filter((item) => item.kind === 'user' || item.kind === 'talk' || item.kind === 'work');
		}

		return items;
	});

	let visibleItems = $derived(filteredItems.slice(0, maxRenderCount));

	function getKindColor(kind: string) {
		switch (kind) {
			case 'user':
				return 'bg-blue-500/10 text-blue-400 border-blue-500/20';
			case 'talk':
				return 'bg-emerald-500/10 text-emerald-400 border-emerald-500/20';
			case 'tool':
				return 'bg-amber-500/10 text-amber-400 border-amber-500/20';
			case 'echo':
				return 'bg-slate-500/10 text-slate-400 border-slate-500/20';
			case 'work':
				return 'bg-purple-500/10 text-purple-400 border-purple-500/20';
			default:
				return 'bg-slate-500/10 text-slate-400 border-slate-500/20';
		}
	}
</script>

<div class="fixed inset-y-0 right-0 w-[520px] bg-[#090d16] border-l border-slate-800 shadow-2xl z-50 flex flex-col text-xs text-slate-200">
	<!-- Top Bar -->
	<div class="flex items-center justify-between px-4 py-3 border-b border-slate-800 bg-slate-950/50">
		<div class="flex items-center gap-2">
			<Database class="w-4 h-4 text-emerald-400" />
			<span class="font-semibold text-slate-100 text-sm">Fractal Memory Explorer</span>
			<span class="px-2 py-0.5 rounded text-[10px] bg-emerald-500/10 text-emerald-400 border border-emerald-500/20 font-mono">
				{memoryItems.length} Events (bbolt)
			</span>
		</div>
		<div class="flex items-center gap-1">
			{#if onRefresh}
				<button
					type="button"
					onclick={onRefresh}
					class="p-1 rounded hover:bg-slate-800 text-slate-400 hover:text-slate-200"
					title="Refresh events from bbolt"
				>
					<RefreshCw class="w-3.5 h-3.5" />
				</button>
			{/if}
			<button
				type="button"
				onclick={onClose}
				class="p-1 rounded hover:bg-slate-800 text-slate-400 hover:text-slate-200"
				title="Close explorer"
			>
				<X class="w-4 h-4" />
			</button>
		</div>
	</div>

	<!-- Controls & Resolution Slider -->
	<div class="p-3 border-b border-slate-800/80 bg-slate-900/30 space-y-2.5">
		<!-- Search & Kind Filter -->
		<div class="flex items-center gap-2">
			<div class="relative flex-1">
				<Search class="w-3.5 h-3.5 text-slate-500 absolute left-2.5 top-2" />
				<input
					type="text"
					bind:value={searchQuery}
					placeholder="Search events, IDs, or text..."
					class="w-full pl-8 pr-3 py-1.5 bg-slate-950 border border-slate-800 rounded-md text-xs placeholder:text-slate-500 text-slate-200 outline-none focus:border-blue-500/50"
				/>
			</div>

			<select
				bind:value={selectedKind}
				class="bg-slate-950 border border-slate-800 rounded-md px-2 py-1.5 text-xs text-slate-300 outline-none focus:border-blue-500/50"
			>
				<option value="all">All Kinds</option>
				<option value="user">User</option>
				<option value="talk">Talk</option>
				<option value="tool">Tool</option>
				<option value="echo">Echo</option>
				<option value="work">Work</option>
			</select>
		</div>

		<!-- Resolution Zoom Slider -->
		<div class="flex items-center justify-between bg-slate-950/60 px-3 py-2 rounded-lg border border-slate-800/80">
			<div class="flex items-center gap-2">
				<ZoomIn class="w-3.5 h-3.5 text-blue-400" />
				<span class="text-[11px] font-medium text-slate-400">Resolution:</span>
			</div>
			<div class="flex items-center gap-1.5">
				{#each [1, 10, 100] as res}
					<button
						type="button"
						onclick={() => (zoomResolution = res)}
						class={cn(
							"px-2 py-0.5 rounded text-[10px] font-mono border transition-all",
							zoomResolution === res
								? "bg-blue-600 text-white border-blue-500 shadow-sm font-semibold"
								: "bg-slate-900 text-slate-400 border-slate-800 hover:bg-slate-800"
						)}
					>
						{res}x {res === 1 ? '(Verbatim)' : res === 10 ? '(Sub-tree)' : '(Macro)'}
					</button>
				{/each}
			</div>
		</div>
	</div>

	<!-- Event Timeline List -->
	<div class="flex-1 overflow-y-auto p-3 space-y-2 no-scrollbar">
		{#if filteredItems.length === 0}
			<div class="flex flex-col items-center justify-center p-8 text-center text-slate-500">
				<Database class="w-8 h-8 opacity-20 mb-2" />
				<p>No events recorded in this scope</p>
			</div>
		{/if}

		{#each visibleItems as item (item.id)}
			<div
				role="button"
				tabindex="0"
				onclick={() => (selectedItem = item)}
				onkeydown={(e) => e.key === 'Enter' && (selectedItem = item)}
				class={cn(
					"p-2.5 rounded-lg border transition-all cursor-pointer text-left",
					selectedItem?.id === item.id
						? "bg-emerald-950/20 border-emerald-500/50 shadow-sm"
						: "bg-slate-900/40 border-slate-800/80 hover:bg-slate-900/80 hover:border-slate-700"
				)}
			>
				<div class="flex items-center justify-between mb-1.5">
					<div class="flex items-center gap-1.5">
						<span class="px-1.5 py-0.5 rounded font-mono text-[10px] font-semibold border {getKindColor(item.kind)}">
							{item.kind}
						</span>
						<span class="font-mono text-[10px] text-slate-500">#{item.id}</span>
					</div>
					<div class="flex items-center gap-1 text-[10px] text-slate-500 font-mono">
						<Calendar class="w-3 h-3 text-slate-600" />
						<span>{new Date(item.timestamp).toLocaleTimeString()}</span>
						<span>• {item.size} B</span>
					</div>
				</div>

				<div class="text-xs text-slate-300 font-mono whitespace-pre-wrap line-clamp-3 leading-relaxed">
					{item.text}
				</div>
			</div>
		{/each}

		{#if filteredItems.length > maxRenderCount}
			<div class="pt-2 text-center">
				<button
					type="button"
					onclick={() => (maxRenderCount += 100)}
					class="px-3 py-1.5 rounded-lg bg-slate-900 hover:bg-slate-800 border border-slate-800 text-xs text-slate-400 hover:text-slate-200 transition-colors"
				>
					Load more events ({filteredItems.length - maxRenderCount} remaining)
				</button>
			</div>
		{/if}
	</div>

	<!-- Event Drill-Down Inspector Modal / Drawer Bottom -->
	{#if selectedItem}
		<div class="border-t border-slate-800 bg-slate-950 p-3 space-y-2">
			<div class="flex items-center justify-between text-xs font-semibold">
				<div class="flex items-center gap-1.5">
					<Hash class="w-3.5 h-3.5 text-blue-400" />
					<span>Event Details #{selectedItem.id} ({selectedItem.kind})</span>
				</div>
				<button
					type="button"
					onclick={() => (selectedItem = null)}
					class="text-slate-400 hover:text-white"
				>
					<X class="w-3.5 h-3.5" />
				</button>
			</div>
			<pre class="p-2.5 rounded-lg bg-slate-900 border border-slate-800 text-[11px] font-mono text-slate-300 max-h-48 overflow-y-auto whitespace-pre-wrap">{selectedItem.text}</pre>
		</div>
	{/if}
</div>
