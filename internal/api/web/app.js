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

let refreshTimer = null;

function refreshIn(ms, fn) {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => fn().catch((err) => { if (err.status !== 401) fail(err); }), ms);
}

async function route() {
  if (!session || !session.signed_in) return;
  clearTimeout(refreshTimer);
  const [name, arg] = (location.hash || "#setup").slice(1).split("/");
  const view = views[name] || views.setup;
  const section = name === "profile" ? "profiles" : name;
  for (const a of document.querySelectorAll("#nav a")) {
    if (a.getAttribute("href") === "#" + section) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  status("");
  try {
    await view(arg);
  } catch (err) {
    if (err.status !== 401) fail(err);
  }
}

function mainImageID(images) {
  const img = images.find((i) => (i.aliases || []).includes("main"));
  return img ? img.id : null;
}

views.setup = async () => {
  const [keys, images, defaults, tokens, profiles] = await Promise.all([
    api("GET", "/v1/ssh-keys"), api("GET", "/v1/images"), api("GET", "/v1/templates/default"), api("GET", "/v1/tokens"), api("GET", "/v1/profiles"),
  ]);
  const defaultProfile = profiles.profiles.find((p) => p.name === (defaults.profile || "main"));
  const steps = [
    [keys.ssh_keys.length > 0, "Add an SSH key", "The keys new environments accept. boxfacectl login registers its own device key too.", "#keys"],
    [mainImageID(images.images) !== null, "Register an image and mark it main", "New environments use the main image unless told otherwise.", "#images"],
    [!!defaultProfile && defaultProfile.latest.state === "ready", `Build the ${defaults.profile || "main"} profile`, "The tools every new environment gets, built from Nix packages.", defaultProfile ? `#profile/${defaultProfile.id}` : "#profile/new"],
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

function gib(bytes) {
  return `${(bytes / 2 ** 30).toFixed(1)} GiB`;
}

function storageCard(st) {
  const clear = h("button", { type: "button" }, "Clear build cache");
  clear.addEventListener("click", busy(clear, "Clearing…", async () => {
    if (!confirm("Clear your Nix build cache? The next profile build downloads everything again.")) return;
    await api("DELETE", "/v1/storage/build-cache");
    status("Build cache cleared.", "ok");
    await views.images();
  }));
  const low = st.free_bytes < 10 * 2 ** 30;
  return h("div", { class: "card" },
    h("p", {}, h("strong", {}, `${gib(st.free_bytes)} free`), ` of ${gib(st.total_bytes)}`, low ? [" ", h("span", { class: "badge warn" }, "low: builds need 10 GiB")] : null),
    h("p", { class: "hint" }, `Images ${gib(st.images_bytes)} · profile drives ${gib(st.profile_drives_bytes)} · build cache ${gib(st.build_caches_bytes)} · data disks ${gib(st.data_disks_bytes)}`),
    h("p", { class: "hint" }, "Unused profile drives are removed automatically. Remove images nothing uses, or clear the build cache, to free more."),
    clear);
}

views.images = async () => {
  const [staged, images, storage] = await Promise.all([api("GET", "/v1/image-imports"), api("GET", "/v1/images"), api("GET", "/v1/storage")]);
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
        const action = [];
        if (!isMain && img.status === "ready") {
          const make = h("button", { type: "button" }, "Make main");
          make.addEventListener("click", busy(make, "Saving…", async () => {
            await api("PUT", "/v1/image-aliases/main", { image_id: img.id });
            status(`main now points at ${short(img.id)}. Existing environments keep their image.`, "ok");
            await views.images();
          }));
          action.push(make, " ");
        }
        if (!isMain) {
          const remove = h("button", { type: "button", class: "danger" }, "Remove");
          remove.addEventListener("click", busy(remove, "Removing…", async () => {
            if (!confirm(`Remove image ${short(img.id)}? Its files are deleted; importing the same files again restores it.`)) return;
            await api("DELETE", `/v1/images/${encodeURIComponent(img.id)}`);
            status(`Removed ${short(img.id)}.`, "ok");
            await views.images();
          }));
          action.push(remove);
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
    h("h2", {}, "Registered"), registered,
    h("h2", {}, "Storage"), storageCard(storage));
};

views.defaults = async () => {
  const [d, caps, images] = await Promise.all([api("GET", "/v1/templates/default"), api("GET", "/v1/capabilities"), api("GET", "/v1/images")]);
  const imageOptions = new Set(["main"]);
  for (const img of images.images) for (const a of img.aliases || []) imageOptions.add(a);
  const field = (label, name, value, extra) => {
    const input = h("input", Object.assign({ id: name, name, value: String(value) }, extra));
    return [input, h("label", { for: name }, label, input)];
  };
  const [image, imageLabel] = field("Image (alias or ID)", "image", d.image, { list: "image-options", required: true });
  const [profile, profileLabel] = field("Profile", "profile", d.profile || "main", { required: true });
  const [vcpus, vcpusLabel] = field("vCPUs", "vcpus", d.vcpus, { type: "number", min: 1, required: true });
  const [memory, memoryLabel] = field("Memory (MiB)", "memory", d.memory_mib, { type: "number", min: 128, step: 128, required: true });
  const [disk, diskLabel] = field("Disk (GiB)", "disk", d.disk_gib, { type: "number", min: 1, required: true });
  const save = h("button", { class: "primary", type: "submit" }, "Save defaults");
  const form = h("form", { class: "stack" },
    imageLabel, h("datalist", { id: "image-options" }, [...imageOptions].map((o) => h("option", { value: o }))),
    profileLabel,
    h("div", { class: "row" }, vcpusLabel, memoryLabel, diskLabel), h("div", {}, save));
  form.addEventListener("submit", busy(save, "Saving…", async () => {
    await api("PUT", "/v1/templates/default", { image: image.value.trim(), profile: profile.value.trim(), vcpus: +vcpus.value, memory_mib: +memory.value, disk_gib: +disk.value });
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

const presets = [
  { id: "claude", label: "Claude Code subscription token (claude setup-token)", name: "claude-token", bindings: [["claude", "CLAUDE_CODE_OAUTH_TOKEN"]] },
  { id: "openai", label: "OpenAI API key", name: "openai-api-key", bindings: [["codex", "OPENAI_API_KEY"], ["opencode", "OPENAI_API_KEY"], ["pi", "OPENAI_API_KEY"]] },
  { id: "anthropic", label: "Anthropic API key", name: "anthropic-api-key", bindings: [["claude", "ANTHROPIC_API_KEY"], ["opencode", "ANTHROPIC_API_KEY"], ["pi", "ANTHROPIC_API_KEY"]] },
  { id: "github", label: "GitHub token for gh", name: "github-token", bindings: [["gh", "GH_TOKEN"]] },
  { id: "custom", label: "Custom", name: "", bindings: [] },
];

function describeTarget(consumer) {
  return consumer === "session" ? "every session" : consumer;
}

views.credentials = async () => {
  const data = await api("GET", "/v1/secrets");
  const preset = h("select", { id: "preset" }, presets.map((p) => h("option", { value: p.id }, p.label)));
  const name = h("input", { id: "secret-name", required: true, maxlength: 63, spellcheck: "false" });
  const value = h("input", { id: "secret-value", type: "password", required: true, autocomplete: "off", spellcheck: "false" });
  const variable = h("input", { id: "secret-variable", spellcheck: "false", placeholder: "GH_TOKEN" });
  const commands = h("input", { id: "secret-commands", spellcheck: "false", placeholder: "gh terraform", list: "command-suggestions" });
  const everywhere = h("input", { type: "checkbox", id: "secret-session" });
  const warning = h("p", { class: "hint warn", hidden: true }, "Every process in a session can read this value, including tools and scripts you run. Prefer naming the commands that need it.");
  everywhere.addEventListener("change", () => { warning.hidden = !everywhere.checked; commands.disabled = everywhere.checked; });
  const custom = h("div", { class: "stack", hidden: true },
    h("label", { for: "secret-variable" }, "Environment variable", variable),
    h("label", { for: "secret-commands" }, "Commands that receive it (separated by spaces)", commands),
    h("datalist", { id: "command-suggestions" }, data.suggested.map((c) => h("option", { value: c }))),
    h("label", { class: "inline", for: "secret-session" }, everywhere, " Every session instead"),
    warning);
  const covered = h("p", { class: "hint" });
  const sync = () => {
    const p = presets.find((x) => x.id === preset.value);
    custom.hidden = p.id !== "custom";
    name.value = p.name;
    covered.textContent = p.id === "custom" ? "" : "Given to " + p.bindings.map(([c, v]) => `${describeTarget(c)} as ${v}`).join(", ") + ".";
  };
  preset.addEventListener("change", sync);
  sync();
  const save = h("button", { class: "primary", type: "submit" }, "Save credential");
  const form = h("form", { class: "stack" },
    h("label", { for: "preset" }, "Kind", preset), covered,
    h("label", { for: "secret-name" }, "Name", name),
    custom,
    h("label", { for: "secret-value" }, "Value", value),
    h("div", {}, save));
  form.addEventListener("submit", busy(save, "Saving…", async () => {
    const p = presets.find((x) => x.id === preset.value);
    let bindings;
    if (p.id === "custom") {
      const targets = everywhere.checked ? [data.session] : commands.value.split(/[\s,]+/).filter(Boolean);
      bindings = targets.map((consumer) => ({ consumer, variable: variable.value.trim() }));
    } else {
      bindings = p.bindings.map(([consumer, variable]) => ({ consumer, variable }));
    }
    if (bindings.length === 0) throw new Error("Name at least one command, or choose every session.");
    await api("POST", "/v1/secrets", { name: name.value.trim(), value: value.value, bindings });
    value.value = "";
    await views.credentials();
    status(`Saved ${name.value}. Shells opened from now on pick it up.`, "ok");
  }));

  const rows = data.secrets.map((sec) => {
    const replace = h("button", { type: "button" }, "Replace value");
    const remove = h("button", { type: "button", class: "danger" }, "Delete");
    const editor = h("div", {});
    replace.addEventListener("click", () => {
      const input = h("input", { type: "password", autocomplete: "off", required: true, "aria-label": `New value for ${sec.name}` });
      const ok = h("button", { class: "primary", type: "submit" }, "Save");
      const f = h("form", { class: "command" }, input, ok);
      f.addEventListener("submit", busy(ok, "Saving…", async () => {
        await api("PUT", `/v1/secrets/${encodeURIComponent(sec.id)}`, { value: input.value });
        await views.credentials();
        status(`Replaced ${sec.name}. Commands launched from now on use the new value.`, "ok");
      }));
      editor.replaceChildren(f);
      input.focus();
    });
    remove.addEventListener("click", busy(remove, "Deleting…", async () => {
      if (!confirm(`Delete ${sec.name}? Commands launched from now on will not receive it.`)) return;
      await api("DELETE", `/v1/secrets/${encodeURIComponent(sec.id)}`);
      await views.credentials();
    }));
    return h("tr", {},
      h("td", {}, sec.name, editor),
      h("td", {}, sec.bindings.map((b) => h("div", {}, h("span", { class: "badge" + (b.consumer === "session" ? " warn" : "") }, describeTarget(b.consumer)), " ", h("code", {}, b.variable)))),
      h("td", { class: "optional" }, when(sec.updated_at)),
      h("td", { class: "actions" }, replace, " ", remove));
  });

  render(
    h("h1", {}, "Credentials"),
    h("p", { class: "lead" }, "Tokens and keys for the tools you run in your environments. Each is given only to the commands you name, or to every session if you choose. Values are encrypted on the server and never shown again."),
    data.secrets.length === 0 ? h("p", { class: "muted" }, "No credentials saved.") : h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Name"), h("th", {}, "Given to"), h("th", { class: "optional" }, "Updated"), h("th", {}, ""))),
      h("tbody", {}, rows))),
    h("h2", {}, "Add a credential"),
    h("div", { class: "card" }, form),
    h("p", { class: "hint" }, "Commands get the current value each time they start, in shells opened after it was saved. ChatGPT subscription sign-ins for Codex, OpenCode, and Pi cannot be shared between environments; sign in inside each environment instead."));
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

const suggestedPackages = ["claude-code", "codex", "opencode", "pi-coding-agent", "mise", "nodejs", "python3", "go", "rustc", "cargo", "gcc", "gnumake", "ripgrep", "fd", "jq", "gh", "htop", "neovim", "uv"];

function stateBadge(state) {
  const cls = state === "ready" ? "badge ok" : state === "failed" ? "badge warn" : "badge";
  return h("span", { class: cls }, state === "building" ? "building…" : state);
}

views.profiles = async () => {
  const [data, defaults] = await Promise.all([api("GET", "/v1/profiles"), api("GET", "/v1/templates/default")]);
  const def = defaults.profile || "main";
  const rows = data.profiles.map((p) => {
    const actions = [h("a", { href: `#profile/${p.id}` }, "Edit")];
    if (p.name !== def) {
      const make = h("button", { type: "button" }, "Make default");
      make.addEventListener("click", busy(make, "Saving…", async () => {
        await api("PUT", "/v1/templates/default", { image: defaults.image, profile: p.name, vcpus: defaults.vcpus, memory_mib: defaults.memory_mib, disk_gib: defaults.disk_gib });
        status(`New environments now use ${p.name}.`, "ok");
        await views.profiles();
      }));
      actions.push(" ", make);
    }
    return h("tr", {},
      h("td", {}, h("a", { href: `#profile/${p.id}` }, p.name), p.name === def ? [" ", h("span", { class: "badge main" }, "default")] : null),
      h("td", {}, `#${p.latest.number} `, stateBadge(p.latest.state)),
      h("td", { class: "optional" }, p.latest.spec.module ? "Home Manager module" : `${(p.latest.spec.packages || []).length} packages`),
      h("td", { class: "optional" }, when(p.latest.updated_at)),
      h("td", { class: "actions" }, actions));
  });
  render(
    h("h1", {}, "Profiles"),
    h("p", { class: "lead" }, "The tools your environments get, built from Nix packages in an isolated builder. New environments use the default profile; existing ones keep the revision they were created with."),
    h("p", {}, h("a", { href: "#profile/new", class: "button-link" }, "New profile")),
    data.profiles.length === 0 ? h("p", { class: "muted" }, "No profiles yet.") : h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, h("th", {}, "Profile"), h("th", {}, "Latest revision"), h("th", { class: "optional" }, "Contents"), h("th", { class: "optional" }, "Updated"), h("th", {}, ""))),
      h("tbody", {}, rows))));
  if (data.profiles.some((p) => p.latest.state === "pending" || p.latest.state === "building")) refreshIn(4000, views.profiles);
};

views.profile = async (id) => {
  const isNew = !id || id === "new";
  const profile = isNew ? null : await api("GET", `/v1/profiles/${encodeURIComponent(id)}`);
  const spec = profile ? profile.latest.spec : { packages: ["claude-code", "codex", "opencode", "pi-coding-agent", "mise", "nodejs"], unfree: true };
  let packages = [...(spec.packages || [])];
  let mode = spec.module ? "module" : "packages";

  const name = h("input", { id: "profile-name", required: true, maxlength: 63, spellcheck: "false", value: isNew ? "main" : profile.name });
  const chips = h("div", { class: "chips", "aria-live": "polite" });
  const drawChips = () => chips.replaceChildren(...packages.map((p) => {
    const rm = h("button", { type: "button", class: "chip", "aria-label": `Remove ${p}` }, p, " ×");
    rm.addEventListener("click", () => { packages = packages.filter((x) => x !== p); drawChips(); });
    return rm;
  }));
  drawChips();
  const pkgInput = h("input", { id: "package-input", list: "package-suggestions", spellcheck: "false", placeholder: "ripgrep" });
  const addPkg = () => {
    for (const p of pkgInput.value.split(/[\s,]+/).filter(Boolean)) if (!packages.includes(p)) packages.push(p);
    pkgInput.value = "";
    drawChips();
  };
  pkgInput.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); addPkg(); } });
  const addBtn = h("button", { type: "button" }, "Add");
  addBtn.addEventListener("click", addPkg);
  const packagesPane = h("div", { class: "stack" },
    h("label", { for: "package-input" }, "Nixpkgs packages"),
    h("div", { class: "command" }, pkgInput, addBtn),
    h("datalist", { id: "package-suggestions" }, suggestedPackages.map((p) => h("option", { value: p }))),
    chips,
    h("p", { class: "hint" }, "Names are nixpkgs attributes, as on search.nixos.org. git, tmux, and chezmoi are always included."));

  const module = h("textarea", { id: "profile-module", class: "code", spellcheck: "false", rows: 14 });
  module.value = spec.module || "";
  const fromPackages = h("button", { type: "button" }, "Start from the package list");
  fromPackages.addEventListener("click", () => {
    module.value = `{ pkgs, ... }: {\n  home.packages = with pkgs; [\n${packages.map((p) => "    " + p).join("\n")}\n  ];\n}\n`;
  });
  const modulePane = h("div", { class: "stack" },
    h("label", { for: "profile-module" }, "Home Manager module"), module, h("div", {}, fromPackages),
    h("p", { class: "hint" }, "A Home Manager module evaluated with the server's pinned nixpkgs and Home Manager. Packages it adds reach your environments."));

  const tabPackages = h("button", { type: "button", role: "tab" }, "Packages");
  const tabModule = h("button", { type: "button", role: "tab" }, "Home Manager module");
  const setMode = (m) => {
    mode = m;
    packagesPane.hidden = m !== "packages";
    modulePane.hidden = m !== "module";
    tabPackages.setAttribute("aria-selected", String(m === "packages"));
    tabModule.setAttribute("aria-selected", String(m === "module"));
  };
  tabPackages.addEventListener("click", () => setMode("packages"));
  tabModule.addEventListener("click", () => setMode("module"));
  setMode(mode);

  const unfree = h("input", { type: "checkbox", id: "profile-unfree" });
  unfree.checked = !!spec.unfree;
  const repo = h("input", { id: "dotfiles-repo", type: "url", spellcheck: "false", placeholder: "https://github.com/you/dotfiles", value: (spec.dotfiles && spec.dotfiles.repo) || "" });
  const ref = h("input", { id: "dotfiles-ref", spellcheck: "false", placeholder: "main", value: (spec.dotfiles && spec.dotfiles.ref) || "" });
  const save = h("button", { class: "primary", type: "submit" }, isNew ? "Create and build" : "Save and build");
  let remove = null;
  if (!isNew) {
    remove = h("button", { type: "button", class: "danger" }, "Delete profile");
    remove.addEventListener("click", busy(remove, "Deleting…", async () => {
      if (!confirm(`Delete the ${profile.name} profile? Environments using it must be deleted first.`)) return;
      await api("DELETE", `/v1/profiles/${encodeURIComponent(profile.id)}`);
      location.hash = "#profiles";
      status(`Deleted ${profile.name}.`, "ok");
    }));
  }
  const form = h("form", { class: "stack wide" },
    isNew ? h("label", { for: "profile-name" }, "Name", name) : null,
    h("div", { class: "tabs", role: "tablist" }, tabPackages, tabModule),
    packagesPane, modulePane,
    h("label", { class: "inline", for: "profile-unfree" }, unfree, " Allow unfree packages (Claude Code needs this)"),
    h("div", { class: "row" }, h("label", { for: "dotfiles-repo" }, "Dotfiles repository (optional, public https)", repo), h("label", { for: "dotfiles-ref" }, "Branch, tag, or commit", ref)),
    h("div", {}, save, remove ? [" ", remove] : null));
  form.addEventListener("submit", busy(save, "Saving…", async () => {
    if (mode === "packages") addPkg();
    const body = { unfree: unfree.checked };
    if (mode === "module") body.module = module.value;
    else body.packages = packages;
    if (repo.value.trim()) body.dotfiles = { repo: repo.value.trim(), ref: ref.value.trim() };
    if (isNew) {
      const created = await api("POST", "/v1/profiles", { name: name.value.trim(), spec: body });
      location.hash = `#profile/${created.id}`;
    } else {
      await api("POST", `/v1/profiles/${encodeURIComponent(profile.id)}/revisions`, { spec: body });
      await views.profile(profile.id);
    }
    status("Saved. The build has started; this page updates as it runs.", "ok");
  }));

  const history = profile ? h("div", {},
    h("h2", {}, "Revisions"),
    profile.revisions.map((r, i) => h("details", { class: "revision", open: i === 0 },
      h("summary", {}, `#${r.number} `, stateBadge(r.state), r.pruned ? [" ", h("span", { class: "badge", title: "the drive was removed because nothing uses it; saving the same packages rebuilds it" }, "pruned")] : null, " ", h("span", { class: "muted" }, when(r.created_at)),
        r.dotfiles_commit && r.dotfiles_commit !== "none" ? h("span", { class: "muted mono" }, ` dotfiles ${r.dotfiles_commit.slice(0, 12)}`) : null),
      r.spec.module ? h("p", { class: "hint" }, "Home Manager module") : h("p", { class: "hint" }, (r.spec.packages || []).join(", ") || "only the required tools"),
      r.error ? h("p", { class: "status error" }, r.error) : null,
      r.log_tail ? h("pre", { class: "log", tabindex: "0", "aria-label": `Build log for revision ${r.number}` }, r.log_tail) : null))) : null;

  render(
    h("p", {}, h("a", { href: "#profiles" }, "← Profiles")),
    h("h1", {}, isNew ? "New profile" : profile.name),
    h("div", { class: "card" }, form),
    history);
  for (const log of document.querySelectorAll("pre.log")) log.scrollTop = log.scrollHeight;
  if (profile && profile.revisions.some((r) => r.state === "pending" || r.state === "building")) {
    const poll = async () => {
      if (location.hash !== `#profile/${profile.id}`) return;
      if (form.contains(document.activeElement)) refreshIn(4000, poll);
      else await views.profile(profile.id);
    };
    refreshIn(4000, poll);
  }
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
          h("td", {}, aliasOf(e.image_id), mainID && e.image_id !== mainID ? [" ", h("span", { class: "badge warn", title: "main points at a newer image; recreate to use it" }, "main moved")] : null,
            e.profile ? h("div", { class: "hint" }, `${e.profile} #${e.profile_revision}`, e.profile_latest_ready > e.profile_revision ? [" ", h("span", { class: "badge warn", title: "a newer revision is ready; recreate to use it" }, `#${e.profile_latest_ready} ready`)] : null) : null),
          h("td", { class: "optional" }, `${e.vcpus} vCPU · ${e.memory_mib} MiB`),
          h("td", {}, command(`boxfacectl connect ${e.name}`))))))));
};

window.addEventListener("hashchange", route);
start().catch(fail);
