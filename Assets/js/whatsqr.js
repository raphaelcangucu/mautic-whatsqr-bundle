(function () {
    "use strict";
    function mount() {
        const panel = document.getElementById("whatsqr-pair-status");
        if (!panel || panel.dataset.mounted) return;
        panel.dataset.mounted = "1";
        let lastHtml = "", failures = 0;
        const error = document.getElementById("whatsqr-refresh-error");
        async function update() {
            if (!panel.isConnected || panel.dataset.stage !== "waiting") return;
            if (!document.hidden) {
                const abort = new AbortController();
                const timeout = setTimeout(() => abort.abort(), 12000);
                try {
                    const response = await fetch(panel.dataset.statusUrl, {credentials: "same-origin", cache: "no-store", signal: abort.signal, headers: {"X-Requested-With": "XMLHttpRequest"}});
                    if (!response.ok) throw new Error("status");
                    const data = await response.json();
                    if (!panel.isConnected) return;
                    if (typeof data.html !== "string" || typeof data.stage !== "string") throw new Error("payload");
                    if (data.html !== lastHtml) { panel.innerHTML = data.html; lastHtml = data.html; }
                    panel.dataset.stage = data.stage;
                    failures = 0; error.hidden = true;
                } catch (_) {
                    failures++; error.textContent = panel.dataset.refreshError; error.hidden = false;
                } finally { clearTimeout(timeout); }
            }
            if (panel.isConnected && panel.dataset.stage === "waiting") setTimeout(update, Math.min(15000, 4000 * (1 + failures)));
        }
        setTimeout(update, 4000);
    }
    window.Mautic = window.Mautic || {};
    window.Mautic.whatsqrOnLoad = mount;
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", mount, {once: true}); else mount();
})();
