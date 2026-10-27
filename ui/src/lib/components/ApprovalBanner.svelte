<script lang="ts">
	import { ShieldAlert } from '@lucide/svelte';
	import type { ApprovalPrompt } from '$lib/types';

	let {
		prompt,
		onApprove,
		onDeny
	}: {
		prompt: ApprovalPrompt;
		onApprove: () => void;
		onDeny: () => void;
	} = $props();
</script>

<!--
	The sandbox stopped a turn: a tool call is waiting for a human. The banner
	shows the exact command or patch, because that is what is being approved.
-->
<div class="mx-4 mb-2 rounded-lg border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
	<div class="flex items-start gap-2">
		<ShieldAlert size={18} class="mt-0.5 flex-shrink-0 text-amber-400" />
		<div class="min-w-0 flex-1">
			<div class="font-medium text-amber-200">
				Approve this {prompt.tool || 'tool'} call?
			</div>
			<pre class="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-black/40 p-2 text-xs text-slate-200">{prompt.action || prompt.hint}</pre>
			{#if prompt.reason}
				<div class="mt-1 text-xs text-slate-400">why: {prompt.reason}</div>
			{/if}
		</div>
		<div class="flex flex-shrink-0 flex-col gap-2 sm:flex-row">
			<button
				type="button"
				class="rounded bg-emerald-600 px-3 py-1 text-xs font-medium text-white hover:bg-emerald-500"
				onclick={onApprove}
			>
				Run it
			</button>
			<button
				type="button"
				class="rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-300 hover:bg-slate-800"
				onclick={onDeny}
			>
				Refuse
			</button>
		</div>
	</div>
</div>
