<script lang="ts">
	import { GitPullRequest, Check, X, FileCode, ArrowRight, ShieldCheck, AlertCircle } from '@lucide/svelte';
	import type { PatchProposal } from '$lib/types';
	import { cn } from '$lib/utils';

	interface Props {
		patches: PatchProposal[];
		onClose: () => void;
		onApplyPatch: (patch: PatchProposal) => Promise<void>;
		onRejectPatch: (patch: PatchProposal) => void;
	}

	let { patches, onClose, onApplyPatch, onRejectPatch }: Props = $props();

	let applyingPatches = $state<Set<string>>(new Set());
	let patchError = $state<string | null>(null);

	function patchKey(patch: PatchProposal): string {
		return `${patch.filePath}:${patch.anchor}:${patch.op}`;
	}

	async function handleApply(patch: PatchProposal) {
		const key = patchKey(patch);
		applyingPatches = new Set(applyingPatches).add(key);
		patchError = null;
		try {
			await onApplyPatch(patch);
		} catch (err: any) {
			patchError = err?.message || 'Failed to apply patch';
		} finally {
			const next = new Set(applyingPatches);
			next.delete(key);
			applyingPatches = next;
		}
	}
</script>

<div class="fixed inset-0 w-full md:inset-y-0 md:left-auto md:right-0 md:w-[540px] bg-[#090d16] border-l border-slate-800 shadow-2xl z-50 flex flex-col text-xs text-slate-200">
	<!-- Top Bar -->
	<div class="flex items-center justify-between px-4 py-3 border-b border-slate-800 bg-slate-950/50">
		<div class="flex items-center gap-2">
			<GitPullRequest class="w-4 h-4 text-blue-400" />
			<span class="font-semibold text-slate-100 text-sm">Hashline Patch Inspector</span>
			<span class="px-2 py-0.5 rounded text-[10px] bg-blue-500/10 text-blue-400 border border-blue-500/20 font-mono">
				{patches.length} Proposed
			</span>
		</div>
		<button
			type="button"
			onclick={onClose}
			class="p-2 min-h-[44px] min-w-[44px] md:min-h-0 md:min-w-0 md:p-1 flex items-center justify-center rounded hover:bg-slate-800 text-slate-400 hover:text-slate-200"
			title="Close inspector"
		>
			<X class="w-4 h-4" />
		</button>
	</div>

	<!-- Error Alert if patch application fails -->
	{#if patchError}
		<div class="m-3 p-2.5 rounded-lg bg-rose-500/10 border border-rose-500/30 flex items-center gap-2 text-rose-300">
			<AlertCircle class="w-4 h-4 flex-shrink-0" />
			<span class="text-[11px] font-mono">{patchError}</span>
		</div>
	{/if}

	<!-- Patch List -->
	<div class="flex-1 overflow-y-auto p-3 space-y-3 no-scrollbar">
		{#if patches.length === 0}
			<div class="flex flex-col items-center justify-center p-8 text-center text-slate-500">
				<ShieldCheck class="w-8 h-8 opacity-20 mb-2" />
				<p>No pending hashline patch proposals</p>
				<p class="text-[10px] text-slate-600 mt-1">Patches proposed by Golem tools appear here for review and application.</p>
			</div>
		{/if}

		{#each patches as patch (patch.anchor + patch.filePath)}
			<div class="p-3 rounded-lg border border-slate-800 bg-slate-900/50 space-y-2.5 shadow-sm">
				<div class="flex items-center justify-between">
					<div class="flex items-center gap-2">
						<FileCode class="w-3.5 h-3.5 text-blue-400" />
						<span class="font-medium text-slate-100 font-mono text-[11px]">{patch.filePath}</span>
					</div>
					<div class="flex items-center gap-1.5 font-mono text-[10px]">
						<span class="px-1.5 py-0.5 rounded bg-blue-500/10 text-blue-400 border border-blue-500/20 font-semibold">
							{patch.op}
						</span>
						<span class="px-1.5 py-0.5 rounded bg-slate-800 text-slate-300 border border-slate-700">
							anchor: {patch.anchor}
						</span>
					</div>
				</div>

				<!-- Diff Code Viewer -->
				<div class="rounded border border-slate-800/80 bg-slate-950 font-mono text-[11px] overflow-x-auto">
					{#if patch.originalLines && patch.originalLines.length > 0}
						{#each patch.originalLines as line}
							<div class="px-2.5 py-0.5 bg-rose-500/10 text-rose-300 border-l-2 border-rose-500">
								- {line}
							</div>
						{/each}
					{/if}
					{#if patch.content}
						{#each patch.content.split('\n') as line}
							<div class="px-2.5 py-0.5 bg-emerald-500/10 text-emerald-300 border-l-2 border-emerald-500 whitespace-pre-wrap">
								+ {line}
							</div>
						{/each}
					{/if}
				</div>

				<!-- Actions -->
				<div class="flex items-center justify-end gap-2 pt-1 border-t border-slate-800/60">
					<button
						type="button"
						onclick={() => onRejectPatch(patch)}
						class="flex items-center gap-1 px-2.5 py-1 rounded bg-slate-800 hover:bg-slate-700 text-slate-300 font-medium text-xs transition-colors"
					>
						<X class="w-3 h-3" />
						<span>Reject</span>
					</button>

					<button
						type="button"
						disabled={applyingPatches.has(patchKey(patch))}
						onclick={() => handleApply(patch)}
						class="flex items-center gap-1 px-3 py-1 rounded bg-emerald-600 hover:bg-emerald-500 disabled:bg-slate-800 disabled:text-slate-500 text-white font-medium text-xs transition-colors shadow-sm"
					>
						<Check class="w-3 h-3" />
						<span>{applyingPatches.has(patchKey(patch)) ? 'Applying...' : 'Approve & Apply'}</span>
					</button>
				</div>
			</div>
		{/each}
	</div>
</div>
