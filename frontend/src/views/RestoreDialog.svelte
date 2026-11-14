<script lang="ts">
  import Icon from '$lib/components/Icon.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { app } from '$lib/state.svelte';
  import { t, fmtBytes } from '$lib/i18n';
  import { restore } from '$lib/mock';
</script>

<Modal labelledby="restore-title" onclose={() => app.closeOverlay()}>
  <div class="dialog restore-dialog">
    <div class="dialog-head" style="padding-bottom:2px">
      <div class="grow">
        <div class="dialog-title" id="restore-title">{t('restore.title', { name: restore.name })}</div>
      </div>
      <button class="icon-button" aria-label={t('common.close')} onclick={() => app.closeOverlay()}>
        <Icon name="close" size={20} />
      </button>
    </div>

    <div class="dialog-body">
      <div class="restore-file">
        <span class="rtile"><Icon name="movie" size={28} /></span>
        <div class="grow">
          <div class="rn">{restore.name}</div>
          <div class="rs">{t('restore.sub', { size: fmtBytes(restore.size), parts: restore.parts })}</div>
          <div class="rs">{restore.remotePath} · {t('clouds.connected')}</div>
        </div>
        <span class="status ok"><span class="dot"></span>{t('files.split.ready')}</span>
      </div>

      <div class="restore-where">{t('restore.where')}</div>
      <div class="restore-dest">
        <button class="chip selected"><Icon name="folder" size={16} />{t('restore.destName')}</button>
        <button class="chip interactive"><Icon name="image" size={16} />{t('files.root.pictures')}</button>
        <button class="chip interactive"><Icon name="computer" size={16} />{t('files.root.desktop')}</button>
        <button class="chip interactive"><Icon name="folder-open" size={16} />{t('restore.other')}</button>
      </div>
      <div class="rs" style="margin-top:6px;color:var(--m3c-on-surface-variant)">
        {t('restore.free', { free: fmtBytes(restore.free) })}
      </div>

      <div class="restore-progress">
        <span class="bar"><i style="width:100%"></i></span>
      </div>
      <div class="restore-done">
        <Icon name="check-circle" size={22} />
        <div>
          <div class="dt">{t('restore.done')}</div>
          <div class="db">
            {t('restore.doneBody', { name: restore.name, dest: t('restore.destName') })}
          </div>
        </div>
      </div>
    </div>

    <div class="dialog-foot">
      <button class="btn text" onclick={() => app.notify(t('restore.destName'))}>
        <Icon name="folder-open" size={18} /><span>{t('common.view')} {t('restore.destName')}</span>
      </button>
      <div class="grow"></div>
      <!-- svelte-ignore a11y_autofocus -->
      <button class="btn filled" autofocus onclick={() => app.closeOverlay()}>{t('common.done')}</button>
    </div>
  </div>
</Modal>
