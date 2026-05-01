// Log panel: live-tail auto-scroll + copy to clipboard + clear
document.addEventListener('DOMContentLoaded', () => {
    initLogPanels();
});

// Re-init after htmx swaps (for dynamically inserted build logs)
document.addEventListener('htmx:afterSettle', () => {
    initLogPanels();
});

function initLogPanels() {
    document.querySelectorAll('.log-panel').forEach(panel => {
        if (panel._logPanelInit) return;
        panel._logPanelInit = true;

        const pre = panel.querySelector('pre');
        const tailToggle = panel.querySelector('.log-tail-toggle');
        const copyBtn = panel.querySelector('.log-copy-btn');
        const clearBtn = panel.querySelector('.log-clear-btn');
        if (!pre) return;

        // Live tail: auto-scroll on new content
        let liveTail = tailToggle ? tailToggle.checked : true;

        if (tailToggle) {
            tailToggle.addEventListener('change', () => {
                liveTail = tailToggle.checked;
                if (liveTail) pre.scrollTop = pre.scrollHeight;
            });
        }

        const observer = new MutationObserver(() => {
            if (liveTail) pre.scrollTop = pre.scrollHeight;
        });
        observer.observe(pre, { childList: true, characterData: true, subtree: true });

        // Copy to clipboard with fallback
        if (copyBtn) {
            copyBtn.addEventListener('click', () => {
                const text = pre.textContent || pre.innerText;
                copyToClipboard(text).then(() => {
                    const orig = copyBtn.textContent;
                    copyBtn.textContent = 'Copied!';
                    setTimeout(() => { copyBtn.textContent = orig; }, 1500);
                }).catch(() => {
                    copyBtn.textContent = 'Failed';
                    setTimeout(() => { copyBtn.textContent = 'Copy'; }, 1500);
                });
            });
        }

        // Clear log contents (and the server-side buffer if data-clear-url is set)
        if (clearBtn) {
            const clearUrl = panel.dataset.clearUrl;
            clearBtn.addEventListener('click', () => {
                if (clearUrl) {
                    fetch(clearUrl, { method: 'DELETE' }).catch(() => {});
                }
                pre.textContent = '';
            });
        }
    });
}

// Copy text to clipboard with fallback for non-secure contexts
function copyToClipboard(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
        return navigator.clipboard.writeText(text);
    }
    // Fallback: create a temporary textarea
    return new Promise((resolve, reject) => {
        try {
            var ta = document.createElement('textarea');
            ta.value = text;
            ta.style.position = 'fixed';
            ta.style.left = '-9999px';
            document.body.appendChild(ta);
            ta.select();
            document.execCommand('copy');
            document.body.removeChild(ta);
            resolve();
        } catch (e) {
            reject(e);
        }
    });
}
