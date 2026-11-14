<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import Ring from '$lib/components/Ring.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtDuration } from '$lib/i18n';
  import { accounts as mockAccounts, backupSource, jobs, accountName } from '$lib/mock';
  import { data, refreshJobs, type JobView } from '$lib/data.svelte';
  import { backend, isWails } from '$lib/backend';

  const step = $derived(app.step);
  const mirror = $derived(app.mirrorMode);
  const isProgress = $derived(app.route === 'wizard/progress');
  const startDisabled = $derived(step === 3 && mirror && !app.mirrorChecked);

  const accounts = $derived(data.accounts.length ? data.accounts : mockAccounts);
  const currentAccount = $derived(accounts.find((a) => a.id === app.account) ?? accounts[0]);

  let liveJob = $state<JobView | null>(null);
  let completed = $state(false);

  const src = $derived({
    name: app.sourceName || (app.lang === 'en' ? backupSource.nameEn : backupSource.name),
    files: app.sourcePath ? app.sourceFiles : backupSource.files,
    bytes: app.sourcePath ? app.sourceBytes : backupSource.bytes
  });

  const shown = $derived(
    liveJob
      ? {
          bytes: liveJob.bytes,
          total: liveJob.totalBytes,
          speed: liveJob.speed,
          eta: liveJob.eta ?? 0,
          current: liveJob.current ?? ''
        }
      : {
          bytes: jobs.active.bytes,
          total: jobs.active.total,
          speed: jobs.active.speed,
          eta: jobs.active.eta,
          current: jobs.active.current ?? ''
        }
  );
  const pct = $derived(shown.total > 0 ? shown.bytes / shown.total : 0);

  const quick = [
    ['desktop', 'files.root.desktop', 'computer'],
    ['documents', 'files.root.documents', 'description'],
    ['pictures', 'files.root.pictures', 'image'],
    ['downloads', 'files.root.downloads', 'download'],
    ['music', 'files.root.music', 'music-note']
  ] as const;

  async function pickSource(): Promise<void> {
    const picked = await backend.chooseFolder(t('wizard.s1.folder'));
    if (!picked?.path) {
      if (!isWails) app.notify(t('wizard.s1.folder'));
      return;
    }
    app.sourcePath = picked.path;
    app.sourceName = picked.path.split(/[\\/]/).filter(Boolean).pop() ?? picked.path;
    app.sourceChoice = 'folder';
    const m = await backend.measure(picked.path);
    if (m) {
      app.sourceFiles = m.files;
      app.sourceBytes = m.bytes;
      app.sourceEstimated = m.estimated;
    }
  }

  async function startBackup(): Promise<void> {
    if (!isWails) {
      app.go('wizard/progress');
      return;
    }
    if (!app.sourcePath) {
      await pickSource();
      if (!app.sourcePath) return;
    }
    const res = await backend.startBackup({
      sourcePaths: [app.sourcePath],
      accountId: app.account,
      destRoot: app.destRoot,
      mode: mirror ? 'mirror' : 'additive',
      fastFiles: app.fastFiles,
      transfers: 4
    });
    if (!res || res.err) {
      app.notify(res?.err?.detail || res?.err?.message || 'backup failed');
      return;
    }
    app.activeJobId = res.id;
    completed = false;
    liveJob = null;
    app.go('wizard/progress');
  }

  $effect(() => {
    if (!isProgress || !isWails) return;
    const timer = setInterval(async () => {
      if (!app.activeJobId) return;
      await refreshJobs();
      const found = data.jobs.find((j) => j.id === app.activeJobId);
      if (found) {
        liveJob = found;
      } else if (liveJob) {
        completed = true;
        liveJob = null;
      }
    }, 1000);
    return () => clearInterval(timer);
  });

  function next(): void {
    app.go(`wizard/${Math.min(3, step + 1)}`);
  }
  function back(): void {
    app.go(`wizard/${Math.max(1, step - 1)}`);
  }
  function setMirror(value: boolean): void {
    app.mirrorMode = value;
    app.go(app.route);
  }
  function toggleAdvanced(): void {
    app.advancedOpen = !app.advancedOpen;
    app.go(app.route);
  }
</script>

<Modal labelledby="wiz-title" onclose={() => app.closeOverlay()}>
  <div class="dialog wizard">
    <div class="dialog-head">
      <div class="grow">
        <div class="dialog-title" id="wiz-title">
          {isProgress ? t('progress.backingUp') : t('wizard.title')}
        </div>
        {#if !isProgress}
          <div style="margin-top:8px">
            <div class="wizard-steps">
              {#each ['wizard.step1', 'wizard.step2', 'wizard.step3'] as key, i (key)}
                <span class="step-dot" class:done={i + 1 < step} class:active={i + 1 === step}>
                  <span class="n">
                    {#if i + 1 < step}<Icon name="check" size={13} />{:else}{i + 1}{/if}
                  </span>
                  {t(key)}
                </span>
                {#if i < 2}<span class="step-line"></span>{/if}
              {/each}
            </div>
          </div>
        {/if}
      </div>
      <button class="icon-button" aria-label={t('common.close')} onclick={() => app.closeOverlay()}>
        <Icon name="close" size={20} />
      </button>
    </div>

    <div class="dialog-body">
      {#if isProgress}
        <div class="wizard-body" style="min-height:auto">
          {#if completed}
            <div class="progress-view">
              <div class="big-ring" style="display:grid;place-items:center;margin-bottom:14px">
                <span class="onboard-mark" style="width:96px;height:96px;margin:0">
                  <Icon name="check" size={44} />
                </span>
              </div>
              <div class="progress-line-1">{t('progress.done')}</div>
              <div class="progress-line-2">
                {t('progress.doneSub', { files: src.files.toLocaleString('en-US') })}
              </div>
            </div>
          {:else}
            <div class="progress-view">
              <div class="big-ring"><Ring pct={pct} size={128} /></div>
              <div class="progress-line-1">
                {t('progress.bytes', { done: fmtBytes(shown.bytes), total: fmtBytes(shown.total) })}
              </div>
              <div class="progress-line-2">
                {t('progress.eta', { time: fmtDuration(shown.eta) })}
              </div>
              <div class="progress-file">
                <Icon name="cloud-upload" size={16} />
                <span class="name truncate">{t('progress.current', { name: shown.current })}</span>
                <span class="status info">{(shown.speed / 1024 / 1024).toFixed(1)} MB/s</span>
              </div>
            </div>
          {/if}
        </div>
      {:else if step === 1}
        <div class="wizard-body">
          <div>
            <h2 class="step-title">{t('wizard.s1.title')}</h2>
            <p class="step-sub">{t('wizard.s1.sub')}</p>
          </div>

          <div class="option-pair">
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <label class="option big" class:selected={app.sourceChoice === 'folder'} onclick={pickSource}>
              <input type="radio" name="wiz-source" checked={app.sourceChoice === 'folder'}
                onchange={() => (app.sourceChoice = 'folder')} />
              <Icon name="folder" size={26} />
              <span class="t">{t('wizard.s1.folder')}</span>
              <span class="d">{t('wizard.s1.folderSub')}</span>
            </label>
            <label class="option big" class:selected={app.sourceChoice === 'files'}>
              <input type="radio" name="wiz-source" checked={app.sourceChoice === 'files'}
                onchange={() => (app.sourceChoice = 'files')} />
              <Icon name="description" size={26} />
              <span class="t">{t('wizard.s1.files')}</span>
              <span class="d">{t('wizard.s1.filesSub')}</span>
            </label>
          </div>

          <div class="quick-locations">
            <span class="lbl">{t('wizard.s1.quick')}</span>
            {#each quick as [id, key, icon] (id)}
              <button class="chip interactive" class:selected={id === 'downloads'}>
                <Icon name={icon} size={16} /><span>{t(key)}</span>
              </button>
            {/each}
          </div>

          <div class="source-summary">
            <Icon name="folder" size={20} />
            <div class="sc">
              <div class="sn">{src.name}</div>
              <div class="ss">
                {t('wizard.s1.count', {
                  files: src.files.toLocaleString('en-US'),
                  size: fmtBytes(src.bytes)
                })}
              </div>
            </div>
            <button class="btn text sm" onclick={pickSource}>
              {t('common.change')}
            </button>
          </div>
        </div>
      {:else if step === 2}
        <div class="wizard-body">
          <div>
            <h2 class="step-title">{t('wizard.s2.title')}</h2>
            <p class="step-sub">{t('wizard.s2.sub')}</p>
          </div>

          <div class="account-list">
            {#each accounts as a (a.id)}
              <label class="account-row" class:selected={a.id === app.account}>
                <input type="radio" name="wiz-account" checked={a.id === app.account}
                  onchange={() => (app.account = a.id)} />
                <span class="ptile {a.provider}">
                  <ProviderIcon provider={a.provider} size={22} />
                </span>
                <div class="am">
                  <div class="an">{accountName(a, app.lang)}</div>
                  <div class="as">
                    {t('clouds.used', { used: fmtBytes(a.used), total: fmtBytes(a.total) })}
                  </div>
                </div>
                <div class="as nums" style="color:var(--m3c-on-surface-variant)">
                  {fmtBytes(a.total - a.used)} free
                </div>
              </label>
            {/each}
          </div>

          <div class="dest-block">
            <span class="hint">{t('wizard.s2.dest')}</span>
            <div class="breadcrumb">
              <span class="crumb"><Icon name="folder" size={15} />{t('files.folder.backup')}</span>
              <span class="crumb-sep"><Icon name="chevron-right" size={16} /></span>
              <span class="crumb"><Icon name="folder-open" size={15} />{src.name}</span>
            </div>
            <button class="btn text sm" onclick={() => app.notify(t('common.change'))}>
              {t('common.change')}
            </button>
          </div>

          <div class="banner info" style="align-items:center">
            <Icon name="history" size={20} />
            <div>{t('wizard.s2.hint', { files: (1240).toLocaleString('en-US') })}</div>
          </div>

          <button class="btn text sm" style="align-self:flex-start" onclick={() => app.go('clouds/add')}>
            <Icon name="add" size={16} />{t('wizard.s2.connect')}
          </button>
        </div>
      {:else}
        <div class="wizard-body">
          <div class="summary-box">
            {t('wizard.s3.summary', {
              source: src.name,
              files: src.files.toLocaleString('en-US'),
              size: fmtBytes(src.bytes),
              account: accountName(currentAccount, app.lang),
              dest: `${t('files.folder.backup')} / ${src.name}`
            })}
          </div>

          <div class="mode-list">
            <label class="mode" class:selected={!mirror}>
              <input type="radio" name="wiz-mode" checked={!mirror} onchange={() => setMirror(false)} />
              <div>
                <div class="mt">{t('wizard.s3.modeAdd')}</div>
                <div class="md">{t('wizard.s3.modeAddSub')}</div>
              </div>
            </label>
            <label class="mode" class:selected={mirror}>
              <input type="radio" name="wiz-mode" checked={mirror} onchange={() => setMirror(true)} />
              <div>
                <div class="mt">{t('wizard.s3.modeMirror')}</div>
                <div class="md">{t('wizard.s3.modeMirrorSub')}</div>
              </div>
            </label>
          </div>

          {#if mirror}
            <div class="mirror-warn">
              <Icon name="warning" size={20} />
              <div class="grow">
                <div class="wt">
                  {t('wizard.s3.warnTitle', { files: '37', size: fmtBytes(412 * 1024 * 1024) })}
                </div>
                <button class="btn text sm" style="padding-left:0;color:inherit"
                  onclick={() => app.notify(t('wizard.s3.warnView'))}>
                  {t('wizard.s3.warnView')}
                </button>
                <label class="checkline">
                  <input type="checkbox" bind:checked={app.mirrorChecked} />
                  <span>{t('wizard.s3.confirm')}</span>
                </label>
              </div>
            </div>
          {/if}

          <button class="advanced-toggle" class:open={app.advancedOpen} onclick={toggleAdvanced}>
            <Icon name="tune" size={18} />{t('wizard.s3.advanced')}<Icon name="expand-more" size={18} />
          </button>

          {#if app.advancedOpen}
            <div class="advanced-panel">
              <div class="adv-row">
                <div class="al">
                  <div class="at">{t('wizard.s3.fast')}</div>
                  <div class="ad">{t('wizard.s3.fastSub')}</div>
                  <div class="ad fast-count">{t('wizard.s3.fastCount', { n: 3, parts: 12 })}</div>
                </div>
                <input type="checkbox" class="switch" role="switch" bind:checked={app.fastFiles} />
              </div>
              <hr class="divider" />
              <div class="adv-row">
                <div class="al">
                  <div class="at">{t('wizard.adv.excludes')}</div>
                  <div class="ad">{t('wizard.adv.excludesSub')}</div>
                </div>
                <button class="btn text sm" onclick={() => app.notify(t('common.change'))}>
                  {t('common.change')}
                </button>
              </div>
              <div class="adv-row">
                <div class="al"><div class="at">{t('wizard.adv.speed')}</div></div>
                <span class="hint">{t('common.unlimited')}</span>
              </div>
              <div class="adv-row">
                <div class="al">
                  <div class="at">{t('wizard.adv.parts')}</div>
                  <div class="ad">{t('wizard.adv.partsSub')}</div>
                </div>
                <span class="hint">{t('common.auto')}</span>
              </div>
            </div>
          {/if}
        </div>
      {/if}
    </div>

    <div class="dialog-foot">
      {#if isProgress}
        <div class="grow"></div>
        <div class="progress-actions">
          {#if completed}
            <button class="btn filled" onclick={() => app.closeOverlay()}>{t('common.done')}</button>
          {:else}
            <button class="btn tonal" onclick={() => app.notify(t('progress.pauseHint'))}>
              <Icon name="pause" size={18} /><span>{t('progress.pause')}</span>
            </button>
            <button class="btn text" onclick={() => app.closeOverlay()}>{t('common.cancel')}</button>
          {/if}
        </div>
      {:else}
        {#if step > 1}
          <!-- svelte-ignore a11y_autofocus -->
          <button class="btn text" autofocus={startDisabled} onclick={back}>{t('common.back')}</button>
        {/if}
        <div class="grow"></div>
        {#if step < 3}
          <!-- svelte-ignore a11y_autofocus -->
          <button class="btn filled" autofocus onclick={next}>{t('common.next')}</button>
        {:else}
          <!-- svelte-ignore a11y_autofocus -->
          <button class="btn filled" autofocus={!startDisabled} disabled={startDisabled}
            onclick={startBackup}>
            <Icon name="cloud-upload" size={18} /><span>{t('wizard.s3.start')}</span>
          </button>
        {/if}
      {/if}
    </div>
  </div>
</Modal>
