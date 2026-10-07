<script lang="ts">
	import { onMount } from 'svelte';
	import { X, Settings, Check, Plus, Trash2, Edit3, ArrowLeft, Loader2, Sparkles, Sliders, Cpu, Key, Database, RefreshCw } from '@lucide/svelte';
	import type { ProfileConfig, SaveProfileRequest, ProfilesResponse } from '$lib/types';
	import { getProfiles, saveProfile, setActiveProfile, deleteProfile } from '$lib/client';
	import { activeProfile, profilesList, userConfigPath, workspaceConfigPath } from '$lib/stores';
	import { cn } from '$lib/utils';

	interface Props {
		open: boolean;
		onClose: () => void;
	}

	let { open, onClose }: Props = $props();

	let loading = $state<boolean>(false);
	let saving = $state<boolean>(false);
	let error = $state<string | null>(null);
	let successMsg = $state<string | null>(null);

	// 'list' or profile name being edited, or 'new'
	let mode = $state<'list' | 'edit'>('list');

	// Form fields
	let formName = $state<string>('');
	let formScope = $state<'workspace' | 'user'>('workspace');
	let formProvider = $state<string>('gemini');
	let formModel = $state<string>('gemini-2.5-flash');
	let formBaseUrl = $state<string>('');
	let formApiKeyEnv = $state<string>('GEMINI_API_KEY');
	let formApiKey = $state<string>('');
	let formUseCompactor = $state<boolean>(false);
	let formCompactorModel = $state<string>('gemini-2.5-flash');
	let formCompactorProvider = $state<string>('gemini');
	let formSetActive = $state<boolean>(true);

	const modelPresets: Record<string, string[]> = {
		gemini: ['gemini-2.5-flash', 'gemini-2.5-pro'],
		opencode: ['deepseek-chat', 'deepseek-reasoner'],
		openai: ['gpt-4o', 'gpt-4o-mini'],
		openrouter: ['anthropic/claude-3.7-sonnet', 'deepseek/deepseek-r1']
	};

	async function loadAllProfiles() {
		loading = true;
		error = null;
		try {
			const res = await getProfiles();
			$activeProfile = res.active_profile || 'default';
			$profilesList = res.profiles || {};
			$userConfigPath = res.user_config_path || '';
			$workspaceConfigPath = res.workspace_config_path || '';
		} catch (err: any) {
			error = err?.message || 'Failed to fetch profiles';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (open) {
			loadAllProfiles();
			mode = 'list';
			error = null;
			successMsg = null;
		}
	});

	function handleOpenNew() {
		formName = '';
		formScope = 'workspace';
		formProvider = 'gemini';
		formModel = 'gemini-2.5-flash';
		formBaseUrl = '';
		formApiKeyEnv = 'GEMINI_API_KEY';
		formApiKey = '';
		formUseCompactor = false;
		formCompactorModel = 'gemini-2.5-flash';
		formCompactorProvider = 'gemini';
		formSetActive = true;
		mode = 'edit';
		error = null;
	}

	function handleEditProfile(name: string, prof: ProfileConfig) {
		formName = name;
		formScope = 'workspace';
		formProvider = prof.provider || 'gemini';
		formModel = prof.model || '';
		formBaseUrl = prof.base_url || '';
		formApiKeyEnv = prof.api_key_env || '';
		formApiKey = prof.api_key || '';
		if (prof.compactor && prof.compactor.model) {
			formUseCompactor = true;
			formCompactorModel = prof.compactor.model;
			formCompactorProvider = prof.compactor.provider || 'gemini';
		} else {
			formUseCompactor = false;
			formCompactorModel = 'gemini-2.5-flash';
			formCompactorProvider = 'gemini';
		}
		formSetActive = $activeProfile === name;
		mode = 'edit';
		error = null;
	}

	function handleProviderChange(newProvider: string) {
		formProvider = newProvider;
		const presets = modelPresets[newProvider];
		if (presets && presets.length > 0) {
			formModel = presets[0];
		}
		if (newProvider === 'gemini') formApiKeyEnv = 'GEMINI_API_KEY';
		else if (newProvider === 'opencode') formApiKeyEnv = 'DEEPSEEK_API_KEY';
		else if (newProvider === 'openai') formApiKeyEnv = 'OPENAI_API_KEY';
		else if (newProvider === 'openrouter') formApiKeyEnv = 'OPENROUTER_API_KEY';
	}

	async function handleSave() {
		if (saving || loading) return;
		const cleanName = formName.trim();
		if (!cleanName) {
			error = 'Profile name cannot be empty.';
			return;
		}

		saving = true;
		error = null;
		successMsg = null;

		const req: SaveProfileRequest = {
			name: cleanName,
			scope: formScope,
			provider: formProvider,
			model: formModel.trim(),
			base_url: formBaseUrl.trim(),
			api_key_env: formApiKeyEnv.trim(),
			api_key: formApiKey.trim(),
			set_active: formSetActive
		};

		if (formUseCompactor && formCompactorModel.trim()) {
			req.compactor = {
				model: formCompactorModel.trim(),
				provider: formCompactorProvider
			};
		}

		try {
			const res = await saveProfile(req);
			$activeProfile = res.active_profile;
			$profilesList = res.profiles;
			successMsg = `Profile "${cleanName}" saved successfully.`;
			mode = 'list';
		} catch (err: any) {
			error = err?.message || 'Failed to save profile';
		} finally {
			saving = false;
		}
	}

	async function handleActivate(name: string) {
		if (saving || loading) return;
		loading = true;
		error = null;
		try {
			const res = await setActiveProfile(name);
			$activeProfile = res.active_profile;
			$profilesList = res.profiles;
			successMsg = `Active profile switched to "${name}".`;
		} catch (err: any) {
			error = err?.message || 'Failed to activate profile';
		} finally {
			loading = false;
		}
	}

	async function handleDelete(name: string) {
		if (saving || loading) return;
		if (!confirm(`Are you sure you want to delete profile "${name}"?`)) return;
		loading = true;
		error = null;
		try {
			const res = await deleteProfile(name);
			$activeProfile = res.active_profile;
			$profilesList = res.profiles;
			successMsg = `Profile "${name}" deleted.`;
		} catch (err: any) {
			error = err?.message || 'Failed to delete profile';
		} finally {
			loading = false;
		}
	}
</script>

{#if open}
	<div
		class="fixed inset-0 z-50 bg-black/75 backdrop-blur-sm flex items-center justify-center p-4 transition-opacity animate-in fade-in"
		role="dialog"
		aria-modal="true"
		tabindex="-1"
		onclick={(e) => e.target === e.currentTarget && onClose()}
		onkeydown={(e) => e.key === 'Escape' && onClose()}
	>
		<div class="relative w-full max-w-2xl bg-[#0d121f] border border-slate-800 rounded-xl shadow-2xl flex flex-col max-h-[88vh] overflow-hidden">
			<!-- Modal Header -->
			<div class="flex items-center justify-between px-5 py-3.5 border-b border-slate-800 bg-slate-950/60">
				<div class="flex items-center gap-2.5">
					<div class="p-1.5 rounded-lg bg-purple-500/10 border border-purple-500/20 text-purple-400">
						<Sliders class="w-4 h-4" />
					</div>
					<div>
						<h2 class="text-sm font-semibold text-slate-100 flex items-center gap-2">
							Profile Configuration
							<span class="text-[10px] font-mono px-2 py-0.5 rounded-full bg-slate-800 text-slate-400">
								active: {$activeProfile}
							</span>
						</h2>
						<p class="text-[11px] text-slate-400">Manage LLM models, provider credentials, and compaction settings</p>
					</div>
				</div>

				<button
					type="button"
					onclick={onClose}
					class="p-1 rounded-md text-slate-400 hover:text-slate-200 hover:bg-slate-800/60 transition-colors"
					title="Close"
				>
					<X class="w-4 h-4" />
				</button>
			</div>

			<!-- Success / Error Banner -->
			{#if error}
				<div class="mx-5 mt-3 p-2.5 rounded-md bg-rose-950/40 border border-rose-800/80 text-rose-300 text-xs font-mono">
					✕ {error}
				</div>
			{/if}
			{#if successMsg}
				<div class="mx-5 mt-3 p-2.5 rounded-md bg-emerald-950/40 border border-emerald-800/80 text-emerald-300 text-xs font-mono">
					✓ {successMsg}
				</div>
			{/if}

			<!-- Modal Body -->
			<div class="flex-1 overflow-y-auto p-5 no-scrollbar space-y-4">
				{#if mode === 'list'}
					<!-- Top Action Bar -->
					<div class="flex items-center justify-between pb-2 border-b border-slate-800/80">
						<div class="text-xs text-slate-400 font-mono">
							Configured Profiles ({Object.keys($profilesList).length})
						</div>
						<div class="flex items-center gap-2">
							<button
								type="button"
								onclick={loadAllProfiles}
								class="p-1.5 rounded-md bg-slate-900 hover:bg-slate-800 border border-slate-800 text-slate-400 hover:text-slate-200 transition-colors"
								title="Refresh"
							>
								<RefreshCw class={cn("w-3.5 h-3.5", loading && "animate-spin")} />
							</button>
							<button
								type="button"
								onclick={handleOpenNew}
								class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-purple-600 hover:bg-purple-500 text-white text-xs font-medium transition-colors shadow-sm"
							>
								<Plus class="w-3.5 h-3.5" />
								<span>Add Profile</span>
							</button>
						</div>
					</div>

					<!-- Profiles Grid -->
					{#if Object.keys($profilesList).length === 0}
						<div class="p-8 text-center text-slate-500 border border-dashed border-slate-800 rounded-lg">
							<Cpu class="w-8 h-8 opacity-20 mx-auto mb-2" />
							<p class="text-xs">No custom profiles defined yet.</p>
							<p class="text-[11px] text-slate-600 mt-1">Golem is using the default configuration from your CLI flags or environment.</p>
							<button
								type="button"
								onclick={handleOpenNew}
								class="mt-4 px-3 py-1.5 rounded-md bg-purple-600/80 hover:bg-purple-600 text-white text-xs inline-flex items-center gap-1.5"
							>
								<Plus class="w-3.5 h-3.5" />
								<span>Create your first profile</span>
							</button>
						</div>
					{:else}
						<div class="grid grid-cols-1 gap-3">
							{#each Object.entries($profilesList) as [name, prof] (name)}
								{@const isActive = $activeProfile === name}
								<div class={cn(
									"p-3.5 rounded-lg border transition-all flex flex-col justify-between gap-3 bg-slate-900/50",
									isActive ? "border-purple-500/60 shadow-md bg-purple-950/20" : "border-slate-800/80 hover:border-slate-700"
								)}>
									<div class="flex items-start justify-between">
										<div class="space-y-1">
											<div class="flex items-center gap-2">
												<span class="font-semibold text-slate-200 text-sm">{name}</span>
												{#if isActive}
													<span class="px-2 py-0.5 rounded-full text-[10px] font-mono font-bold bg-emerald-500/20 text-emerald-300 border border-emerald-500/30 flex items-center gap-1">
														<Check class="w-2.5 h-2.5" /> ACTIVE
													</span>
												{/if}
												<span class="px-2 py-0.5 rounded text-[10px] font-mono uppercase bg-slate-800 text-slate-300">
													{prof.provider || 'gemini'}
												</span>
											</div>
											<div class="text-xs text-slate-400 font-mono">
												Model: <span class="text-slate-200">{prof.model || 'default'}</span>
											</div>
											{#if prof.compactor && prof.compactor.model}
												<div class="text-[11px] text-purple-300/80 font-mono">
													Compactor: {prof.compactor.model} ({prof.compactor.provider || 'gemini'})
												</div>
											{/if}
										</div>

										<div class="flex items-center gap-1.5">
											{#if !isActive}
												<button
													type="button"
													onclick={() => handleActivate(name)}
													disabled={loading || saving}
													class="px-2.5 py-1 rounded bg-slate-800 hover:bg-purple-600 hover:text-white text-slate-300 text-xs font-medium transition-colors disabled:opacity-50 disabled:pointer-events-none"
													title="Switch to this profile"
												>
													Activate
												</button>
											{/if}
											<button
												type="button"
												onclick={() => handleEditProfile(name, prof)}
												disabled={loading || saving}
												class="p-1.5 rounded text-slate-400 hover:text-slate-200 hover:bg-slate-800 transition-colors disabled:opacity-50 disabled:pointer-events-none"
												title="Edit profile"
											>
												<Edit3 class="w-3.5 h-3.5" />
											</button>
											<button
												type="button"
												onclick={() => handleDelete(name)}
												disabled={loading || saving}
												class="p-1.5 rounded text-slate-400 hover:text-rose-400 hover:bg-slate-800 transition-colors disabled:opacity-50 disabled:pointer-events-none"
												title="Delete profile"
											>
												<Trash2 class="w-3.5 h-3.5" />
											</button>
										</div>
									</div>
								</div>
							{/each}
						</div>
					{/if}

					<!-- Storage Paths Footnote -->
					<div class="pt-2 border-t border-slate-800/60 text-[10px] text-slate-500 font-mono space-y-0.5">
						{#if $workspaceConfigPath}
							<div class="truncate">Workspace config: {$workspaceConfigPath}</div>
						{/if}
						{#if $userConfigPath}
							<div class="truncate">User config: {$userConfigPath}</div>
						{/if}
					</div>

				{:else if mode === 'edit'}
					<!-- Form View -->
					<div class="flex items-center justify-between pb-2 border-b border-slate-800/80">
						<button
							type="button"
							onclick={() => (mode = 'list')}
							class="flex items-center gap-1.5 text-xs text-slate-400 hover:text-slate-200 transition-colors"
						>
							<ArrowLeft class="w-3.5 h-3.5" />
							<span>Back to profiles</span>
						</button>
						<span class="text-xs font-medium text-slate-200">
							{formName ? `Edit "${formName}"` : 'Create Profile'}
						</span>
					</div>

					<div class="space-y-3.5 text-xs">
						<!-- Name and Scope -->
						<div class="grid grid-cols-2 gap-3">
							<div class="space-y-1">
								<label for="prof-name" class="block text-slate-300 font-medium">Profile Name</label>
								<input
									id="prof-name"
									type="text"
									bind:value={formName}
									placeholder="e.g. work, fast, coding-heavy"
									class="w-full bg-slate-900/90 border border-slate-800 rounded-md px-3 py-1.5 text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-purple-500 font-mono text-xs"
								/>
							</div>

							<div class="space-y-1">
								<label for="prof-scope" class="block text-slate-300 font-medium">Save Scope</label>
								<select
									id="prof-scope"
									bind:value={formScope}
									class="w-full bg-slate-900/90 border border-slate-800 rounded-md px-3 py-1.5 text-slate-200 focus:outline-none focus:border-purple-500 text-xs"
								>
									<option value="workspace">Workspace (.golem/config.json)</option>
									<option value="user">User Home (~/.golem/config.json)</option>
								</select>
							</div>
						</div>

						<!-- Provider -->
						<div class="space-y-1">
							<label for="prof-prov" class="block text-slate-300 font-medium">Model Provider</label>
							<div class="grid grid-cols-4 gap-2">
								{#each ['gemini', 'opencode', 'openai', 'openrouter'] as prov}
									<button
										type="button"
										onclick={() => handleProviderChange(prov)}
										class={cn(
											"py-1.5 px-2 rounded-md border text-center font-mono capitalize transition-colors",
											formProvider === prov
												? "bg-purple-950/60 border-purple-500 text-purple-300 font-semibold"
												: "bg-slate-900 border-slate-800 text-slate-400 hover:text-slate-200 hover:border-slate-700"
										)}
									>
										{prov}
									</button>
								{/each}
							</div>
						</div>

						<!-- Model Input & Presets -->
						<div class="space-y-1.5">
							<label for="prof-model" class="block text-slate-300 font-medium">Model Name</label>
							<input
								id="prof-model"
								type="text"
								bind:value={formModel}
								placeholder="e.g. gemini-2.5-flash, deepseek-chat, gpt-4o"
								class="w-full bg-slate-900/90 border border-slate-800 rounded-md px-3 py-1.5 text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-purple-500 font-mono text-xs"
							/>
							{#if modelPresets[formProvider]}
								<div class="flex items-center gap-1.5 pt-0.5">
									<span class="text-[10px] text-slate-500">Presets:</span>
									{#each modelPresets[formProvider] as preset}
										<button
											type="button"
											onclick={() => (formModel = preset)}
											class="text-[10px] font-mono px-2 py-0.5 rounded bg-slate-800 hover:bg-purple-900/50 hover:text-purple-300 text-slate-400 transition-colors"
										>
											{preset}
										</button>
									{/each}
								</div>
							{/if}
						</div>

						<!-- Credentials / Env Var -->
						<div class="grid grid-cols-2 gap-3">
							<div class="space-y-1">
								<label for="prof-env" class="block text-slate-300 font-medium">API Key Env Var</label>
								<input
									id="prof-env"
									type="text"
									bind:value={formApiKeyEnv}
									placeholder="e.g. GEMINI_API_KEY"
									class="w-full bg-slate-900/90 border border-slate-800 rounded-md px-3 py-1.5 text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-purple-500 font-mono text-xs"
								/>
							</div>

							<div class="space-y-1">
								<label for="prof-key" class="block text-slate-300 font-medium">Direct API Key (optional)</label>
								<input
									id="prof-key"
									type="password"
									bind:value={formApiKey}
									placeholder="Optional fallback secret"
									class="w-full bg-slate-900/90 border border-slate-800 rounded-md px-3 py-1.5 text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-purple-500 font-mono text-xs"
								/>
							</div>
						</div>

						<!-- Base URL -->
						<div class="space-y-1">
							<label for="prof-baseurl" class="block text-slate-300 font-medium">Custom Base URL (optional)</label>
							<input
								id="prof-baseurl"
								type="text"
								bind:value={formBaseUrl}
								placeholder="e.g. https://api.deepseek.com/v1 or http://localhost:11434/v1"
								class="w-full bg-slate-900/90 border border-slate-800 rounded-md px-3 py-1.5 text-slate-200 placeholder:text-slate-600 focus:outline-none focus:border-purple-500 font-mono text-xs"
							/>
						</div>

						<!-- Dedicated Compactor Option -->
						<div class="p-3 rounded-lg border border-slate-800 bg-slate-950/40 space-y-2.5">
							<label class="flex items-center gap-2 cursor-pointer">
								<input
									type="checkbox"
									bind:checked={formUseCompactor}
									class="rounded border-slate-700 bg-slate-800 text-purple-600 focus:ring-0"
								/>
								<span class="font-medium text-slate-200">Use Dedicated Compactor Model</span>
							</label>

							{#if formUseCompactor}
								<div class="grid grid-cols-2 gap-3 pt-1">
									<div class="space-y-1">
										<label for="comp-model" class="block text-slate-400 text-[11px]">Compactor Model</label>
										<input
											id="comp-model"
											type="text"
											bind:value={formCompactorModel}
											placeholder="e.g. gemini-2.5-flash"
											class="w-full bg-slate-900 border border-slate-800 rounded-md px-2.5 py-1 text-slate-200 font-mono text-xs"
										/>
									</div>
									<div class="space-y-1">
										<label for="comp-prov" class="block text-slate-400 text-[11px]">Compactor Provider</label>
										<select
											id="comp-prov"
											bind:value={formCompactorProvider}
											class="w-full bg-slate-900 border border-slate-800 rounded-md px-2.5 py-1 text-slate-200 text-xs"
										>
											<option value="gemini">Gemini</option>
											<option value="opencode">OpenCode</option>
											<option value="openai">OpenAI</option>
											<option value="openrouter">OpenRouter</option>
										</select>
									</div>
								</div>
							{/if}
						</div>

						<!-- Set Active Checkbox -->
						<label class="flex items-center gap-2 cursor-pointer pt-1">
							<input
								type="checkbox"
								bind:checked={formSetActive}
								class="rounded border-slate-700 bg-slate-800 text-purple-600 focus:ring-0"
							/>
							<span class="text-slate-300">Set as active profile immediately upon saving</span>
						</label>

						<!-- Save / Cancel Buttons -->
						<div class="flex items-center justify-end gap-2 pt-3 border-t border-slate-800">
							<button
								type="button"
								onclick={() => (mode = 'list')}
								class="px-3 py-1.5 rounded-md bg-slate-900 hover:bg-slate-800 text-slate-300 transition-colors text-xs"
							>
								Cancel
							</button>
							<button
								type="button"
								onclick={handleSave}
								disabled={saving}
								class="flex items-center gap-1.5 px-4 py-1.5 rounded-md bg-purple-600 hover:bg-purple-500 text-white font-medium transition-colors text-xs disabled:opacity-50"
							>
								{#if saving}
									<Loader2 class="w-3.5 h-3.5 animate-spin" />
									<span>Saving...</span>
								{:else}
									<Check class="w-3.5 h-3.5" />
									<span>Save Profile</span>
								{/if}
							</button>
						</div>
					</div>
				{/if}
			</div>
		</div>
	</div>
{/if}
