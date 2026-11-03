// @ts-check
// console.js adds convenience to the console pages, which work without it; the server enforces every rule. It never
// reads row text and never writes HTML: it writes only text content, attributes, hidden, checked, indeterminate,
// disabled, and document.title. The page's Trusted Types policy ('none') makes every HTML sink throw, and a Go test
// refuses their names in this file. The selectors and data attributes it reads are a contract with the templates,
// which TestConsoleScriptReadsOnlyWhatTheTemplatesRender checks.

/**
 * selectionText is the screen-reader line of the bulk bar, which repeats the CSS counter text.
 * @param {number} selected
 * @param {number} review the selected rows that need review, which a batch approve skips
 */
function selectionText(selected, review) {
  if (selected === 0) return "";
  if (review === selected) return `${selected} selected, all need review: approve each from its row`;
  if (review === 0) return `${selected} selected`;
  return `${selected} selected, ${review} ${review === 1 ? "needs" : "need"} review: Approve skips them`;
}

/**
 * titleWith puts the waiting count in front of the page title.
 * @param {number} waiting
 * @param {string} title
 */
function titleWith(waiting, title) {
  return waiting > 0 ? `(${waiting}) ${title}` : title;
}

/**
 * shouldReload reports whether the interval is up and the page stayed idle for the grace period since it was last
 * busy, so that the page does not jump the moment a box is cleared.
 * @param {number} now
 * @param {number} dueAt
 * @param {number} lastBusy
 * @param {number} graceMs
 */
function shouldReload(now, dueAt, lastBusy, graceMs) {
  return now >= dueAt && now - lastBusy >= graceMs;
}

/**
 * reloadURL is the list URL with its filter only. A reload happens only while no box is ticked, so select= must not
 * tick rows again, and a batch notice is old news.
 * @param {string} href
 */
function reloadURL(href) {
  const url = new URL(href);
  const kept = new URLSearchParams();
  for (const key of ["account", "status"]) {
    const value = url.searchParams.get(key);
    if (value !== null) kept.set(key, value);
  }
  url.search = kept.toString();
  return url.href;
}

const body = document.body;
document.title = titleWith(Number(body.dataset.waiting) || 0, document.title);

/** @type {HTMLInputElement[]} */
const rowBoxes = Array.from(document.querySelectorAll("input.sel"));

// Each table's header link, which ticks the table through ?select=, gives way to a real checkbox.
/** @type {{box: HTMLInputElement, rows: HTMLInputElement[]}[]} */
const groups = [];
for (const section of document.querySelectorAll("section[data-group]")) {
  const link = section.querySelector("a.all");
  const label = section.querySelector("label.all");
  const box = section.querySelector("input.allbox");
  /** @type {HTMLInputElement[]} */
  const rows = Array.from(section.querySelectorAll("input.sel"));
  if (!(link instanceof HTMLElement) || !(label instanceof HTMLElement) || !(box instanceof HTMLInputElement) || rows.length === 0) continue;
  link.hidden = true;
  label.hidden = false;
  box.addEventListener("change", () => {
    for (const row of rows) row.checked = box.checked;
    selectionChanged();
  });
  groups.push({ box, rows });
}

const selectionStatus = document.getElementById("selstatus");

// selectionChanged syncs the header boxes, checked or indeterminate, and the screen-reader line. Setting checked from
// script fires no change event, so every change of the boxes calls it.
function selectionChanged() {
  for (const { box, rows } of groups) {
    const n = rows.filter((r) => r.checked).length;
    box.checked = n > 0 && n === rows.length;
    box.indeterminate = n > 0 && n < rows.length;
  }
  const picked = rowBoxes.filter((r) => r.checked);
  const review = picked.filter((r) => r.dataset.review !== undefined).length;
  if (selectionStatus) selectionStatus.textContent = selectionText(picked.length, review);
}

document.addEventListener("change", (e) => {
  if (e.target instanceof HTMLInputElement && e.target.matches("input.sel")) selectionChanged();
});
// Clear resets the form after its reset event, without change events.
document.getElementById("batch")?.addEventListener("reset", () => setTimeout(selectionChanged, 0));
selectionChanged();

// An action that needs review: Approve stays disabled until "I reviewed it" is ticked. The box stays required, and
// the server still refuses the approve without it.
for (const form of document.querySelectorAll("form.ack")) {
  const reviewed = form.querySelector('input[name="confirm"]');
  const approve = form.querySelector('button[value="approve"]');
  if (!(reviewed instanceof HTMLInputElement) || !(approve instanceof HTMLButtonElement)) continue;
  const sync = () => {
    approve.disabled = !reviewed.checked;
  };
  reviewed.addEventListener("change", sync);
  sync();
}

// busy is true while the owner works: a row box is ticked, a row or a Recents list is open, or the refresh menu is.
function busy() {
  return document.querySelector("input.sel:checked, .row details[open], details.recent[open], details.refresh[open]") !== null;
}

// Auto-refresh: a full reload of the list with its filter when the interval is up and the page is idle. The page
// without the script reloads through its noscript meta refresh instead.
const refreshMs = (Number(body.dataset.refreshSeconds) || 0) * 1000;
if (refreshMs > 0) {
  const dueAt = Date.now() + refreshMs;
  const graceMs = Math.min(5000, refreshMs);
  let lastBusy = 0;
  const timer = setInterval(() => {
    const now = Date.now();
    if (busy()) {
      lastBusy = now;
    } else if (shouldReload(now, dueAt, lastBusy, graceMs)) {
      // Once: a slow page load must not be restarted by the next tick.
      clearInterval(timer);
      location.replace(reloadURL(location.href));
    }
  }, 1000);
}
