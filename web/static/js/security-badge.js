// Klever Node Hub - Security Check badge
// Shows the number of warnings from the dashboard security check on the
// "Security Check" sidebar entry, so a finding is visible on every page.

// Loaded from the shared sidebar, i.e. before the page's api.js: start on
// window load, once API exists.
window.addEventListener('load', function() {
    var badge = document.getElementById('sidebar-security-badge');
    if (!badge || typeof API === 'undefined') return;

    async function poll() {
        try {
            var d = await API.getJSON('/api/security/check');
            if (!d) return;
            var warns = (d.findings || []).filter(function(f) { return f.level === 'warn'; }).length;
            if (warns > 0) {
                badge.textContent = warns;
                badge.classList.add('visible');
            } else {
                badge.textContent = '';
                badge.classList.remove('visible');
            }
        } catch (e) { /* not logged in yet or offline */ }
    }

    // After the page's own start-up (token refresh) has run.
    setTimeout(poll, 1500);
    setInterval(poll, 60000);
});
