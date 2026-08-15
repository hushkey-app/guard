// The members page: who may sign in, and the two presses that change it.
//
// Small on purpose. The list is guard's own SQLite rather than anybody's API,
// so there is no cache and no throttle here — it is fetched on a mount and
// after every change, and that is the whole of its state.
//
// Two rows are drawn without a Remove button, and both are load-bearing. An
// address from GUARD_ADMIN_EMAIL is not a row in the table, so removing it
// would look like it worked and change nothing; your own address is a row, and
// removing it is how somebody locks themselves out of their own dashboard. The
// server refuses both as well — this is the half of the rule that is visible,
// not the half that enforces it.
import { adminHeaders, el, muted, qs, relativeTime, request } from "./core.js";
import { ensure, forget } from "./store.js";
import { ask } from "./cluster.js";

const cellBase = "cn-table-cell cn-table-cell-aria";

function cell(value, className = "") {
  const td = el("td", `${cellBase} ${className}`.trim());
  td.textContent = value ?? "";
  return td;
}

function notice(message) {
  const line = document.createElement("tr");
  line.className = "cn-table-row";
  const td = el("td", `${cellBase} ${muted}`, message);
  td.colSpan = 4;
  line.append(td);
  return line;
}

// memberCell is the address, with what the last sign-in said about the person
// under it. A member who has never signed in shows nothing there, which is how
// "invited, never arrived" reads without a column for it.
function memberCell(member, you) {
  const td = el("td", cellBase);
  const line = el("div", "flex flex-wrap items-center gap-2");
  line.append(el("span", "text-sm font-medium", member.email));
  if (you && member.email === you) line.append(el("span", "cn-badge cn-badge-variant-secondary", "You"));
  td.append(line);
  const detail = [member.name, member.provider === "apple" ? "Apple" : member.provider === "google" ? "Google" : ""]
    .filter(Boolean).join(" · ");
  if (detail) td.append(el("p", `text-xs ${muted}`, detail));
  return td;
}

function roleCell(member) {
  const td = el("td", cellBase);
  const badge = el("span", `cn-badge ${member.role === "admin" ? "cn-badge-variant-default" : "cn-badge-variant-secondary"}`,
    member.role === "admin" ? "Admin" : "Member");
  td.append(badge);
  // Why this one has no buttons beside it.
  if (member.fixed) td.append(el("p", `text-xs ${muted}`, "From GUARD_ADMIN_EMAIL"));
  return td;
}

function actionCell(member, you) {
  const td = el("td", `${cellBase} text-right`);
  if (member.fixed || (you && member.email === you)) return td;
  const remove = el("button", "cn-button cn-button-variant-ghost cn-button-size-sm text-destructive", "Remove");
  remove.type = "button";
  remove.dataset.memberRemove = member.email;
  td.append(remove);
  return td;
}

export async function refreshMembers() {
  const body = qs("[data-member-rows]");
  if (!body) return;
  try {
    // Through the store, like every other page: the list is small, but the
    // point is that walking back to it never shows an empty table.
    await ensure("members", () => request("/api/members", { headers: adminHeaders() }), (roster) => {
    const disabled = qs("[data-members-disabled]");
    if (disabled) disabled.hidden = roster.enabled;
    const you = roster.you?.email || "";
    if (!roster.members.length) {
      body.replaceChildren(notice("Nobody on the list yet. Add an address above."));
      return;
    }
    body.replaceChildren(...roster.members.map((member) => {
      const line = document.createElement("tr");
      line.className = "cn-table-row";
      line.append(
        memberCell(member, you),
        roleCell(member),
        cell(member.last_seen ? relativeTime(member.last_seen) : "Never signed in", `text-sm ${muted}`),
        actionCell(member, you),
      );
      return line;
    }));
    });
  } catch (failure) {
    body.replaceChildren(notice(failure.message));
  }
}

function status(message) {
  const node = qs("[data-member-status]");
  if (node) node.textContent = message;
}

async function addMember(form) {
  const email = qs('[data-member="email"]', form).value.trim();
  const role = qs('[data-member="role"]', form).value;
  if (!email) return;
  status("Adding…");
  try {
    const member = await request("/api/members", {
      method: "POST", headers: adminHeaders(),
      body: JSON.stringify({ email, role }),
    });
    status(`${member.email} can sign in${member.role === "admin" ? " as an admin" : ""}.`);
    qs('[data-member="email"]', form).value = "";
    forget("members");
    await refreshMembers();
  } catch (failure) {
    status(failure.message);
  }
}

async function removeMember(email) {
  if (!await ask({
    title: `Remove ${email}?`,
    body: "They stop being able to sign in, and every dashboard they have open stops working on its next request.",
    confirm: "Remove the member",
  })) return;
  status("Removing…");
  try {
    const removed = await request(`/api/members/${encodeURIComponent(email)}`, { method: "DELETE", headers: adminHeaders() });
    status(removed.sessions > 0
      ? `${email} removed, and ${removed.sessions} open session${removed.sessions === 1 ? "" : "s"} ended.`
      : `${email} removed.`);
    forget("members");
    await refreshMembers();
  } catch (failure) {
    status(failure.message);
  }
}

document.addEventListener("submit", (event) => {
  const form = event.target.closest("[data-member-form]");
  if (!form) return;
  event.preventDefault();
  addMember(form);
});

document.addEventListener("click", (event) => {
  const remove = event.target.closest("[data-member-remove]");
  if (!remove) return;
  removeMember(remove.dataset.memberRemove);
});
