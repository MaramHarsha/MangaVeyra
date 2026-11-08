<script lang="ts">
 // D7 review drawer: comments on this page's panels with their status,
 // replies, the roles on the project and "Approve page N".
 import {tick, untrack} from 'svelte';
 import Button from './Button.svelte';
 import {avatarTone, initials} from './document';
 import {ASSIGNABLE_ROLES, FILTERS, authorLine, filterComments, roleLine, scopeChoices, scopeFor, scopeKind, statusTone, type Filter, type Member, type ReviewComment, type Status} from './collab';
 import type {ReviewStore} from './review.svelte';
 import {showToast} from '../../lib/toast';

 let {store, pageNumber, pageVersion, panelID = '', panelLabel = '', compose = 0, focus = '', lang, t, onClose, onFocus, onConfirm}: {
  store: ReviewStore; pageNumber: number; pageVersion: number; panelID?: string; panelLabel?: string; compose?: number; focus?: string; lang: string;
  t: (key: string, args?: Record<string, string | number>) => string; onClose: () => void; onFocus: (id: string) => void;
  // onConfirm asks through the editor's confirmation dialog.
  onConfirm: (title: string, body: string, action: () => void, label: string) => void;
 } = $props();

 let filter = $state<Filter>('open');
 let composing = $state(''), draft = $state(''), assignee = $state('');
 let replying = $state(''), replyDraft = $state('');
 let statusMenu = $state('');
 let editing = $state(''), editRole = $state(''), editKind = $state('all'), editFrom = $state(1), editTo = $state(1);
 let adding = $state(false), addName = $state(''), addRole = $state('viewer');
 let list: HTMLElement | undefined = $state();
 let composer: HTMLTextAreaElement | undefined = $state();

 const data = $derived(store.data);
 const viewer = $derived(data?.viewer);
 const shown = $derived(data ? filterComments(data.comments, filter) : []);
 const approval = $derived(data?.approval);
 // The approval holds while the page is still the version that was approved.
 const approvedNow = $derived(!!approval && approval.version === pageVersion);
 const others = $derived(data ? data.members.filter(m => m.user_id !== data.viewer.id) : []);
 const dateTime = $derived(new Intl.DateTimeFormat(lang, {dateStyle: 'medium', timeStyle: 'short'}));

 // The canvas toolbar's Comment button (compose increments) opens the composer for the selected panel.
 $effect(() => { if (compose > 0) untrack(() => startComment()); });
 // A pin click focuses its comment.
 $effect(() => { const id = focus; if (id) untrack(() => void tick().then(() => list?.querySelector<HTMLElement>(`[data-comment="${id}"]`)?.focus())); });

 function startComment() {
  if (!panelID || !viewer?.can_comment) return;
  filter = 'open'; composing = panelID; draft = ''; assignee = '';
  void tick().then(() => composer?.focus());
 }
 async function post() {
  const body = draft.trim();
  if (!body || !composing) return;
  if (await store.comment(composing, body, assignee)) { composing = ''; draft = ''; }
 }
 async function sendReply(c: ReviewComment) {
  const body = replyDraft.trim();
  if (!body) return;
  if (await store.reply(c.id, body)) { replyDraft = ''; replying = ''; }
 }
 async function setStatus(c: ReviewComment, s: Status) {
  statusMenu = '';
  await store.status(c.id, s);
 }
 async function approve() {
  if (await store.approve(pageVersion)) showToast({kind: 'success', title: t('collab.approve.done', {number: pageNumber})});
 }
 function edit(m: Member) {
  editing = editing === m.user_id ? '' : m.user_id;
  editRole = m.role; editKind = scopeKind(m.scope); editFrom = m.scope.page_from || 1; editTo = m.scope.page_to || pageNumber;
 }
 async function saveMember(m: Member) {
  if (await store.updateMember(m.user_id, editRole, scopeFor(scopeChoices(editRole).includes(editKind) ? editKind : 'all', editFrom, editTo))) {
   editing = '';
   showToast({kind: 'success', title: t('collab.members.saved')});
  }
 }
 function removeMember(m: Member) {
  onConfirm(t('collab.members.remove'), t('collab.members.remove_body', {name: m.name}), () => void store.removeMember(m.user_id).then(ok => { if (ok) editing = ''; }), t('collab.members.remove'));
 }
 async function addMember() {
  const name = addName.trim();
  if (!name) return;
  if (await store.addMember(name, addRole, {})) { showToast({kind: 'success', title: t('collab.members.added', {name})}); addName = ''; adding = false; }
 }
 function menuKey(e: KeyboardEvent) {
  const items = Array.from((e.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="menuitem"]'));
  const i = items.indexOf(document.activeElement as HTMLElement);
  if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); const id = statusMenu; statusMenu = ''; list?.querySelector<HTMLElement>(`[data-status="${id}"]`)?.focus(); }
  else if (e.key === 'ArrowDown') { e.preventDefault(); items[(i + 1) % items.length]?.focus(); }
  else if (e.key === 'ArrowUp') { e.preventDefault(); items[(i - 1 + items.length) % items.length]?.focus(); }
 }
 function openMenu(id: string) { statusMenu = statusMenu === id ? '' : id; if (statusMenu) void tick().then(() => list?.querySelector<HTMLElement>(`[data-menu="status-${id}"] [role="menuitem"]`)?.focus()); }
</script>

<svelte:window onpointerdown={(e) => { if (statusMenu && !(e.target instanceof Element && e.target.closest('.mv-review__status-wrap'))) statusMenu = ''; }}/>
<div class="mv-review" aria-busy={store.busy || store.loading}>
 <div class="mv-review__head">
  <div><strong>{t('collab.review.title', {number: pageNumber})}</strong><span>{data ? t('collab.review.counts', {open: data.counts.open, resolved: data.counts.resolved}) : t('collab.review.loading')}</span></div>
  {#if viewer?.can_comment && panelID}<button type="button" class="mv-iconlink mv-review__new" aria-pressed={composing === panelID} aria-label={t('collab.comment.new', {panel: panelLabel})} title={t('collab.comment.new', {panel: panelLabel})} onclick={startComment}><i class="ti ti-message-plus" aria-hidden="true"></i></button>{/if}
  <button type="button" class="mv-iconlink" aria-label={t('collab.review.close')} title={t('collab.review.close')} onclick={onClose}><i class="ti ti-x" aria-hidden="true"></i></button>
 </div>
 {#if store.error}
  <div class="mv-notice mv-notice--error mv-review__notice" role="alert"><i class="ti ti-alert-triangle" aria-hidden="true"></i><div><p>{store.error}</p>{#if !data}<Button small onclick={() => void store.load(store.pageID)}>{t('collab.review.retry')}</Button>{/if}</div></div>
 {/if}
 <div class="mv-review__scroll" bind:this={list}>
  <div class="mv-seg mv-seg--full mv-review__filters" role="group" aria-label={t('collab.filter.label')}>
   {#each FILTERS as f}<button type="button" class="mv-seg__item" aria-pressed={filter === f} onclick={() => filter = f}>{t(`collab.filter.${f}`)}</button>{/each}
  </div>
  {#if composing}
   <form class="mv-review__composer" onsubmit={(e) => { e.preventDefault(); void post(); }}>
    <label class="mv-field"><span class="mv-field__label">{t('collab.comment.label', {panel: data?.comments.find(c => c.panel_id === composing)?.label || panelLabel})}</span><textarea bind:this={composer} class="mv-textarea resize-none" rows="3" maxlength="2000" bind:value={draft} onkeydown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); composing = ''; } if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); void post(); } }}></textarea></label>
    {#if others.length}
     <label class="mv-field"><span class="mv-field__label">{t('collab.comment.assign')}</span><span class="mv-select-wrap"><select class="mv-select" bind:value={assignee}><option value="">{t('collab.comment.unassigned')}</option>{#each others as m (m.user_id)}<option value={m.user_id}>{m.name} · {t(`collab.role.${m.role}`)}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span></label>
    {/if}
    <div class="mv-review__buttons"><Button small variant="quiet" onclick={() => composing = ''}>{t('collab.cancel')}</Button><Button small variant="primary" type="submit" busy={store.busy} disabled={!draft.trim()}>{t('collab.comment.post')}</Button></div>
   </form>
  {/if}
  {#each shown as c (c.id)}
   <article class="mv-review__item" class:focused={focus === c.id} data-comment={c.id} tabindex="-1" aria-label={`${c.label} · ${authorLine(c.author, c.agent, t)}`}>
    <div class="mv-review__who">
     <span class={`mv-avatar mv-review__avatar mv-avatar--c${avatarTone(c.author.id || c.id)}`} aria-hidden="true">{initials(c.author.name || '?')}</span>
     <strong>{authorLine(c.author, c.agent, t)}</strong>
     <button type="button" class="mv-review__label" title={t('collab.pin', {number: shown.indexOf(c) + 1, panel: c.label})} onclick={() => onFocus(c.id)}>{c.label}</button>
    </div>
    <p class="mv-review__body">{c.body}</p>
    {#if c.assignee}<p class="mv-review__meta">{t('collab.assigned', {name: c.assignee.name})}</p>{/if}
    <div class="mv-review__row">
     {#if c.next.length}
      <span class="mv-review__status-wrap">
       <button type="button" class={`mv-badge mv-badge--${statusTone(c.status)} mv-review__status`} data-status={c.id} aria-haspopup="menu" aria-expanded={statusMenu === c.id} aria-label={`${t('collab.status.change', {panel: c.label})}: ${t(`collab.status.${c.status}`)}`} disabled={store.busy} onclick={() => openMenu(c.id)}>{t(`collab.status.${c.status}`)}<i class="ti ti-chevron-down" aria-hidden="true"></i></button>
       {#if statusMenu === c.id}
        <div class="mv-menu mv-review__menu" role="menu" tabindex="-1" data-menu={`status-${c.id}`} aria-label={t('collab.status.change', {panel: c.label})} onkeydown={menuKey}>
         {#each c.next as s}<button type="button" class="mv-menu__item" role="menuitem" onclick={() => void setStatus(c, s)}><span class={`mv-review__dot mv-review__dot--${statusTone(s)}`} aria-hidden="true"></span>{t(`collab.action.${s}`)}</button>{/each}
        </div>
       {/if}
      </span>
     {:else}
      <span class={`mv-badge mv-badge--${statusTone(c.status)}`}>{t(`collab.status.${c.status}`)}</span>
     {/if}
     {#if viewer?.can_comment}<button type="button" class="mv-review__reply" aria-expanded={replying === c.id} aria-label={t('collab.reply.label', {name: c.author.name})} onclick={() => { replying = replying === c.id ? '' : c.id; replyDraft = ''; }}>{c.replies.length ? `${t('collab.reply')} · ${c.replies.length}` : t('collab.reply')}</button>{/if}
    </div>
    {#if replying === c.id || (c.replies.length && focus === c.id)}
     <ol class="mv-review__thread">
      {#each c.replies as r (r.id)}
       <li><span class={`mv-avatar mv-avatar--xs mv-avatar--c${avatarTone(r.author.id || r.id)}`} aria-hidden="true">{initials(r.author.name || '?')}</span><div><strong>{authorLine(r.author, r.agent, t)}</strong><p>{r.body}</p></div></li>
      {/each}
     </ol>
    {/if}
    {#if replying === c.id}
     <form class="mv-review__composer mv-review__composer--reply" onsubmit={(e) => { e.preventDefault(); void sendReply(c); }}>
      <label class="mv-sr-only" for={`reply-${c.id}`}>{t('collab.reply.label', {name: c.author.name})}</label>
      <textarea id={`reply-${c.id}`} class="mv-textarea resize-none" rows="2" maxlength="2000" bind:value={replyDraft} onkeydown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); replying = ''; } if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); void sendReply(c); } }}></textarea>
      <div class="mv-review__buttons"><Button small variant="quiet" onclick={() => replying = ''}>{t('collab.cancel')}</Button><Button small variant="primary" type="submit" busy={store.busy} disabled={!replyDraft.trim()}>{t('collab.reply')}</Button></div>
     </form>
    {/if}
   </article>
  {:else}
   {#if data}<p class="mv-review__empty">{t(`collab.empty.${filter}`)}</p>{/if}
  {/each}
  {#if data}
   <section class="mv-review__roles" aria-labelledby="mv-review-roles">
    <strong id="mv-review-roles">{t('collab.roles.title')}</strong>
    <ul>
     {#each data.members as m (m.user_id)}
      <li>
       <div class="mv-review__role"><span>{m.name}</span><span class="mv-review__role-line">{roleLine(m, t)}</span>
        {#if viewer?.can_manage && m.role !== 'owner'}<button type="button" class="mv-iconlink mv-review__edit" aria-expanded={editing === m.user_id} aria-label={t('collab.members.edit', {name: m.name})} title={t('collab.members.edit', {name: m.name})} onclick={() => edit(m)}><i class="ti ti-pencil" aria-hidden="true"></i></button>{/if}
       </div>
       {#if editing === m.user_id}
        <form class="mv-review__member" onsubmit={(e) => { e.preventDefault(); void saveMember(m); }}>
         <label class="mv-field"><span class="mv-field__label">{t('collab.members.role')}</span><span class="mv-select-wrap"><select class="mv-select" bind:value={editRole}>{#each ASSIGNABLE_ROLES as r}<option value={r}>{t(`collab.role.${r}`)}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span></label>
         <label class="mv-field"><span class="mv-field__label">{t('collab.members.scope')}</span><span class="mv-select-wrap"><select class="mv-select" bind:value={editKind}>{#each scopeChoices(editRole) as k}<option value={k}>{t(`collab.scope.option.${k}`)}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span></label>
         {#if editKind === 'pages' && scopeChoices(editRole).includes('pages')}
          <div class="mv-ed__field-pair"><label class="mv-field"><span class="mv-field__label">{t('collab.scope.from')}</span><input class="mv-input" type="number" min="1" max="256" bind:value={editFrom}/></label><label class="mv-field"><span class="mv-field__label">{t('collab.scope.to')}</span><input class="mv-input" type="number" min="1" max="256" bind:value={editTo}/></label></div>
         {/if}
         <div class="mv-review__buttons"><Button small variant="danger" onclick={() => removeMember(m)}>{t('collab.members.remove')}</Button><span class="mv-review__spacer"></span><Button small variant="quiet" onclick={() => editing = ''}>{t('collab.cancel')}</Button><Button small variant="primary" type="submit" busy={store.busy}>{t('collab.members.save')}</Button></div>
        </form>
       {/if}
      </li>
     {/each}
    </ul>
    {#if viewer?.can_manage}
     {#if adding}
      <form class="mv-review__member" onsubmit={(e) => { e.preventDefault(); void addMember(); }}>
       <label class="mv-field"><span class="mv-field__label">{t('collab.members.username')}</span><input class="mv-input" autocomplete="off" spellcheck="false" bind:value={addName}/></label>
       <label class="mv-field"><span class="mv-field__label">{t('collab.members.role')}</span><span class="mv-select-wrap"><select class="mv-select" bind:value={addRole}>{#each ASSIGNABLE_ROLES as r}<option value={r}>{t(`collab.role.${r}`)}</option>{/each}</select><i class="ti ti-chevron-down" aria-hidden="true"></i></span></label>
       <div class="mv-review__buttons"><Button small variant="quiet" onclick={() => adding = false}>{t('collab.cancel')}</Button><Button small variant="primary" type="submit" busy={store.busy} disabled={!addName.trim()}>{t('collab.members.add')}</Button></div>
      </form>
     {:else}
      <button type="button" class="mv-review__add" onclick={() => adding = true}><i class="ti ti-user-plus" aria-hidden="true"></i>{t('collab.members.add')}</button>
     {/if}
    {/if}
   </section>
  {/if}
 </div>
 <div class="mv-review__foot">
  {#if approval}<p class="mv-review__meta">{approvedNow ? t('collab.approved', {name: approval.by.name, time: dateTime.format(new Date(approval.at))}) : t('collab.approved_changed', {name: approval.by.name})}</p>{/if}
  <span title={viewer && !viewer.can_review ? t('collab.error.forbidden') : undefined}><Button full variant="primary" icon="check" busy={store.busy} disabled={!data || !viewer?.can_review || approvedNow} onclick={() => void approve()}>{t('collab.approve', {number: pageNumber})}</Button></span>
 </div>
</div>
