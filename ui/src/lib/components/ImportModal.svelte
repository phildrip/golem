<script lang="ts">
	import { onMount } from 'svelte';
	import { X, Search, Database, ArrowRight, Loader2, Sparkles, RefreshCw, Folder } from '@lucide/svelte';
	import type { DiscoveredSession, SessionInfo } from '$lib/types';
	import { discoverHistories, importSession } from '$lib/client';
	import { cn } from '$lib/utils';

	interface Props {
		open: boolean;
		onClose: () => void;
		onImported: (session: SessionInfo) => void;
	}

	let { open, onClose, onImported }: Props = $props();

	let sessions = $state<DiscoveredSession[]>([]);
	let loading = $state<boolean>(false);
	let importingId = $state<string | null>(null);
	let searchQuery = $state<string>('');
	let selectedFilter = $state<'all' | 'agy' | 'cursor'>('all');
	let error = $state<string | null>(null);

	async function loadSessions() {
		loading = true;
		error = null;
		try {
			sessions = await discoverHistories();
		} catch (err: any) {
			error = err?.message || 'Failed to discover agent histories';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (open) {
			loadSessions();
		}
	});

	let filteredSessions = $derived(
		sessions.filter((s) => {
			if (selectedFilter !== 'all' && s.format !== selectedFilter) {
				return false;
			}
			if (!searchQuery.trim()) return true;
			const q = searchQuery.toLowerCase();
			return (
				s.title.toLowerCase().includes(q) ||
				s.id.toLowerCase().includes(q) ||
				(s.workspace && s.workspace.toLowerCase().includes(q))
			);
		})
	);

	async function handleImport(session: DiscoveredSession) {
		if (importingId !== null) return;
		importingId = session.id;
		error = null;
		try {
			const created = await importSession(session.path, session.id, session.title);
			onImported(created);
			onClose();
		} catch (err: any) {
			error = err?.message || 'Failed to import session';
		} finally {
			importingId = null;
		}
	}

	function formatRelativeTime(dateStr: string): string {
		try {
			const d = new Date(dateStr);
			if (isNaN(d.getTime())) return dateStr;
			const diff = (Date.now() - d.getTime()) / 1000;
			if (diff < 60) return 'Just now';
			if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
			if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
			return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
		} catch {
			return dateStr;
		}
	}
</script>

{#if open}
	<div class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/70 backdrop-blur-sm animate-in fade-in-0 duration-150">
		<div class="relative w-full max-w-2xl bg-slate-950 border border-slate-800 rounded-xl shadow-2xl flex flex-col max-h-[85vh] overflow-hidden">
			<!-- Header -->
			<div class="flex items-center justify-between px-5 py-4 border-b border-slate-800 bg-slate-900/40">
				<div class="flex items-center gap-2.5">
					<div class="p-1.5 rounded-lg bg-blue-500/10 border border-blue-500/20 text-blue-400">
						<Sparkles class="w-4 h-4" />
					</div>
					<div>
						<h2 class="text-sm font-semibold text-slate-100">Import Past Agent History</h2>
						<p class="text-xs text-slate-400">Discovered from local Antigravity (AGY) and Cursor workspaces</p>
					</div>
				</div>
				<div class="flex items-center gap-1.5">
					<button
						type="button"
						onclick={loadSessions}
						disabled={loading}
						class="p-1.5 rounded-md hover:bg-slate-800 text-slate-400 hover:text-slate-200 transition-colors disabled:opacity-50"
						title="Rescan local environments"
					>
						<RefreshCw class={cn("w-4 h-4", loading && "animate-spin")} />
					</button>
					<button
						type="button"
						onclick={onClose}
						class="p-1.5 rounded-md hover:bg-slate-800 text-slate-400 hover:text-slate-200 transition-colors"
						title="Close modal"
					>
						<X class="w-4 h-4" />
					</button>
				</div>
			</div>

			<!-- Filter & Search Toolbar -->
			<div class="p-3 border-b border-slate-800/80 bg-slate-900/20 flex flex-wrap items-center gap-2">
				<div class="relative flex-1 min-w-[200px]">
					<Search class="w-3.5 h-3.5 text-slate-500 absolute left-2.5 top-2.5" />
					<input
						type="text"
						bind:value={searchQuery}
						placeholder="Search by title, ID, or workspace..."
						class="w-full pl-8 pr-3 py-1.5 bg-slate-900/60 border border-slate-800 rounded-md text-xs placeholder:text-slate-500 text-slate-200 outline-none focus:border-blue-500/50"
					/>
				</div>

				<div class="flex items-center gap-1 bg-slate-900/80 p-0.5 rounded-md border border-slate-800 text-xs">
					<button
						type="button"
						onclick={() => (selectedFilter = 'all')}
						class={cn(
							"px-2.5 py-1 rounded text-xs transition-colors",
							selectedFilter === 'all' ? "bg-slate-800 text-slate-100 font-medium" : "text-slate-400 hover:text-slate-200"
						)}
					>
						All ({sessions.length})
					</button>
					<button
						type="button"
						onclick={() => (selectedFilter = 'agy')}
						class={cn(
							"px-2.5 py-1 rounded text-xs transition-colors",
							selectedFilter === 'agy' ? "bg-purple-900/40 text-purple-200 font-medium border border-purple-700/50" : "text-slate-400 hover:text-slate-200"
						)}
					>
						AGY ({sessions.filter((s) => s.format === 'agy').length})
					</button>
					<button
						type="button"
						onclick={() => (selectedFilter = 'cursor')}
						class={cn(
							"px-2.5 py-1 rounded text-xs transition-colors",
							selectedFilter === 'cursor' ? "bg-blue-900/40 text-blue-200 font-medium border border-blue-700/50" : "text-slate-400 hover:text-slate-200"
						)}
					>
						Cursor ({sessions.filter((s) => s.format === 'cursor').length})
					</button>
				</div>
			</div>

			<!-- Error Alert -->
			{#if error}
				<div class="px-4 py-2 bg-red-950/40 border-b border-red-800/50 text-red-300 text-xs flex items-center justify-between">
					<span>{error}</span>
					<button type="button" onclick={() => (error = null)} class="text-red-400 hover:text-red-200">
						<X class="w-3.5 h-3.5" />
					</button>
				</div>
			{/if}

			<!-- Sessions List -->
			<div class="flex-1 overflow-y-auto p-4 space-y-2.5 no-scrollbar">
				{#if loading && sessions.length === 0}
					<div class="flex flex-col items-center justify-center py-12 text-slate-500 gap-2">
						<Loader2 class="w-6 h-6 animate-spin text-blue-400" />
						<p class="text-xs">Scanning local environments for histories...</p>
					</div>
				{:else if filteredSessions.length === 0}
					<div class="flex flex-col items-center justify-center py-12 text-slate-500 gap-2">
						<Database class="w-8 h-8 opacity-20" />
						<p class="text-xs">No matching agent histories found</p>
					</div>
				{:else}
					{#each filteredSessions as s (s.id)}
						<div
							class={cn(
								"p-3 rounded-lg border transition-all flex items-center justify-between gap-3 text-left",
								s.matches_workspace
									? "bg-slate-900/60 border-emerald-500/30 hover:border-emerald-500/50"
									: "bg-slate-900/30 border-slate-800/80 hover:bg-slate-900/70 hover:border-slate-700"
							)}
						>
							<div class="flex-1 min-w-0">
								<div class="flex items-center gap-2 mb-1 flex-wrap">
									<span
										class={cn(
											"px-1.5 py-0.5 rounded text-[10px] font-mono font-semibold uppercase border",
											s.format === 'agy'
												? "bg-purple-950/60 border-purple-500/40 text-purple-300"
												: "bg-cyan-950/60 border-cyan-500/40 text-cyan-300"
										)}
									>
										{s.format}
									</span>

									{#if s.matches_workspace}
										<span class="px-1.5 py-0.5 rounded text-[10px] font-semibold bg-emerald-950/60 border border-emerald-500/40 text-emerald-300">
											Current Workspace
										</span>
									{/if}

									<span class="text-[11px] text-slate-400 font-mono">
										{formatRelativeTime(s.updated_at)}
									</span>

									{#if s.event_count > 0}
										<span class="text-[10px] text-slate-500 font-mono">
											{s.event_count} events
										</span>
									{/if}
								</div>

								<h3 class="text-xs font-medium text-slate-200 truncate">{s.title}</h3>

								{#if s.workspace}
									<p class="text-[10px] text-slate-500 truncate flex items-center gap-1 mt-0.5 font-mono">
										<Folder class="w-3 h-3 text-slate-600 inline" />
										{s.workspace}
									</p>
								{/if}
							</div>

							<button
								type="button"
								onclick={() => handleImport(s)}
								disabled={importingId !== null}
								class={cn(
									"flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs font-medium transition-all shrink-0",
									s.matches_workspace
										? "bg-emerald-600 hover:bg-emerald-500 text-white shadow-sm"
										: "bg-slate-800 hover:bg-slate-700 text-slate-200",
									importingId === s.id && "opacity-75 cursor-wait"
								)}
							>
								{#if importingId === s.id}
									<Loader2 class="w-3 h-3 animate-spin" />
									<span>Importing...</span>
								{:else}
									<span>Import</span>
									<ArrowRight class="w-3 h-3" />
								{/if}
							</button>
						</div>
					{/each}
				{/if}
			</div>

			<!-- Footer -->
			<div class="px-5 py-3 border-t border-slate-800/80 bg-slate-900/40 flex items-center justify-between text-xs text-slate-500">
				<span>All historical messages & timestamps will be converted into fractal memory</span>
				<button
					type="button"
					onclick={onClose}
					class="px-3 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 transition-colors"
				>
					Cancel
				</button>
			</div>
		</div>
	</div>
{/if}
