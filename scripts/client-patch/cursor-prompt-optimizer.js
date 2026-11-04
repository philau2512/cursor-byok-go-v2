// cursor-prompt-optimizer.js
// Nhúng nút Cải thiện Prompt (/prompt-optimizer) vào Composer UI của Cursor Client

(function () {
  'use strict';

  const OPTIMIZER_API_URL = 'http://127.0.0.1:18090/api/prompt/optimize';
  const BUTTON_CLASS = 'cursor-prompt-opt-btn';

  function createOptimizerButton() {
    const btn = document.createElement('div');
    btn.className = `anysphere-icon-button bg-[transparent] border-none text-[var(--cursor-text-primary)] flex w-4 items-center justify-center ${BUTTON_CLASS}`;
    btn.style.marginLeft = '4px';
    btn.style.marginRight = '2px';
    btn.style.width = '20px';
    btn.style.height = '20px';
    btn.style.cursor = 'pointer';
    btn.style.position = 'relative';
    btn.style.userSelect = 'none';
    btn.setAttribute('title', 'Cải thiện Prompt (/prompt-optimizer)');
    btn.setAttribute('data-variant', 'background');

    const icon = document.createElement('span');
    icon.className = 'codicon codicon-sparkle !text-[16px]';
    btn.appendChild(icon);

    btn.addEventListener('click', async (e) => {
      e.preventDefault();
      e.stopPropagation();
      await handleOptimizePrompt(btn, icon);
    });

    return btn;
  }

  function extractPromptText(editor) {
    if (!editor) return '';
    try {
      const clone = editor.cloneNode(true);
      const mentions = clone.querySelectorAll('[data-mention-name]');
      mentions.forEach(m => {
        const name = m.getAttribute('data-mention-name');
        if (name) {
          m.replaceWith(document.createTextNode(` @${name} `));
        }
      });
      return clone.innerText.trim();
    } catch {
      return editor.innerText.trim();
    }
  }

  async function handleOptimizePrompt(btn, icon) {
    const composerBox = btn.closest('.ai-input-full-input-box') || btn.closest('.composer-input-blur-wrapper') || document;
    const editor = composerBox.querySelector('.aislash-editor-input');
    if (!editor) {
      showToast(btn, 'Không tìm thấy khung nhập prompt');
      return;
    }

    const rawPrompt = extractPromptText(editor);
    if (!rawPrompt) {
      showToast(btn, 'Vui lòng nhập nội dung prompt trước khi cải thiện!');
      return;
    }

    // Set loading state
    btn.style.pointerEvents = 'none';
    btn.style.opacity = '0.6';
    icon.className = 'codicon codicon-loading codicon-modifier-spin !text-[16px]';

    try {
      const resp = await fetch(OPTIMIZER_API_URL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: rawPrompt })
      });

      if (!resp.ok) {
        const errData = await resp.json().catch(() => ({}));
        throw new Error(errData.error || `Lỗi server HTTP ${resp.status}`);
      }

      const data = await resp.json();
      const optimized = (data.optimized_prompt || '').trim();
      if (!optimized) {
        throw new Error('Nội dung tối ưu trả về rỗng');
      }

      // Replace prompt text in Lexical editor
      const replaced = replaceEditorText(editor, optimized);
      if (!replaced) {
        console.warn('[PromptOptimizer] All replacement strategies completed with fallback.');
      }

      // Success feedback (flash check icon)
      icon.className = 'codicon codicon-check !text-[16px] text-green-500';
      showToast(btn, 'Đã tối ưu hóa prompt thành công!');
      setTimeout(() => {
        icon.className = 'codicon codicon-sparkle !text-[16px]';
      }, 1200);

    } catch (err) {
      console.error('[PromptOptimizer] Error:', err);
      icon.className = 'codicon codicon-error !text-[16px] text-red-500';
      showToast(btn, 'Lỗi: ' + (err.message || 'Không thể kết nối backend'));
      setTimeout(() => {
        icon.className = 'codicon codicon-sparkle !text-[16px]';
      }, 2000);
    } finally {
      btn.style.pointerEvents = 'auto';
      btn.style.opacity = '1';
    }
  }

  function replaceEditorText(editor, newText) {
    if (!editor) return false;

    // Strategy 1: Lexical Virtual DOM API via __lexicalEditor
    const lexical = editor.__lexicalEditor || editor.closest('[data-lexical-editor]')?.__lexicalEditor;
    if (lexical && typeof lexical.parseEditorState === 'function' && typeof lexical.setEditorState === 'function') {
      try {
        const lines = newText.split('\n');
        const children = lines.map(line => ({
          children: line ? [{
            detail: 0,
            format: 0,
            mode: 'normal',
            style: '',
            text: line,
            type: 'text',
            version: 1
          }] : [],
          direction: 'ltr',
          format: '',
          indent: 0,
          type: 'paragraph',
          version: 1
        }));
        const stateObj = {
          root: {
            children: children,
            direction: 'ltr',
            format: '',
            indent: 0,
            type: 'root',
            version: 1
          }
        };
        const parsed = lexical.parseEditorState(JSON.stringify(stateObj));
        lexical.setEditorState(parsed);
        if (typeof lexical.focus === 'function') {
          lexical.focus();
        }
        return true;
      } catch (e) {
        console.warn('[PromptOptimizer] Strategy 1 (lexical.setEditorState) failed:', e);
      }
    }

    // Strategy 2: Select All -> Delete existing nodes -> InsertText (clears contenteditable=false blocks first)
    try {
      editor.focus();
      document.execCommand('selectAll', false, null);
      document.execCommand('delete', false, null);
      if (document.execCommand('insertText', false, newText)) {
        return true;
      }
    } catch (e) {
      console.warn('[PromptOptimizer] Strategy 2 (execCommand selectAll+delete+insertText) failed:', e);
    }

    // Strategy 3: Simulated Clipboard Paste Event
    try {
      editor.focus();
      document.execCommand('selectAll', false, null);
      const dt = new DataTransfer();
      dt.setData('text/plain', newText);
      const pasteEvt = new ClipboardEvent('paste', {
        bubbles: true,
        cancelable: true,
        clipboardData: dt
      });
      editor.dispatchEvent(pasteEvt);
      return true;
    } catch (e) {
      console.warn('[PromptOptimizer] Strategy 3 (simulated paste event) failed:', e);
    }

    // Strategy 4: Direct DOM manipulation with input events as final fallback
    try {
      editor.innerHTML = '';
      const lines = newText.split('\n');
      lines.forEach((line) => {
        const p = document.createElement('p');
        p.setAttribute('dir', 'ltr');
        if (line === '') {
          p.appendChild(document.createElement('br'));
        } else {
          const span = document.createElement('span');
          span.setAttribute('data-lexical-text', 'true');
          span.textContent = line;
          p.appendChild(span);
        }
        editor.appendChild(p);
      });
      editor.dispatchEvent(new Event('input', { bubbles: true, cancelable: true }));
      editor.dispatchEvent(new Event('change', { bubbles: true, cancelable: true }));
      return true;
    } catch (e) {
      console.warn('[PromptOptimizer] Strategy 4 (DOM replacement) failed:', e);
    }

    return false;
  }

  function showToast(anchorElement, message) {
    const existing = document.querySelector('.cursor-prompt-opt-toast');
    if (existing) {
      existing.remove();
    }

    const toast = document.createElement('div');
    toast.className = 'cursor-prompt-opt-toast';
    toast.textContent = message;
    toast.style.position = 'fixed';
    toast.style.zIndex = '99999';
    toast.style.padding = '6px 12px';
    toast.style.fontSize = '12px';
    toast.style.color = '#ffffff';
    toast.style.backgroundColor = 'rgba(30, 30, 30, 0.95)';
    toast.style.border = '1px solid rgba(255, 255, 255, 0.15)';
    toast.style.borderRadius = '6px';
    toast.style.boxShadow = '0 4px 12px rgba(0, 0, 0, 0.3)';
    toast.style.pointerEvents = 'none';
    toast.style.transition = 'opacity 0.2s ease, transform 0.2s ease';
    toast.style.opacity = '0';
    toast.style.transform = 'translateY(4px)';

    const rect = anchorElement.getBoundingClientRect();
    toast.style.left = `${Math.max(10, rect.left - 80)}px`;
    toast.style.top = `${Math.max(10, rect.top - 36)}px`;

    document.body.appendChild(toast);
    requestAnimationFrame(() => {
      toast.style.opacity = '1';
      toast.style.transform = 'translateY(0)';
    });

    setTimeout(() => {
      toast.style.opacity = '0';
      toast.style.transform = 'translateY(-4px)';
      setTimeout(() => toast.remove(), 250);
    }, 2500);
  }

  function attachButtons() {
    const buttonAreas = document.querySelectorAll('.button-container.composer-button-area');
    buttonAreas.forEach((area) => {
      if (area.querySelector(`.${BUTTON_CLASS}`)) {
        return; // Already attached
      }

      const btn = createOptimizerButton();
      const sendWithMode = area.querySelector('.send-with-mode');
      if (sendWithMode) {
        area.insertBefore(btn, sendWithMode);
      } else {
        area.appendChild(btn);
      }
    });
  }

  // Observe DOM additions
  const observer = new MutationObserver(() => {
    attachButtons();
  });

  observer.observe(document.body, { childList: true, subtree: true });

  // Initial and heartbeat checks
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', attachButtons);
  } else {
    attachButtons();
  }
  setInterval(attachButtons, 1500);

  console.log('[PromptOptimizer] Injected successfully.');
})();
