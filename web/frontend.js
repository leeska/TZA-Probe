// The public frontend is a pluggable document. Admin always uses the core UI.
const skin = (window.__tzaThemes || []).find((t) => t.id === document.documentElement.dataset.skin && t.type !== "addon");
if (!/^\/admin(?:\/|$)/.test(location.pathname) && skin?.frontend) {
  const { startFrontend } = await import("./theme-runtime.js");
  startFrontend(skin);
} else {
  await import("./app.js");
}
