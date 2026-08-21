// The backup page: what a backup would hold, the download, and the restore.
//
// The counts come from GET /api/backup rather than being written into the page,
// so the day somebody adds a table to the catalogue the line says so without
// being edited.
//
// The file itself never goes through the store. Everything else on the
// dashboard is a value two pages might share; this is a download somebody asked
// for once, and caching it would mean a backup taken from a page that had been
// open since Tuesday.
import { adminHeaders, el, muted, number, qs, request } from "./core.js";
import { ensure, forgetAll } from "./store.js";
import { ask } from "./cluster.js";

function status(message) {
  const node = qs("[data-backup-status]");
  if (node) node.textContent = message;
}

function restoreStatus(message) {
  const node = qs("[data-backup-restore-status]");
  if (node) node.textContent = message;
}

// One line, not a list. What a person decides from here is whether the file is
// worth taking — how much configuration there is and how many credentials are
// in it — and an export is all of it or none of it, so a table of sections
// reads like a chooser that does not exist.
function drawSummary(summary) {
  const host = qs("[data-backup-sections]");
  if (!host) return;
  const rows = (summary.tables || []).reduce((total, table) => total + table.rows, 0);
  const sections = (summary.tables || []).filter((table) => table.rows > 0).length;
  const sealed = summary.sealed || 0;
  host.textContent = `${number.format(rows)} row${rows === 1 ? "" : "s"} across ` +
    `${sections} section${sections === 1 ? "" : "s"}, including ${number.format(sealed)} ` +
    `stored credential${sealed === 1 ? "" : "s"} — sealed under your passphrase, or written plainly without one.`;
}

export async function refreshBackup() {
  const host = qs("[data-backup-sections]");
  if (!host) return;
  try {
    await ensure("backup", () => request("/api/backup", { headers: adminHeaders() }), drawSummary);
  } catch (failure) {
    host.textContent = failure.message;
  }
}

// The download. A blob and a synthetic click, because the file comes back from
// a POST carrying a passphrase — a plain link would have to put it in a URL,
// where it would land in the access log of every proxy in front of guard.
function save(doc) {
  const stamp = new Date().toISOString().slice(0, 19).replaceAll(":", "-");
  const blob = new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `guard-backup-${stamp}.json`;
  document.body.append(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

async function exportBackup() {
  const passphrase = qs("[data-backup-passphrase]")?.value || "";
  status("Reading the configuration…");
  try {
    const doc = await request("/api/backup", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ passphrase }),
    });
    save(doc);
    const rows = (doc.tables || []).reduce((total, table) => total + table.rows.length, 0);
    // Anything the export could not carry travels with it as a note, and is
    // said here rather than left in the file — today that is the stored
    // configuration, which is nothing but sealed values and so cannot go
    // without a passphrase.
    status([
      `${number.format(rows)} rows saved.`,
      doc.secrets === "passphrase"
        ? "Credentials are sealed with your passphrase."
        : "Credentials are in the file as they are — keep it somewhere you would keep a password.",
      ...(doc.notes || []),
    ].join(" "));
  } catch (failure) {
    status(failure.message);
  }
}

// What came back from a restore: the counts that say it landed, and any
// warning, which is the only part somebody has to act on.
function drawReport(report) {
  const host = qs("[data-backup-report]");
  if (!host) return;
  const box = el("div", "rounded-lg border border-border bg-background/40 p-3 space-y-1");
  box.append(el("p", "text-sm",
    `${number.format(report.rows)} row${report.rows === 1 ? "" : "s"} restored from a backup taken by ${report.guard_version || "an unknown build"}.`));
  box.append(el("p", `text-xs ${muted}`,
    `${number.format(report.sealed || 0)} credential${report.sealed === 1 ? "" : "s"} re-sealed with this instance's key` +
    (report.blank ? `, ${number.format(report.blank)} left empty for you to set again.` : ".")));
  for (const warning of report.warnings || []) {
    box.append(el("p", "text-xs text-destructive", warning));
  }
  if (report.restart) {
    box.append(el("p", `text-xs ${muted}`,
      "Stored settings are read at startup, so they take a restart to come into force."));
  }
  host.replaceChildren(box);
}

async function restoreBackup() {
  const input = qs("[data-backup-file]");
  const file = input?.files?.[0];
  if (!file) {
    restoreStatus("Choose a backup file first.");
    return;
  }
  let doc;
  try {
    doc = JSON.parse(await file.text());
  } catch {
    restoreStatus("That file is not JSON, so it is not a Guard backup.");
    return;
  }
  const sections = (doc.tables || []).length;
  const rows = (doc.tables || []).reduce((total, table) => total + (table.rows?.length || 0), 0);
  const taken = doc.created_ns ? new Date(doc.created_ns / 1e6).toLocaleString() : "an unknown time";
  if (!await ask({
    title: "Replace this instance's configuration?",
    body: `${sections} section${sections === 1 ? "" : "s"} and ${number.format(rows)} row${rows === 1 ? "" : "s"}, ` +
      `taken by ${doc.guard_version || "an unknown build"} at ${taken}. ` +
      "Everything on this instance that is not in the file is removed — machines, commands, rules, secrets, members.",
    detail: doc.secrets === "passphrase"
      ? "The file carries credentials, sealed with a passphrase."
      : doc.secrets === "plaintext"
        ? "The file carries the credentials themselves; they are sealed again with this instance's key on the way in."
        : "The file carries no credentials, so every stored password and API key on this instance is cleared.",
    confirm: "Replace it",
    phrase: "replace",
  })) return;

  restoreStatus("Restoring…");
  try {
    const report = await request("/api/backup/restore", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ passphrase: qs("[data-backup-restore-passphrase]")?.value || "", backup: doc }),
    });
    drawReport(report);
    restoreStatus("Restored.");
    const restart = qs("[data-backup-restart]");
    if (restart) restart.hidden = !report.restart;
    // Every page's cached copy is now about an instance that no longer exists.
    forgetAll();
    await refreshBackup();
  } catch (failure) {
    restoreStatus(failure.message);
  }
}

// The same exit the configuration page uses: guard restarts by exiting, and
// whatever supervises it brings it back against the environment it just stored.
async function restart() {
  restoreStatus("Restarting…");
  try {
    await request("/api/config/restart", { method: "POST", headers: adminHeaders() });
    restoreStatus("Restarting — this page will reconnect on its own.");
  } catch (failure) {
    restoreStatus(failure.message);
  }
}

// The file control is a button, so the press has to reach the input the browser
// will actually open a dialog for.
document.addEventListener("change", (event) => {
  const input = event.target.closest("[data-backup-file]");
  if (!input) return;
  const name = qs("[data-backup-filename]");
  if (name) name.textContent = input.files?.[0]?.name || "No file chosen";
  restoreStatus("");
});

document.addEventListener("click", (event) => {
  if (event.target.closest("[data-backup-pick]")) { qs("[data-backup-file]")?.click(); return; }
  if (event.target.closest("[data-backup-export]")) { exportBackup(); return; }
  if (event.target.closest("[data-backup-restore]")) { restoreBackup(); return; }
  if (event.target.closest("[data-backup-restart]")) { restart(); return; }
});
