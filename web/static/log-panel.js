// Log panel: live-tail auto-scroll + copy to clipboard
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

        // Copy to clipboard
        if (copyBtn) {
            copyBtn.addEventListener('click', () => {
                const text = pre.textContent;
                navigator.clipboard.writeText(text).then(() => {
                    const orig = copyBtn.textContent;
                    copyBtn.textContent = 'Copied!';
                    setTimeout(() => { copyBtn.textContent = orig; }, 1500);
                });
            });
        }
    });
}
