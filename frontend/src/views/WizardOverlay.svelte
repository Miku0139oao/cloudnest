<script lang="ts">
  import { onMount } from 'svelte';
  import Icon from '$lib/components/Icon.svelte';
  import ProviderIcon from '$lib/components/ProviderIcon.svelte';
  import Ring from '$lib/components/Ring.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import CloudFolderPicker from './CloudFolderPicker.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes, fmtDuration } from '$lib/i18n';
  import { accounts as mockAccounts, backupSource, jobs, accountName } from '$lib/mock';
  import { data, refreshJobs, refreshHistory, type JobView } from '$lib/data.svelte';
  import { backend, isWails } from '$lib/backend';

  const step = $derived(app.step);
  const mirror = $derived(app.mirrorMode);
  const isProgress = $derived(app.route === 'wizard/progress');
  const startDisabled = $derived(step === 3 && mirror && !app.mirrorChecked);

  const accounts = $derived(data.accounts.length ? data.accounts : mockAccounts);
  const currentAccount = $derived(accounts.find((a) => a.id === app.account) ?? accounts[0]);

  let liveJob = $state<JobView | null>(null);
  let completed = $state(false);
  let finishError = $state('');
  let showDeletions = $state(false);
  let folderPicker = $state(false);
  let verifying = $state(false);
  let verifyResult = $state<{
    differ: number;
    missing: number;
    examples?: string[];
    status?: string;
    errors?: number;
  } | null>(null);

  // rclone reports a check that couldn't read or hash files as
  // "N errors while checking" with no differ/missing entries; never present
  // that as a complete backup.
  function verifyErrorCount(r: { errors?: number; status?: string }): number {
    if (r.errors && r.errors > 0) return r.errors;
    const m = /(\d+)\s+errors?\b/i.exec(r.status ?? '');
    return m ? Number(m[1]) : 0;
  }

  function verifyStatusOk(
    status: string | undefined,
    differ: number,
    missing: number,
    errors: number
  ): boolean {
    // The counts are authoritative. The backend deliberately keeps rclone's
    // "N differences found" string for a reconciled split backup and zeroes
    // the counts, so the status text alone must not decide the verdict.
    if (errors > 0 || differ > 0 || missing > 0) return false;
    const s = (status ?? '').trim().toLowerCase();
    if (!s) return true;
    if (s.includes('split uploads verified') || s.includes('split upload verified')) return true;
    if (s.includes('could not') || s.includes('did not complete')) return false;
    // Successful checks can carry descriptive statuses ("identical by size").
    // Only explicit failure wording means the check did not complete.
    return !(
      /\d+\s+errors?\b/.test(s) ||
      s.includes('error') ||
      s.includes('differences found')
    );
  }

  const verifyErrors = $derived(verifyResult ? verifyErrorCount(verifyResult) : 0);
  const verifyStatusFailed = $derived(
    !!verifyResult &&
      !verifyStatusOk(
        verifyResult.status,
        verifyResult.differ,
        verifyResult.missing,
        verifyErrors
      )
  );

  // Runs rclone check for every source the backup used: a folder is compared
  // as a tree, while each selected file is compared against its own object
  // (passing a file's parent folder would report unrelated siblings missing).
  async function verifyNow(): Promise<void> {
    if (!isWails || app.sourcePaths.length === 0) return;
    verifying = true;
    let differ = 0;
    let missing = 0;
    let errors = 0;
    let status = 'success';
    let failureStatus = '';
    const examples: string[] = [];
    let failure = '';
    for (const src of app.sourcePaths) {
      const res = (await backend.verifyBackup(app.account, src, app.destRoot)) as
        | {
            differ?: number;
            missing?: number;
            examples?: string[];
            status?: string;
            errors?: number;
            err?: { message?: string; detail?: string };
          }
        | null;
      if (!res || res.err) {
        failure = res?.err?.detail || res?.err?.message || t('files.verify');
        break;
      }
      const resDiffer = res.differ ?? 0;
      const resMissing = res.missing ?? 0;
      const resErrors = verifyErrorCount(res);
      differ += resDiffer;
      missing += resMissing;
      errors += resErrors;
      if (!verifyStatusOk(res.status, resDiffer, resMissing, resErrors) && !failureStatus) {
        failureStatus = res.status || 'verification did not complete';
      }
      examples.push(...(res.examples ?? []));
      if (res.status) status = res.status;
    }
    verifying = false;
    if (failure) {
      app.notify(failure);
      return;
    }
    verifyResult = {
      differ,
      missing,
      examples: examples.slice(0, 5),
      status: failureStatus || status,
      errors
    };
  }

  // Backup preferences shown in the advanced panel. They are read-only here;
  // the Settings page owns them.
  let prefs = $state({ bwLimit: 'off', partSizeMB: 256 });
  let excludeText = $state('');

  onMount(async () => {
    const s = await backend.getSettings();
    if (s?.backup) {
      prefs = { bwLimit: s.backup.bwLimit || 'off', partSizeMB: s.backup.partSizeMB || 256 };
    }
    excludeText = app.excludes.join('\n');
  });

  function openExcludes(): void {
    excludeText = app.excludes.join('\n');
    app.excludesOpen = true;
  }

  function saveExcludes(): void {
    app.excludes = excludeText
      .split('\n')
      .map((line) => line.trim())
      .filter(Boolean);
    app.excludesOpen = false;
    computePlan();
  }

  function bwLabel(): string {
    return prefs.bwLimit === 'off'
      ? t('common.unlimited')
      : `${prefs.bwLimit.replace(/M$/, '')} MB/s`;
  }

  const jobPaused = $derived(liveJob?.state === 'paused');
  // Pause/Resume and Retry both call the backend for the same job; a
  // double-click would reach ResumeJob twice and surface its duplicate-start
  // refusal as a false failure.
  let actionBusy = $state(false);
  // Start is the only wizard action which creates a new job; guard it too.
  let starting = $state(false);

  async function togglePause(): Promise<void> {
    if (!app.activeJobId || actionBusy) return;
    actionBusy = true;
    try {
      if (jobPaused) {
        const res = await backend.resumeJob(app.activeJobId);
        // ResumeJob refuses with a reason while the previous run is still
        // winding down; show it instead of silently leaving the card paused.
        if (res?.err) {
          app.notify(res.err.detail || res.err.message || t('progress.resumeFailed'));
          return;
        }
      } else {
        await backend.pauseJob(app.activeJobId);
      }
      await refreshJobs();
      const found = data.jobs.find((j) => j.id === app.activeJobId);
      if (found) liveJob = found;
    } finally {
      actionBusy = false;
    }
  }

  interface PlanView {
    files: number;
    bytes: number;
    estimated: boolean;
    deleteCount: number;
    deleteBytes: number;
    deletions: { path: string; size: number }[];
    warnings?: string[];
  }
  let plan = $state<PlanView | null>(null);
  let planLoading = $state(false);
  // A failed plan must never read as "0 files will be deleted": that is a
  // destructive mirror decision. Keep the error and block Start until a real
  // plan exists.
  let planError = $state('');

  // Mirror syncs one folder into destRoot/<folder>; a file set (or several
  // folders) is copied item by item, so Plan refuses mirror for it, and a
  // drive root has no folder name to sync into. Offer the mode only where it
  // can actually run, and fall back to additive when the source changes
  // underneath a persisted mirror selection.
  //
  // sourceChoice is written only by the pickers on success (never by the radio
  // itself), so a cancelled dialog cannot leave the flag describing a source
  // that is not selected.
  function isDriveRoot(p: string): boolean {
    return /^[A-Za-z]:[\\/]*$/.test(p.trim());
  }
  const canMirror = $derived(
    app.sourceChoice === 'folder' && app.sourcePaths.length === 1 && !isDriveRoot(app.sourcePaths[0])
  );
  $effect(() => {
    if (!canMirror && app.mirrorMode) app.mirrorMode = false;
  });

  let planSeq = 0;

  async function computePlan(): Promise<void> {
    if (!isWails || app.sourcePaths.length === 0) return;
    const seq = ++planSeq;
    planLoading = true;
    planError = '';
    try {
      const res = (await backend.plan({
        sourcePaths: app.sourcePaths,
        accountId: app.account,
        destRoot: app.destRoot,
        mode: mirror && canMirror ? 'mirror' : 'additive',
        fastFiles: app.fastFiles,
        transfers: 4,
        excludes: app.excludes
      })) as { plan?: PlanView; err?: { code?: string; message?: string; detail?: string } } | null;
      // A slower earlier response must not replace the newest plan or error.
      if (seq !== planSeq) return;
      if (!res || res.err || !res.plan) {
        plan = null;
        planError =
          res?.err?.detail ||
          (res?.err?.code === 'provider_unsupported'
            ? t('wizard.s3.modeMirrorFolderOnly')
            : res?.err?.message) ||
          t('wizard.s3.planFailed');
        return;
      }
      plan = res.plan;
    } finally {
      if (seq === planSeq) planLoading = false;
    }
  }

  $effect(() => {
    const s = step;
    const m = mirror && canMirror;
    const count = app.sourcePaths.length;
    if (s !== 3 || !count || !isWails) return;
    computePlan();
  });

  const src = $derived({
    name:
      app.sourcePaths.length > 1
        ? t('wizard.s1.fileCount', { n: app.sourcePaths.length })
        : app.sourceName || (isWails ? '' : (app.lang === 'en' ? backupSource.nameEn : backupSource.name)),
    files: app.sourcePaths.length ? app.sourceFiles : (isWails ? 0 : backupSource.files),
    bytes: app.sourcePaths.length ? app.sourceBytes : (isWails ? 0 : backupSource.bytes)
  });

  const summaryFiles = $derived(plan?.files ?? src.files);
  const summaryBytes = $derived(plan?.bytes ?? src.bytes);
  const summaryDeleteCount = $derived(plan?.deleteCount ?? 0);
  const summaryDeleteBytes = $derived(plan?.deleteBytes ?? 0);

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

  // One click on a source card can reach its picker twice: the label's own
  // click handler runs, and the browser's label activation also dispatches a
  // click from the nested radio which bubbles back to the label. Without a
  // guard both calls request a native dialog.
  let picking = $state(false);

  async function pickSource(): Promise<void> {
    if (picking) return;
    picking = true;
    try {
      const picked = await backend.chooseFolder(t('wizard.s1.folder'));
      if (!picked?.path) {
        if (!isWails) app.notify(t('wizard.s1.folder'));
        return;
      }
      app.sourcePaths = [picked.path];
      app.sourceName = picked.path.split(/[\\/]/).filter(Boolean).pop() ?? picked.path;
      app.sourceChoice = 'folder';
      const m = await backend.measure(picked.path);
      if (m) {
        app.sourceFiles = m.files;
        app.sourceBytes = m.bytes;
        app.sourceEstimated = m.estimated;
      }
    } finally {
      picking = false;
    }
  }

  async function pickFiles(): Promise<void> {
    if (picking) return;
    picking = true;
    try {
      const picked = await backend.chooseFiles(t('wizard.s1.files'));
      if (!picked?.paths?.length) {
        if (!isWails) app.notify(t('wizard.s1.files'));
        return;
      }
      app.sourcePaths = picked.paths;
      app.sourceChoice = 'files';
      app.sourceName =
        picked.paths.length === 1
          ? (picked.paths[0].split(/[\\/]/).filter(Boolean).pop() ?? picked.paths[0])
          : '';
      let files = 0;
      let bytes = 0;
      let estimated = false;
      for (const p of picked.paths) {
        const m = await backend.measure(p);
        if (m) {
          files += m.files;
          bytes += m.bytes;
          estimated = estimated || m.estimated;
        }
      }
      app.sourceFiles = files;
      app.sourceBytes = bytes;
      app.sourceEstimated = estimated;
    } finally {
      picking = false;
    }
  }

  async function startBackup(): Promise<void> {
    // The Start button stays mounted until the IPC call resolves and
    // StartBackup mints a new job for every call, so a double-click would run
    // two independent backups over the same destination.
    if (starting) return;
    if (!isWails) {
      app.go('wizard/progress');
      return;
    }
    // Never start a destructive mirror sync whose deletion count was never
    // computed: the failed plan would otherwise read as "0 files deleted".
    if (mirror && (!canMirror || !plan || planError)) {
      app.notify(!canMirror ? t('wizard.s3.modeMirrorFolderOnly') : planError || t('wizard.s3.planFailed'));
      return;
    }
    starting = true;
    try {
      if (app.sourcePaths.length === 0) {
        await pickSource();
        if (app.sourcePaths.length === 0) return;
      }
      const res = await backend.startBackup({
        sourcePaths: app.sourcePaths,
        accountId: app.account,
        destRoot: app.destRoot,
        mode: mirror && canMirror ? 'mirror' : 'additive',
        fastFiles: app.fastFiles,
        transfers: 4,
        excludes: app.excludes
      });
      if (!res || res.err) {
        app.notify(res?.err?.detail || res?.err?.message || 'backup failed');
        return;
      }
      app.activeJobId = res.id;
      completed = false;
      verifyResult = null;
      liveJob = null;
      app.go('wizard/progress');
    } finally {
      starting = false;
    }
  }

  $effect(() => {
    if (!isProgress || !isWails) return;
    const timer = setInterval(async () => {
      if (!app.activeJobId) return;
      await refreshJobs();
      const found = data.jobs.find((j) => j.id === app.activeJobId);
      if (found) {
        if (found.state === 'failed' || found.state === 'interrupted') {
          // A failed job used to look identical to a finished one: the wizard
          // showed "backup complete" because the job simply disappeared. An
          // interrupted job has the same rule: its outcome was never observed,
          // so it must never render as a success either.
          finishError =
            found.error ||
            t(found.state === 'interrupted' ? 'progress.interrupted' : 'progress.failedUnknown');
          liveJob = null;
          refreshHistory();
          return;
        }
        liveJob = found;
      } else if (liveJob) {
        liveJob = null;
        await refreshHistory();
        const rec = data.history.find((r) => r.id === app.activeJobId);
        if (rec?.state === 'failed' || rec?.state === 'interrupted') {
          finishError =
            rec.error ||
            t(rec.state === 'interrupted' ? 'progress.interrupted' : 'progress.failedUnknown');
        } else {
          completed = true;
        }
      }
    }, 1000);
    return () => clearInterval(timer);
  });

  async function retryBackup(): Promise<void> {
    if (!app.activeJobId || actionBusy) return;
    actionBusy = true;
    try {
      const res = await backend.resumeJob(app.activeJobId);
      // A refused retry must not wipe the failure banner and then do nothing.
      if (res?.err) {
        app.notify(res.err.detail || res.err.message || t('progress.resumeFailed'));
        return;
      }
      finishError = '';
      completed = false;
      await refreshJobs();
    } finally {
      actionBusy = false;
    }
  }

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
              {#if verifyResult}
                <div class="tech" style="margin-top:14px;text-align:left;width:100%">
                  <div class="tech-title">{t('files.verifyTitle')}</div>
                  {#if verifyErrors > 0}
                    <div>{t('files.verifyErrorBody', { n: verifyErrors })}</div>
                  {:else if verifyResult.differ || verifyResult.missing}
                    <div>{t('files.verifyDifferBody')}</div>
                    <div class="nums" style="margin-top:6px">
                      {t('files.differ')}: {verifyResult.differ} · {t('files.missing')}: {verifyResult.missing}
                    </div>
                    {#if verifyResult.examples?.length}
                      <pre>{verifyResult.examples.slice(0, 5).join('\n')}</pre>
                    {/if}
                  {:else if verifyStatusFailed}
                    <div>{t('files.verifyIncompleteBody', { status: verifyResult.status || '—' })}</div>
                  {:else}
                    <div>{t('files.verifySameBody')}</div>
                  {/if}
                </div>
              {:else}
                <button class="btn tonal" style="margin-top:12px" disabled={verifying || !isWails}
                  onclick={verifyNow}>
                  <Icon name="verified" size={18} />
                  <span>{verifying ? t('files.startVerify') : t('files.verify')}</span>
                </button>
              {/if}
            </div>
          {:else if finishError}
            <div class="progress-view">
              <div class="big-ring" style="display:grid;place-items:center;margin-bottom:14px">
                <span class="onboard-mark" style="width:96px;height:96px;margin:0;background:var(--m3c-error-container);color:var(--m3c-on-error-container)">
                  <Icon name="error" size={44} />
                </span>
              </div>
              <div class="progress-line-1">{t('progress.failed')}</div>
              <div class="progress-line-2">{t('progress.failedSub')}</div>
              <div class="tech" style="margin-top:14px;text-align:left;width:100%">
                <div class="tech-title">{t('activity.detail.tech')}</div>
                <pre>{finishError}</pre>
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
                onclick={(e) => e.preventDefault()}
                onkeydown={(e) => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); pickSource(); } }} />
              <Icon name="folder" size={26} />
              <span class="t">{t('wizard.s1.folder')}</span>
              <span class="d">{t('wizard.s1.folderSub')}</span>
            </label>
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
            <label class="option big" class:selected={app.sourceChoice === 'files'} onclick={pickFiles}>
              <input type="radio" name="wiz-source" checked={app.sourceChoice === 'files'}
                onclick={(e) => e.preventDefault()}
                onkeydown={(e) => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); pickFiles(); } }} />
              <Icon name="description" size={26} />
              <span class="t">{t('wizard.s1.files')}</span>
              <span class="d">{t('wizard.s1.filesSub')}</span>
            </label>
          </div>

          <div class="quick-locations">
            <span class="lbl">{t('wizard.s1.quick')}</span>
            {#each quick as [id, key, icon] (id)}
              <button class="chip interactive" class:selected={id === 'downloads'} onclick={pickSource}>
                <Icon name={icon} size={16} /><span>{t(key)}</span>
              </button>
            {/each}
          </div>

          <div class="source-summary">
            <Icon name="folder" size={20} />
            <div class="sc">
              <div class="sn">{src.name || t('wizard.s1.none')}</div>
              {#if src.name}
                <div class="ss">
                  {t('wizard.s1.count', {
                    files: src.files.toLocaleString('en-US'),
                    size: fmtBytes(src.bytes)
                  })}
                </div>
              {/if}
            </div>
            <button class="btn text sm" onclick={pickSource}>
              {src.name ? t('common.change') : t('wizard.s1.folder')}
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
                  {a.total > 0 ? `${fmtBytes(a.total - a.used)} free` : ''}
                </div>
              </label>
            {/each}
          </div>

          <div class="dest-block">
            <span class="hint">{t('wizard.s2.dest')}</span>
            <div class="breadcrumb">
              {#each app.destRoot.split('/').filter(Boolean) as seg, i (seg + i)}
                {#if i > 0}<span class="crumb-sep"><Icon name="chevron-right" size={16} /></span>{/if}
                <span class="crumb"><Icon name="folder" size={15} />{seg}</span>
              {/each}
              <span class="crumb-sep"><Icon name="chevron-right" size={16} /></span>
              <span class="crumb"><Icon name="folder-open" size={15} />{src.name || t('wizard.s1.none')}</span>
            </div>
            <button class="btn text sm" onclick={() => (folderPicker = true)}>
              {t('common.change')}
            </button>
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
              files: summaryFiles.toLocaleString('en-US'),
              size: fmtBytes(summaryBytes),
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
            <label class="mode" class:selected={mirror} class:disabled={!canMirror}>
              <input type="radio" name="wiz-mode" checked={mirror} disabled={!canMirror}
                onchange={() => setMirror(true)} />
              <div>
                <div class="mt">{t('wizard.s3.modeMirror')}</div>
                <div class="md">{t('wizard.s3.modeMirrorSub')}</div>
                {#if !canMirror}<div class="md">{t('wizard.s3.modeMirrorFolderOnly')}</div>{/if}
              </div>
            </label>
          </div>

          {#if mirror}
            <div class="mirror-warn">
              <Icon name="warning" size={20} />
              <div class="grow">
                <div class="wt">
                  {#if planLoading}
                    {t('wizard.s3.calculating')}
                  {:else if planError}
                    {planError}
                  {:else}
                    {t('wizard.s3.warnTitle', {
                      files: summaryDeleteCount.toLocaleString('en-US'),
                      size: fmtBytes(summaryDeleteBytes)
                    })}
                  {/if}
                </div>
                {#if planError}
                  <button class="btn text sm" style="padding-left:0;color:inherit" onclick={computePlan}>
                    {t('wizard.s3.planRetry')}
                  </button>
                {:else}
                  <button class="btn text sm" style="padding-left:0;color:inherit"
                    onclick={() => (showDeletions = !showDeletions)}>
                    {t('wizard.s3.warnView')}
                  </button>
                {/if}
                {#if showDeletions && plan?.deletions?.length}
                  <div style="max-height:150px;overflow:auto;margin:4px 0 8px;font:var(--type-body-s)">
                    {#each plan.deletions.slice(0, 100) as d (d.path)}
                      <div style="display:flex;justify-content:space-between;gap:12px;padding:2px 0">
                        <span class="truncate">{d.path}</span>
                        <span class="nums">{fmtBytes(d.size)}</span>
                      </div>
                    {/each}
                  </div>
                {/if}
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
                  <div class="ad fast-count">{t('wizard.s3.fastCount', { size: `${prefs.partSizeMB} MB` })}</div>
                </div>
                <input type="checkbox" class="switch" role="switch" bind:checked={app.fastFiles} />
              </div>
              <hr class="divider" />
              <div class="adv-row">
                <div class="al">
                  <div class="at">{t('wizard.adv.excludes')}</div>
                  <div class="ad">
                    {app.excludes.length
                      ? t('wizard.adv.excludesCount', { n: app.excludes.length })
                      : t('wizard.adv.excludesSub')}
                  </div>
                </div>
                <button class="btn text sm" onclick={openExcludes}>{t('common.change')}</button>
              </div>
              <div class="adv-row">
                <div class="al"><div class="at">{t('wizard.adv.speed')}</div></div>
                <span class="hint">{bwLabel()}</span>
              </div>
              <div class="adv-row">
                <div class="al">
                  <div class="at">{t('wizard.adv.parts')}</div>
                  <div class="ad">{t('wizard.adv.partsSub')}</div>
                </div>
                <span class="hint">{prefs.partSizeMB} MB</span>
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
          {:else if finishError}
            <button class="btn text" onclick={() => app.closeOverlay()}>{t('common.close')}</button>
            <button class="btn filled" onclick={retryBackup} disabled={actionBusy}>
              <Icon name="refresh" size={18} /><span>{t('common.retry')}</span>
            </button>
          {:else}
            <button class="btn tonal" onclick={togglePause} disabled={actionBusy}>
              <Icon name={jobPaused ? 'play-arrow' : 'pause'} size={18} />
              <span>{jobPaused ? t('activity.resume') : t('activity.pause')}</span>
            </button>
            {#if jobPaused}
              <span class="hint">{t('progress.pauseHint')}</span>
            {/if}
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
          <button class="btn filled" autofocus={!startDisabled} disabled={startDisabled || starting || (mirror && (planLoading || !!planError))}
            onclick={startBackup}>
            <Icon name="cloud-upload" size={18} /><span>{t('wizard.s3.start')}</span>
          </button>
        {/if}
      {/if}
    </div>
  </div>
</Modal>

{#if app.excludesOpen}
  <Modal labelledby="excl-title" onclose={() => (app.excludesOpen = false)}>
    <div class="dialog" style="width:560px">
      <div class="dialog-head" style="padding-bottom:2px">
        <div class="grow">
          <div class="dialog-title" id="excl-title">{t('wizard.adv.excludes')}</div>
        </div>
        <button class="icon-button" aria-label={t('common.close')} onclick={() => (app.excludesOpen = false)}>
          <Icon name="close" size={20} />
        </button>
      </div>
      <div class="dialog-body">
        <p class="hint" style="margin:4px 0 10px">{t('wizard.excludes.body')}</p>
        <textarea
          class="textfield"
          style="width:100%;min-height:132px;resize:vertical;padding:10px 12px;font-family:ui-monospace,Consolas,monospace;font-size:13px"
          bind:value={excludeText}
          spellcheck="false"
          placeholder={t('wizard.excludes.placeholder')}
        ></textarea>
        <p class="hint" style="margin-top:10px">{t('wizard.excludes.example')}</p>
      </div>
      <div class="dialog-foot">
        <div class="grow"></div>
        <button class="btn text" onclick={() => (app.excludesOpen = false)}>{t('common.cancel')}</button>
        <button class="btn filled" onclick={saveExcludes}>{t('common.save')}</button>
      </div>
    </div>
  </Modal>
{/if}

{#if folderPicker && currentAccount}
  <CloudFolderPicker
    accountID={currentAccount.id}
    initial={app.destRoot}
    onpick={(p) => {
      app.destRoot = p || '電腦備份';
      app.go(app.route);
    }}
    onclose={() => (folderPicker = false)}
  />
{/if}
