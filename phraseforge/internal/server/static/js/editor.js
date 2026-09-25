const pfImeCache = {};

async function pfLoadIme(code) {
  if (!code) return null;
  if (pfImeCache[code]) return pfImeCache[code];
  const res = await fetch(`/static/ime/${code}.json`);
  if (!res.ok) return null;
  const data = await res.json();
  pfImeCache[code] = data;
  return data;
}

// Applies script styling (font via css/<script>.css's class, RTL direction,
// enlarged size) unconditionally — this must hold whenever that script is
// selected, whether or not an IME preset happens to be configured for it.
function pfApplyScriptStyle(el, script, direction, enlarged) {
  if (!el) return;
  el.className = script || '';
  el.classList.toggle('editor-enlarged', !!enlarged);
  el.setAttribute('dir', direction || 'ltr');
}

// ime.js's own keydown handlers (ported as-is from deno-app, a single
// full-page textarea/code-editor context) hijack Tab to insert a literal
// tab character and preventDefault — reasonable there, but in phraseforge's
// forms (single-line inputs, multi-field forms) that traps focus in the
// field entirely, so Tab never reaches the next field. Wrapping instead of
// editing ime.js itself: skip the ported handler for Tab and let the
// browser do its normal thing (move focus), call through for every other key.
function pfSkipTabHijack(handler) {
  return function (event) {
    if (event.key === 'Tab') return;
    return handler.call(this, event);
  };
}

// Wires (or clears, if imeData is null) IME key handling onto one textarea.
// Never touches className/dir/enlarged — pfApplyScriptStyle already set
// those and owns them; this only adds/removes typing behavior on top.
function pfWireIme(el, imeData) {
  if (!el) return;
  el.insertText = insertText; // from ime.js

  if (!imeData) {
    el.ime = null;
    el.onkeydown = null;
    el.onkeypress = null;
    el.onkeyup = null;
    return;
  }

  switch (imeData.type) {
    case 'suffix':
      el.ime = compileImeSuffix(imeData.data);
      el.state = newStateSuffix();
      el.onkeydown = pfSkipTabHijack(onKeyDownSuffix);
      el.onkeypress = onKeyPressSuffix;
      el.onkeyup = onKeyUpSuffix;
      break;
    case 'table':
      el.ime = compileImeTable(imeData.data);
      el.meta = imeData.meta;
      el.state = newStateTable();
      el.onkeydown = pfSkipTabHijack(onKeyDownTable);
      el.onkeypress = onKeyPressTable;
      el.onkeyup = onKeyUpTable;
      break;
    default:
      el.ime = null;
      el.onkeydown = null;
      el.onkeypress = null;
      el.onkeyup = null;
  }
}

// Tracks the two independent things that decide whether the Transcribe
// button should be visible on a tabbed (texts/dialogs) editor: whether the
// currently selected language/script needs transcription at all (set async,
// by pfApplyImeConfig below) and which tab is currently active (set
// synchronously, by pfSwitchTab). Kept as module state rather than
// recomputed inline so either one changing — the language/script select, or
// a tab click — refreshes the same result. Meaningless on vocabulary/models'
// tabless single-flat-form pages; see pfRefreshActionVisibility's own guard.
let pfNeedsTranscription = false;
let pfActiveTabName = 'source';

// On a tabbed editor (texts/dialogs — has .editor-tab-btn elements), scopes
// each action to the one tab it belongs to: Transcribe only appears on the
// Transcription tab (and only when this language/script actually needs
// one), Translate and its target-language select only on the Translation
// tab. Save is unscoped — it stays visible on every tab, untouched here.
// On a tabless editor (vocabulary/models item form), there is no tab to
// scope by, so this is a no-op — those pages keep the Transcribe button
// following needs_transcription alone, matching their existing behavior.
function pfRefreshActionVisibility(name) {
  if (document.querySelectorAll('.editor-tab-btn').length === 0) return;
  // layout.html's own `.transcribe-action { display: none; }` rule (a class
  // selector) beats `pf-button { display: inline-block; }` (a type
  // selector) by specificity — setting style.display to '' only clears the
  // inline override and falls back to the stylesheet, which still says
  // none. Must set an explicit non-empty value to actually win; only an
  // inline style (any non-empty value) can override a same-importance
  // stylesheet rule, specificity or not. See the same-shaped pf-field/
  // [hidden] gotcha found in admin-app.js.
  document.querySelectorAll('.transcribe-action').forEach((button) => {
    button.style.display = name === 'transcription' && pfNeedsTranscription ? 'inline-block' : 'none';
  });
  const translateBtn = document.getElementById('translateBtn');
  if (translateBtn) translateBtn.style.display = name === 'translation' ? '' : 'none';
  const translationTarget = document.getElementById('translation-target');
  if (translationTarget) translationTarget.style.display = name === 'translation' ? '' : 'none';
}

// Looks up the script's direction/enlarged treatment and the admin-
// configured source/transcription IME for the currently selected
// language+script, then applies both to the matching fields. Also shows/
// hides the Transcription tab based on needs_transcription.
async function pfApplyImeConfig() {
  const languageSel = document.getElementById('language');
  const scriptSel = document.getElementById('script');
  if (!languageSel || !scriptSel) return;

  const res = await fetch(`/ime-config?language=${encodeURIComponent(languageSel.value)}&script=${encodeURIComponent(scriptSel.value)}`);
  const cfg = res.ok
    ? await res.json()
    : { source_ime: '', transcription_ime: '', needs_transcription: false, script: scriptSel.value, direction: 'ltr', enlarged: false };

  const sourceEl = document.getElementById('field-source');
  const transcriptionEl = document.getElementById('field-transcription');

  // Styling always applies for the selected script — independent of
  // whether an IME preset has been configured for it.
  pfApplyScriptStyle(sourceEl, cfg.script, cfg.direction, cfg.enlarged);
  // Transcription is cli-tools' "pinned Latin/LTR romanization" — always
  // plain Latin styling, regardless of the source script.
  pfApplyScriptStyle(transcriptionEl, 'latn', 'ltr', false);

  pfWireIme(sourceEl, await pfLoadIme(cfg.source_ime));
  pfWireIme(transcriptionEl, await pfLoadIme(cfg.transcription_ime));

  // texts/dialogs show/hide a Transcription tab; vocabulary/models show/hide
  // the whole labeled field (they have no tabs) — same needs_transcription
  // flag drives both, whichever one the current page has.
  const transcriptionTab = document.getElementById('tab-btn-transcription');
  if (transcriptionTab) {
    transcriptionTab.style.display = cfg.needs_transcription ? '' : 'none';
  }
  const transcriptionGroup = document.getElementById('transcription-group');
  if (transcriptionGroup) {
    transcriptionGroup.style.display = cfg.needs_transcription ? '' : 'none';
  }
  pfNeedsTranscription = cfg.needs_transcription;
  if (document.querySelectorAll('.editor-tab-btn').length === 0) {
    // Tabless (vocabulary/models): no tab to scope by, so the Transcribe
    // button just follows needs_transcription directly, as it always has.
    // 'inline-block', not '' — see pfRefreshActionVisibility's comment on
    // why an empty string can't override layout.html's own
    // `.transcribe-action { display: none; }` rule. This was a real,
    // pre-existing bug on this tabless path too, not just the tabbed one.
    document.querySelectorAll('.transcribe-action').forEach((button) => {
      button.style.display = cfg.needs_transcription ? 'inline-block' : 'none';
    });
  } else {
    pfRefreshActionVisibility(pfActiveTabName);
  }
}

function pfSwitchTab(name) {
  pfActiveTabName = name;
  document.querySelectorAll('.editor-tab-btn').forEach((btn) => {
    btn.classList.toggle('active', btn.dataset.tab === name);
  });
  document.querySelectorAll('.editor-tab-pane').forEach((pane) => {
    pane.style.display = pane.id === `tab-${name}` ? '' : 'none';
  });
  pfRefreshActionVisibility(name);
}

function pfInitEditor() {
  const languageSel = document.getElementById('language');
  const scriptSel = document.getElementById('script');
  if (languageSel) languageSel.addEventListener('change', pfApplyImeConfig);
  if (scriptSel) scriptSel.addEventListener('change', pfApplyImeConfig);

  // Reset to the default tab synchronously, before pfApplyImeConfig's fetch
  // resolves, so a tabbed editor never flashes Transcribe/Translate/the
  // target-language select visible on the Source tab for one frame.
  pfActiveTabName = 'source';
  pfRefreshActionVisibility(pfActiveTabName);

  if (languageSel && scriptSel) pfApplyImeConfig();

  document.querySelectorAll('.editor-tab-btn').forEach((btn) => {
    btn.addEventListener('click', () => pfSwitchTab(btn.dataset.tab));
  });
}

document.addEventListener('DOMContentLoaded', pfInitEditor);
