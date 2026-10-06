(function () {
    "use strict";
    const mautic = window.Mautic = window.Mautic || {};
    if (typeof mautic.whatsqrOnUnload === "function") mautic.whatsqrOnUnload();
    let dispose = null;

    function mount() {
        const panel = document.getElementById("whatsqr-pair-status");
        if (!panel || panel.dataset.mounted) return;
        if (dispose) dispose();
        panel.dataset.mounted = "1";
        const indicator = document.getElementById("whatsqr-live-status");
        const error = document.getElementById("whatsqr-refresh-error");
        let source = null, retry = null, failures = 0, stopped = false;
        let version = panel.dataset.version || "", lastHtml = panel.innerHTML;

        function status(state, message) {
            panel.dataset.liveState = state;
            if (indicator) {
                indicator.dataset.state = state;
                indicator.querySelector("span").textContent = message;
            }
        }
        function close() {
            if (source) { source.close(); source = null; }
            if (retry !== null) { clearTimeout(retry); retry = null; }
        }
        function schedule(delay) {
            close();
            if (!stopped && panel.isConnected && !document.hidden) retry = setTimeout(connect, delay);
        }
        function unavailable() {
            failures++;
            status("reconnecting", panel.dataset.connectingLabel);
            if (error) { error.textContent = panel.dataset.refreshError; error.hidden = false; }
            schedule(Math.min(30000, 5000 * Math.pow(2, Math.min(failures - 1, 3))));
        }
        function connect() {
            close();
            if (stopped || !panel.isConnected || document.hidden) return;
            if (typeof window.EventSource !== "function") { unavailable(); return; }
            const url = new URL(panel.dataset.eventsUrl, window.location.href);
            if (version) url.searchParams.set("version", version);
            const current = source = new EventSource(url.href);
            current.addEventListener("live", function () {
                if (source !== current) return;
                failures = 0;
                status("live", panel.dataset.liveLabel);
                if (error) error.hidden = true;
            });
            current.addEventListener("pairing", function (event) {
                if (source !== current || !panel.isConnected) return;
                try {
                    const data = JSON.parse(event.data);
                    if (!data || typeof data.html !== "string" || !["ready", "waiting", "connected", "reconnecting", "not_done"].includes(data.stage)) throw new Error("payload");
                    // Keep focus and layout intact for repeated snapshots/reconnects.
                    if (data.html !== lastHtml) { panel.innerHTML = data.html; lastHtml = data.html; }
                    panel.dataset.stage = data.stage;
                    if (event.lastEventId) { version = event.lastEventId; panel.dataset.version = version; }
                } catch (_) { unavailable(); }
            });
            current.addEventListener("rotate", function () {
                if (source === current) schedule(250);
            });
            current.addEventListener("unavailable", function () {
                if (source === current) unavailable();
            });
            current.onerror = function () { if (source === current) unavailable(); };
        }
        function visibility() {
            if (document.hidden) {
                close(); status("paused", panel.dataset.pausedLabel);
            } else { connect(); }
        }
        function pagehide() { close(); }
        function resume() { if (!document.hidden) connect(); }
        const observer = new MutationObserver(function () { if (!panel.isConnected) cleanup(); });
        observer.observe(document.body, {childList: true, subtree: true});
        document.addEventListener("visibilitychange", visibility);
        window.addEventListener("online", resume);
        window.addEventListener("pagehide", pagehide);
        window.addEventListener("pageshow", resume);
        function cleanup() {
            stopped = true; close(); observer.disconnect();
            document.removeEventListener("visibilitychange", visibility);
            window.removeEventListener("online", resume);
            window.removeEventListener("pagehide", pagehide);
            window.removeEventListener("pageshow", resume);
            delete panel.dataset.mounted;
        }
        dispose = cleanup;
        if (document.hidden) visibility(); else connect();
    }
    mautic.whatsqrOnLoad = mount;
    mautic.whatsqrOnUnload = function () { if (dispose) { dispose(); dispose = null; } };
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", mount, {once: true}); else mount();
})();
