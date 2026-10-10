"use strict";

const views = {};
let session = null;

function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === false || v == null) continue;
    if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "class") el.className = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

class APIError extends Error {
  constructor(status, body) {
    super((body && body.error && body.error.message) || `request failed (${status})`);
    this.status = status;
    this.code = body && body.error && body.error.code;
  }
}

async function api(method, path, body) {
  const opts = { method, credentials: "same-origin", headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
  const text = await resp.text();
  const data = text ? JSON.parse(text) : null;
  if (!resp.ok) {
    if (resp.status === 401 && !path.startsWith("/web/")) {
      session = null;
      showLogin();
    }
    throw new APIError(resp.status, data);
  }
  return data;
}

function status(message, kind) {
  const el = document.getElementById("status");
  el.textContent = message || "";
  el.className = "status" + (kind ? " " + kind : "");
}

function fail(err) {
  status(err.message, "error");
}

function when(iso) {
  if (!iso) return "never";
  const d = new Date(iso);
  return isNaN(d) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

function short(id) {
  return id ? id.slice(0, 8) : "";
}

function copyButton(text) {
  const btn = h("button", { type: "button", "aria-label": "Copy command" }, "Copy");
  btn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(text);
      btn.textContent = "Copied";
      setTimeout(() => (btn.textContent = "Copy"), 1500);
    } catch {
      status("Copy failed; select the command and copy it by hand.", "error");
    }
  });
  return btn;
}

function command(text) {
  return h("div", { class: "command" }, h("code", {}, text), copyButton(text));
}

function busy(button, label, work) {
  return async (event) => {
    if (event) event.preventDefault();
    const original = button.textContent;
    button.disabled = true;
    button.textContent = label;
    try {
      await work();
    } catch (err) {
      fail(err);
    } finally {
      button.disabled = false;
      button.textContent = original;
    }
  };
}

function render(...nodes) {
  document.getElementById("view").replaceChildren(...nodes.filter((n) => n != null && n !== false));
}

function serverURL() {
  return location.origin;
}

function signedOut(set) {
  try {
    if (set === true) sessionStorage.setItem("boxface-signed-out", "1");
    else if (set === false) sessionStorage.removeItem("boxface-signed-out");
    return sessionStorage.getItem("boxface-signed-out") === "1";
  } catch {
    return false;
  }
}

async function showLogin(message) {
  document.getElementById("nav").hidden = true;
  document.getElementById("who").replaceChildren();
  const input = h("input", { id: "code", name: "code", autocomplete: "one-time-code", autocapitalize: "characters", spellcheck: "false", required: true });
  const submit = h("button", { class: "primary", type: "submit" }, "Sign in");
  const form = h("form", { class: "stack" }, h("label", { for: "code" }, "Login code", input), submit);
  form.addEventListener("submit", busy(submit, "Signing in…", async () => {
    await api("POST", "/web/login", { code: input.value.trim() });
    signedOut(false);
    status("");
    await start();
  }));
  const ident = session && session.identity;
  let viaIdentity = null;
  if (ident && ident.bound) {
    const cont = h("button", { class: "primary", type: "button" }, `Continue as ${ident.subject}`);
    cont.addEventListener("click", busy(cont, "Signing in…", async () => {
      signedOut(false);
      await api("POST", "/web/login", {});
      await start();
    }));
    viaIdentity = h("div", { class: "card" }, h("p", {}, "Signed in to Tailscale as ", h("strong", {}, ident.subject), "."), cont);
  }
  render(h("section", { class: "login" },
    h("h1", {}, "Sign in"),
    viaIdentity,
    h("p", { class: "lead" }, "Create a one-time code on the host:"),
    command("runuser -u boxface -- boxface-api bootstrap -owner <your-name>"),
    ident && !ident.bound ? h("p", { class: "hint" }, `Your Tailscale login ${ident.subject} is not bound to an owner yet. Sign in with a code, then bind it under Clients to skip codes next time.`) : null,
    h("div", { class: "card" }, form)));
  if (message) status(message, "error");
  input.focus();
}

async function start() {
  const params = new URLSearchParams(location.search);
  const code = params.get("code");
  if (code) {
    history.replaceState(null, "", "/" + location.hash);
    try {
      await api("POST", "/web/login", { code });
    } catch (err) {
      session = await api("GET", "/web/session");
      return showLogin(err.code === "unauthorized" ? "That code is invalid, expired, or already used." : err.message);
    }
  }
  session = await api("GET", "/web/session");
  if (!session.signed_in && session.identity && session.identity.bound && !signedOut()) {
    try {
      await api("POST", "/web/login", {});
      session = await api("GET", "/web/session");
    } catch (err) {
      fail(err);
    }
  }
  if (!session.signed_in) return showLogin();
  const out = h("button", { class: "link", type: "button" }, "Sign out");
  out.addEventListener("click", async () => {
    signedOut(true);
    await api("POST", "/web/logout");
    session = await api("GET", "/web/session");
    showLogin();
  });
  document.getElementById("who").replaceChildren(h("span", {}, session.owner), out);
  document.getElementById("nav").hidden = false;
  route();
}

async function route() {
  if (!session || !session.signed_in) return;
  const name = (location.hash || "#setup").slice(1);
  const view = views[name] || views.setup;
  for (const a of document.querySelectorAll("#nav a")) {
    if (a.getAttribute("href") === "#" + name) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  status("");
  try {
    await view();
  } catch (err) {
    if (err.status !== 401) fail(err);
  }
}

function mainImageID(images) {
  const img = images.find((i) => (i.aliases || []).includes("main"));
  return img ? img.id : null;
}

views.setup = async () => {
  const [keys, images, defaults, tokens] = await Promise.all([
    api("GET", "/v1/ssh-keys"), api("GET", "/v1/images"), api("GET", "/v1/defaults"), api("GET", "/v1/tokens"),
  ]);
  const steps = [
    [keys.ssh_keys.length > 0, "Add an SSH key", "The keys new environments accept. boxfacectl login registers its own device key too.", "#keys"],
    [mainImageID(images.images) !== null, "Register an image and mark it main", "New environments use the main image unless told otherwise.", "#images"],
    [defaults.saved, "Review default sizes", "vCPUs, memory, and disk for boxfacectl create NAME.", "#defaults"],
    [tokens.tokens.length > 0, "Log in from the CLI", "Issue a one-time code for each device.", "#clients"],
  ];
  const done = steps.every((s) => s[0]);
  render(
    h("h1", {}, "Setup"),
    h("p", { class: "lead" }, done ? "This server is ready. From a client:" : "Finish these steps, then provision and connect from boxfacectl."),
    done ? h("div", {}, command(`boxfacectl login ${serverURL()}`), command("boxfacectl create travel"), command("boxfacectl connect travel")) : null,
    h("div", { class: "card" }, h("ul", { class: "checklist" }, steps.map(([ok, title, detail, href]) =>
      h("li", {},
        h("span", { class: "mark" + (ok ? " done" : ""), "aria-hidden": "true" }, ok ? "✓" : ""),
        h("div", {},
          h("a", { href }, title), h("span", { class: "visually-hidden" }, ok ? " (done)" : " (to do)"),
          h("p", { class: "hint" }, detail)))))));
};

views.images = async () => {
  const [staged, images] = await Promise.all([api("GET", "/v1/image-imports"), api("GET", "/v1/images")]);
  const stagedList = staged.staged.length === 0
    ? h("p", { class: "muted" }, "Nothing staged. Copy vmlinux and rootfs.ext4 into a directory under /var/lib/boxface/import/ on the host.")
    : h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Directory"), h("th", {}, "Size"), h("th", {}, ""))),
      h("tbody", {}, staged.staged.map((s) => {
        const btn = h("button", { type: "button", class: "primary" }, "Register");
        btn.addEventListener("click", busy(btn, "Hashing…", async () => {
          status(`Registering ${s.source}; hashing can take a minute.`);
          const img = await api("POST", "/v1/images", { source: s.source });
          if (!mainImageID(images.images)) await api("PUT", "/v1/image-aliases/main", { image_id: img.id });
          status(`Registered ${s.source} as ${short(img.id)}.`, "ok");
          await views.images();
        }));
        return h("tr", {}, h("td", { class: "mono" }, s.source), h("td", {}, `${s.size_mib} MiB`), h("td", { class: "actions" }, btn));
      }))));
  const registered = images.images.length === 0
    ? h("p", { class: "muted" }, "No images registered yet.")
    : h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Image"), h("th", { class: "optional" }, "Digest"), h("th", {}, "Created"), h("th", {}, ""))),
      h("tbody", {}, images.images.map((img) => {
        const isMain = (img.aliases || []).includes("main");
        let action = null;
        if (!isMain && img.status === "ready") {
          action = h("button", { type: "button" }, "Make main");
          action.addEventListener("click", busy(action, "Saving…", async () => {
            await api("PUT", "/v1/image-aliases/main", { image_id: img.id });
            status(`main now points at ${short(img.id)}. Existing environments keep their image.`, "ok");
            await views.images();
          }));
        }
        return h("tr", {},
          h("td", {}, h("span", { class: "mono", title: img.id }, short(img.id)), " ",
            (img.aliases || []).map((a) => h("span", { class: "badge" + (a === "main" ? " main" : "") }, a)),
            img.status !== "ready" ? h("span", { class: "badge warn" }, img.status) : null),
          h("td", { class: "optional mono", title: img.digest }, img.digest.slice(0, 19) + "…"),
          h("td", {}, when(img.created_at)),
          h("td", { class: "actions" }, action));
      }))));
  render(
    h("h1", {}, "Images"),
    h("p", { class: "lead" }, "The main image is what new environments use. Moving it never changes existing environments."),
    h("h2", {}, "Staged on the host"), stagedList,
    h("h2", {}, "Registered"), registered);
};

views.defaults = async () => {
  const [d, caps, images] = await Promise.all([api("GET", "/v1/defaults"), api("GET", "/v1/capabilities"), api("GET", "/v1/images")]);
  const imageOptions = new Set(["main"]);
  for (const img of images.images) for (const a of img.aliases || []) imageOptions.add(a);
  const field = (label, name, value, extra) => {
    const input = h("input", Object.assign({ id: name, name, value: String(value) }, extra));
    return [input, h("label", { for: name }, label, input)];
  };
  const [image, imageLabel] = field("Image (alias or ID)", "image", d.image, { list: "image-options", required: true });
  const [vcpus, vcpusLabel] = field("vCPUs", "vcpus", d.vcpus, { type: "number", min: 1, required: true });
  const [memory, memoryLabel] = field("Memory (MiB)", "memory", d.memory_mib, { type: "number", min: 128, step: 128, required: true });
  const [disk, diskLabel] = field("Disk (GiB)", "disk", d.disk_gib, { type: "number", min: 1, required: true });
  const save = h("button", { class: "primary", type: "submit" }, "Save defaults");
  const form = h("form", { class: "stack" },
    imageLabel, h("datalist", { id: "image-options" }, [...imageOptions].map((o) => h("option", { value: o }))),
    h("div", { class: "row" }, vcpusLabel, memoryLabel, diskLabel), h("div", {}, save));
  form.addEventListener("submit", busy(save, "Saving…", async () => {
    await api("PUT", "/v1/defaults", { image: image.value.trim(), vcpus: +vcpus.value, memory_mib: +memory.value, disk_gib: +disk.value });
    await views.defaults();
    status("Defaults saved. They apply to environments created from now on.", "ok");
  }));
  const limit = (v, unit) => (v > 0 ? `${v}${unit}` : "no limit");
  const c = caps.capacity;
  render(
    h("h1", {}, "Defaults"),
    h("p", { class: "lead" }, "What boxfacectl create NAME uses when no flags are given."),
    d.saved ? null : h("p", { class: "hint" }, "These are the built-in defaults; nothing has been saved yet."),
    h("div", { class: "card" }, form),
    h("h2", {}, "Host limits"),
    h("p", { class: "muted" }, `Environments: ${limit(c.environments, "")} · vCPUs: ${limit(c.vcpus, "")} · Memory: ${limit(c.memory_mib, " MiB")} · Disk: ${limit(c.disk_gib, " GiB")}`));
};

views.keys = async () => {
  const keys = await api("GET", "/v1/ssh-keys");
  const name = h("input", { id: "key-name", name: "name", required: true, maxlength: 64, placeholder: "laptop" });
  const key = h("textarea", { id: "key-value", name: "public_key", required: true, spellcheck: "false", placeholder: "ssh-ed25519 AAAA… you@laptop" });
  const add = h("button", { class: "primary", type: "submit" }, "Add key");
  const form = h("form", { class: "stack" }, h("label", { for: "key-name" }, "Name", name), h("label", { for: "key-value" }, "Public key", key), h("div", {}, add));
  form.addEventListener("submit", busy(add, "Adding…", async () => {
    const k = await api("POST", "/v1/ssh-keys", { name: name.value.trim(), public_key: key.value.trim() });
    status(`Added ${k.fingerprint}.`, "ok");
    await views.keys();
  }));
  render(
    h("h1", {}, "SSH keys"),
    h("p", { class: "lead" }, "Environments accept these keys from their next start. Removing a key takes effect when an environment next starts."),
    keys.ssh_keys.length === 0 ? h("p", { class: "muted" }, "No keys yet.") : h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Name"), h("th", {}, "Fingerprint"), h("th", { class: "optional" }, "Added"), h("th", {}, ""))),
      h("tbody", {}, keys.ssh_keys.map((k) => {
        const rm = h("button", { type: "button", class: "danger" }, "Remove");
        rm.addEventListener("click", busy(rm, "Removing…", async () => {
          if (!confirm(`Remove the key ${k.name}? Environments stop accepting it at their next start.`)) return;
          await api("DELETE", `/v1/ssh-keys/${encodeURIComponent(k.id)}`);
          await views.keys();
        }));
        return h("tr", {}, h("td", {}, k.name), h("td", { class: "mono" }, k.fingerprint), h("td", { class: "optional" }, when(k.created_at)), h("td", { class: "actions" }, rm));
      })))),
    h("h2", {}, "Add a key"), h("div", { class: "card" }, form));
};

views.clients = async () => {
  const [tokens, identities] = await Promise.all([api("GET", "/v1/tokens"), api("GET", "/v1/identities")]);
  const issued = h("div", {});
  const issue = h("button", { class: "primary", type: "button" }, "Create a login code");
  issue.addEventListener("click", busy(issue, "Creating…", async () => {
    const c = await api("POST", "/v1/login-codes");
    issued.replaceChildren(
      h("p", {}, "Run this on the device. The code works once and expires ", when(c.expires_at), "."),
      command(`boxfacectl login ${serverURL()} --code ${c.code}`));
  }));

  const ident = session.identity;
  let bind = null;
  if (ident && !ident.bound) {
    bind = h("button", { type: "button" }, `Bind ${ident.subject}`);
    bind.addEventListener("click", busy(bind, "Binding…", async () => {
      await api("POST", "/v1/identities", { provider: ident.provider });
      session = await api("GET", "/web/session");
      status(`${ident.subject} can now sign in without a code through Tailscale.`, "ok");
      await views.clients();
    }));
  }

  render(
    h("h1", {}, "Clients"),
    h("p", { class: "lead" }, "Devices logged in to this server, and how new ones log in."),
    h("h2", {}, "Log in a device"),
    h("div", { class: "card" }, issue, issued),
    h("h2", {}, "Logged-in devices"),
    tokens.tokens.length === 0 ? h("p", { class: "muted" }, "No CLI logins yet.") : h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Device"), h("th", { class: "optional" }, "Logged in"), h("th", {}, "Last used"), h("th", {}, ""))),
      h("tbody", {}, tokens.tokens.map((t) => {
        const revoke = h("button", { type: "button", class: "danger" }, "Revoke");
        revoke.addEventListener("click", busy(revoke, "Revoking…", async () => {
          if (!confirm(`Revoke ${t.name}? That device must log in again.`)) return;
          await api("DELETE", `/v1/tokens/${encodeURIComponent(t.id)}`);
          await views.clients();
        }));
        return h("tr", {}, h("td", {}, t.name), h("td", { class: "optional" }, when(t.created_at)), h("td", {}, when(t.last_used_at)), h("td", { class: "actions" }, revoke));
      })))),
    h("h2", {}, "Tailscale sign-in"),
    h("p", { class: "muted" }, "Optional. A bound Tailscale login signs in to this page and to boxfacectl login without a code, but only through tailscale serve."),
    identities.identities.length === 0 ? null : h("div", { class: "table-wrap" }, h("table", {},
      h("tbody", {}, identities.identities.map((i) => {
        const unbind = h("button", { type: "button", class: "danger" }, "Unbind");
        unbind.addEventListener("click", busy(unbind, "Unbinding…", async () => {
          if (!confirm(`Unbind ${i.subject}? Devices already logged in stay logged in until revoked.`)) return;
          await api("DELETE", `/v1/identities/${encodeURIComponent(i.id)}`);
          session = await api("GET", "/web/session");
          await views.clients();
        }));
        return h("tr", {}, h("td", {}, h("span", { class: "badge" }, i.provider), " ", i.subject), h("td", { class: "optional" }, when(i.created_at)), h("td", { class: "actions" }, unbind));
      })))),
    bind ? h("div", { class: "card" }, h("p", {}, `You are using this page as ${ident.subject}.`), bind)
      : ident ? null : h("p", { class: "hint" }, "Open this page through tailscale serve to bind your Tailscale login."));
};

views.environments = async () => {
  const [envs, images] = await Promise.all([api("GET", "/v1/environments"), api("GET", "/v1/images")]);
  const mainID = mainImageID(images.images);
  const aliasOf = (id) => {
    const img = images.images.find((i) => i.id === id);
    return img && img.aliases && img.aliases.length ? img.aliases.join(", ") : short(id);
  };
  const live = envs.environments.filter((e) => e.observed_state !== "deleted");
  const stateClass = (s) => (s === "running" ? "badge ok" : s === "error" ? "badge warn" : "badge");
  render(
    h("h1", {}, "Environments"),
    h("p", { class: "lead" }, "Create, start, stop, and delete from boxfacectl. connect starts a stopped environment."),
    live.length === 0 ? h("div", {}, h("p", { class: "muted" }, "No environments yet. Create one from a client:"), command("boxfacectl create travel")) :
      h("div", { class: "table-wrap" }, h("table", {},
        h("thead", {}, h("tr", {}, h("th", {}, "Name"), h("th", {}, "State"), h("th", {}, "Image"), h("th", { class: "optional" }, "Size"), h("th", {}, "Connect"))),
        h("tbody", {}, live.map((e) => h("tr", {},
          h("td", {}, e.name),
          h("td", {}, h("span", { class: stateClass(e.observed_state) }, e.observed_state), e.error ? h("p", { class: "hint" }, e.error) : null),
          h("td", {}, aliasOf(e.image_id), mainID && e.image_id !== mainID ? [" ", h("span", { class: "badge warn", title: "main points at a newer image; recreate to use it" }, "main moved")] : null),
          h("td", { class: "optional" }, `${e.vcpus} vCPU · ${e.memory_mib} MiB`),
          h("td", {}, command(`boxfacectl connect ${e.name}`))))))));
};

window.addEventListener("hashchange", route);
start().catch(fail);
