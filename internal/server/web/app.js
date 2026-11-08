// Observer dashboard — vanilla JS, no build step.
//
// Flow: fetch /api/project -> populate the tab rail -> on tab click, fetch
// /api/artifact?tab=NAME and render it per the tab's `renderer`. The Update
// button POSTs /api/update?tab=NAME (with the required X-Observer header,
// R15) then re-fetches the artifact. Every /api/* request also carries
// X-Observer-Token (read once from the server-embedded meta tag) since the
// server requires it on all API routes.
(function () {
  "use strict";

  var state = {
    project: null, // { project, tabs, status }
    activeTab: null, // manifest.Tab
    vizInstance: null, // cached Viz.js instance (lazy)
  };

  var mermaidCounter = 0;

  // Auth: the server set an HttpOnly, SameSite=Strict `observer_token` cookie
  // on the authenticated index load (reached via the /?token=... URL that
  // `observer serve` prints). The browser sends that cookie automatically on
  // every same-origin /api/* request, so no token handling is needed here.
  var els = {
    tabs: document.getElementById("tabs"),
    projectName: document.getElementById("project-name"),
    tabTitle: document.getElementById("tab-title"),
    freshness: document.getElementById("freshness"),
    updateBtn: document.getElementById("update-btn"),
    content: document.getElementById("content"),
  };

  if (window.mermaid) {
    window.mermaid.initialize({ startOnLoad: false, theme: "dark", securityLevel: "strict" });
  }

  els.updateBtn.addEventListener("click", onUpdateClick);

  loadProject();

  // ---- Data loading ----

  function loadProject() {
    fetch("/api/project")
      .then(function (r) {
        if (!r.ok) throw new Error("GET /api/project: " + r.status);
        return r.json();
      })
      .then(function (data) {
        state.project = data;
        els.projectName.textContent = data.project || "observer";
        renderTabRail(data);
        if (!state.activeTab && data.tabs && data.tabs.length > 0) {
          selectTab(data.tabs[0]);
        } else if (state.activeTab) {
          // Keep the same tab selected but refresh its freshness line.
          var still = findTab(data.tabs, state.activeTab.name);
          if (still) selectTab(still, { skipContentReload: true });
        }
      })
      .catch(function (err) {
        els.projectName.textContent = "error";
        els.tabs.innerHTML =
          '<div class="tab-empty">Failed to load project:<br>' +
          escapeHtml(err.message) +
          "</div>";
      });
  }

  function findTab(tabs, name) {
    for (var i = 0; i < (tabs || []).length; i++) {
      if (tabs[i].name === name) return tabs[i];
    }
    return null;
  }

  function renderTabRail(data) {
    els.tabs.innerHTML = "";
    if (!data.tabs || data.tabs.length === 0) {
      els.tabs.innerHTML = '<div class="tab-empty">No tabs configured.</div>';
      return;
    }
    data.tabs.forEach(function (tab) {
      var btn = document.createElement("button");
      btn.className = "tab-btn";
      btn.type = "button";
      if (state.activeTab && state.activeTab.name === tab.name) {
        btn.className += " active";
      }

      var label = document.createElement("span");
      label.textContent = tab.name;
      btn.appendChild(label);

      var dot = document.createElement("span");
      dot.className = "dot";
      var status = (data.status || {})[tab.name];
      if (status) dot.className += status.error ? " error" : " fresh";
      btn.appendChild(dot);

      btn.addEventListener("click", function () {
        selectTab(tab);
      });
      els.tabs.appendChild(btn);
    });
  }

  function selectTab(tab, opts) {
    opts = opts || {};
    state.activeTab = tab;
    els.tabTitle.textContent = tab.name;
    els.updateBtn.disabled = tab.kind !== "generator";
    markActiveInRail(tab.name);
    renderFreshness(tab);
    if (!opts.skipContentReload) {
      loadArtifact(tab);
    }
  }

  function markActiveInRail(name) {
    var buttons = els.tabs.querySelectorAll(".tab-btn");
    buttons.forEach(function (btn) {
      var label = btn.querySelector("span");
      var isActive = label && label.textContent === name;
      btn.classList.toggle("active", !!isActive);
    });
  }

  function renderFreshness(tab) {
    var status = (state.project.status || {})[tab.name];
    if (!status) {
      els.freshness.textContent = "not generated yet";
      return;
    }
    if (status.error) {
      els.freshness.innerHTML = '<span class="err">last run failed</span>';
      return;
    }
    var shortHead = (status.head || "").slice(0, 7);
    var when = relativeTime(status.generated_at);
    var parts = [];
    if (status.branch) parts.push("branch " + status.branch);
    if (shortHead) parts.push("commit " + shortHead);
    if (when) parts.push(when);
    els.freshness.textContent = parts.join(" · ");
  }

  function loadArtifact(tab) {
    els.content.innerHTML = '<p class="empty">Loading&hellip;</p>';
    fetch("/api/artifact?tab=" + encodeURIComponent(tab.name))
      .then(function (r) {
        if (r.status === 404) {
          els.content.innerHTML =
            '<p class="empty">Not generated yet &mdash; click <strong>Update</strong>.</p>';
          return null;
        }
        if (!r.ok) {
          return r.text().then(function (body) {
            throw new Error("GET /api/artifact: " + r.status + " " + body);
          });
        }
        return r.text();
      })
      .then(function (text) {
        if (text === null) return;
        renderArtifact(tab, text);
      })
      .catch(function (err) {
        showError(err.message);
      });
  }

  function onUpdateClick() {
    var tab = state.activeTab;
    if (!tab) return;
    els.updateBtn.disabled = true;
    els.updateBtn.classList.add("busy");
    els.updateBtn.textContent = "Updating…";

    fetch("/api/update?tab=" + encodeURIComponent(tab.name), {
      method: "POST",
      headers: { "X-Observer": "1" },
    })
      .then(function (r) {
        return r.json().then(function (artifact) {
          return { ok: r.ok, artifact: artifact };
        });
      })
      .then(function (result) {
        state.project.status = state.project.status || {};
        state.project.status[tab.name] = result.artifact;
        renderFreshness(tab);
        renderTabRail(state.project);
        if (!result.ok || result.artifact.error) {
          showError(result.artifact.error || "update failed");
          return;
        }
        loadArtifact(tab);
      })
      .catch(function (err) {
        showError(err.message);
      })
      .finally(function () {
        els.updateBtn.disabled = tab.kind !== "generator";
        els.updateBtn.classList.remove("busy");
        els.updateBtn.textContent = "Update";
      });
  }

  function showError(message) {
    els.content.innerHTML =
      '<div class="error-banner">' + escapeHtml(message) + "</div>";
  }

  // ---- Renderers, dispatched by tab.renderer ----

  function renderArtifact(tab, text) {
    switch (tab.renderer) {
      case "markdown":
        return renderMarkdown(text);
      case "svg":
        return renderSVG(text);
      case "mermaid":
        return renderMermaid(text);
      case "graph":
        return renderGraph(text);
      case "table":
        return renderTable(text);
      case "dbml":
        return renderRaw(text);
      default:
        return renderRaw(text);
    }
  }

  // Artifacts are derived from repo content (commit messages, file/table names,
  // identifiers) and Observer may analyze untrusted repos, so all rendered HTML
  // is sanitized with DOMPurify before innerHTML assignment (defense in depth —
  // the dashboard is same-origin with an API that runs shell commands).
  function safeHTML(html) {
    return window.DOMPurify ? window.DOMPurify.sanitize(html) : escapeHtml(html);
  }
  function safeSVG(svg) {
    return window.DOMPurify
      ? window.DOMPurify.sanitize(svg, { USE_PROFILES: { svg: true, svgFilters: true } })
      : "";
  }

  function renderMarkdown(text) {
    var html = window.marked ? window.marked.parse(text) : escapeHtml(text);
    var div = document.createElement("div");
    div.className = "render-markdown";
    div.innerHTML = safeHTML(html);
    setContent(div);
  }

  function renderSVG(text) {
    var div = document.createElement("div");
    div.className = "render-svg";
    div.innerHTML = safeSVG(text);
    setContent(div);
    mountInteractiveSVG(div);
  }

  function renderMermaid(text) {
    var div = document.createElement("div");
    div.className = "render-mermaid";
    setContent(div);
    if (!window.mermaid) {
      div.innerHTML =
        '<div class="error-banner">mermaid.js not available</div>';
      return;
    }
    var id = "observer-mermaid-" + ++mermaidCounter;
    window.mermaid
      .render(id, text)
      .then(function (result) {
        div.innerHTML = safeSVG(result.svg);
      })
      .catch(function (err) {
        div.innerHTML =
          '<div class="error-banner">mermaid render failed: ' +
          escapeHtml(String(err)) +
          "</div>";
      });
  }

  function renderGraph(text) {
    var div = document.createElement("div");
    div.className = "render-graph";
    setContent(div);
    if (!window.Viz) {
      div.innerHTML = '<div class="error-banner">viz.js not available</div>';
      return;
    }
    getViz()
      .then(function (viz) {
        var svgEl = viz.renderSVGElement(text);
        div.innerHTML = safeSVG(svgEl.outerHTML);
        mountInteractiveSVG(div);
      })
      .catch(function (err) {
        div.innerHTML =
          '<div class="error-banner">graphviz render failed: ' +
          escapeHtml(String(err)) +
          "</div>";
      });
  }

  function getViz() {
    if (state.vizInstance) return Promise.resolve(state.vizInstance);
    return window.Viz.instance().then(function (viz) {
      state.vizInstance = viz;
      return viz;
    });
  }

  // ---- Interactive graph viewport (pan / zoom / focus) ----
  //
  // Turns a freshly-rendered graphviz SVG (viz.js graph tabs or raw
  // go-callvis output) into something explorable instead of a static wall of
  // boxes: scroll-wheel to zoom toward the cursor, drag to pan, and click a
  // node to focus it — the node, its incident edges, and their adjacent nodes
  // stay lit while everything else dims, so a single call's neighbourhood is
  // legible in an otherwise dense graph. A small control cluster offers zoom
  // in / out and fit-to-view. Operates purely on the already-sanitized DOM
  // (event listeners plus class/transform toggles), so it introduces no new
  // HTML-injection surface.
  function mountInteractiveSVG(container) {
    var svg = container.querySelector("svg");
    if (!svg) return;

    // Normalise the svg so our transforms are the only thing sizing it.
    svg.removeAttribute("width");
    svg.removeAttribute("height");
    svg.style.transformOrigin = "0 0";
    svg.style.position = "absolute";
    svg.style.top = "0";
    svg.style.left = "0";
    svg.style.maxWidth = "none";

    // Intrinsic graph size (graphviz always emits a viewBox).
    var vb = (svg.getAttribute("viewBox") || "").split(/\s+/).map(Number);
    var gw = vb.length === 4 && vb[2] ? vb[2] : 1000;
    var gh = vb.length === 4 && vb[3] ? vb[3] : 1000;
    svg.style.width = gw + "px";
    svg.style.height = gh + "px";

    var view = { scale: 1, tx: 0, ty: 0 };
    var MIN = 0.05,
      MAX = 8;

    function clamp(s) {
      return Math.max(MIN, Math.min(MAX, s));
    }
    function apply() {
      svg.style.transform =
        "translate(" + view.tx + "px," + view.ty + "px) scale(" + view.scale + ")";
    }
    function fit() {
      var cw = container.clientWidth,
        ch = container.clientHeight;
      if (!cw || !ch) return;
      view.scale = clamp(Math.min(cw / gw, ch / gh) * 0.95);
      view.tx = (cw - gw * view.scale) / 2;
      view.ty = (ch - gh * view.scale) / 2;
      apply();
    }
    function zoomAt(px, py, factor) {
      var gx = (px - view.tx) / view.scale;
      var gy = (py - view.ty) / view.scale;
      view.scale = clamp(view.scale * factor);
      view.tx = px - gx * view.scale;
      view.ty = py - gy * view.scale;
      apply();
    }
    function zoomCenter(factor) {
      zoomAt(container.clientWidth / 2, container.clientHeight / 2, factor);
    }

    container.addEventListener(
      "wheel",
      function (e) {
        e.preventDefault();
        var rect = container.getBoundingClientRect();
        zoomAt(
          e.clientX - rect.left,
          e.clientY - rect.top,
          e.deltaY < 0 ? 1.15 : 1 / 1.15
        );
      },
      { passive: false }
    );

    // Drag to pan. `moved` lets the focus handler tell a pan from a click.
    var dragging = false,
      moved = false,
      lastX = 0,
      lastY = 0;
    container.addEventListener("pointerdown", function (e) {
      dragging = true;
      moved = false;
      lastX = e.clientX;
      lastY = e.clientY;
      try {
        container.setPointerCapture(e.pointerId);
      } catch (_) {}
      container.classList.add("gv-grabbing");
    });
    container.addEventListener("pointermove", function (e) {
      if (!dragging) return;
      var dx = e.clientX - lastX,
        dy = e.clientY - lastY;
      if (!moved && (Math.abs(dx) > 3 || Math.abs(dy) > 3)) moved = true;
      if (moved) {
        view.tx += dx;
        view.ty += dy;
        lastX = e.clientX;
        lastY = e.clientY;
        apply();
      }
    });
    function endDrag(e) {
      dragging = false;
      container.classList.remove("gv-grabbing");
      try {
        container.releasePointerCapture(e.pointerId);
      } catch (_) {}
    }
    container.addEventListener("pointerup", endDrag);
    container.addEventListener("pointercancel", endDrag);

    wireGraphFocus(svg, function wasDrag() {
      return moved;
    });

    container.appendChild(
      buildGraphControls({
        zoomIn: function () {
          zoomCenter(1.3);
        },
        zoomOut: function () {
          zoomCenter(1 / 1.3);
        },
        reset: fit,
      })
    );

    // Fit once layout has settled (container has a measurable size).
    requestAnimationFrame(fit);
  }

  // wireGraphFocus wires click-to-focus over a graphviz svg's node/edge
  // groups. Adjacency is read from each edge's <title> ("tail->head" for
  // digraphs, "tail--head" for undirected), matched against node <title>s.
  function wireGraphFocus(svg, wasDrag) {
    var nodes = Array.prototype.slice.call(svg.querySelectorAll("g.node"));
    var edges = Array.prototype.slice.call(svg.querySelectorAll("g.edge"));
    if (nodes.length === 0) return;

    function titleOf(g) {
      var t = g.querySelector("title");
      return t ? t.textContent.trim() : "";
    }

    var nodeByName = {};
    nodes.forEach(function (g) {
      nodeByName[titleOf(g)] = g;
    });

    var adj = {};
    function ensure(n) {
      return adj[n] || (adj[n] = { nbrs: {}, edges: [] });
    }
    edges.forEach(function (g) {
      var parts = titleOf(g).split(/->|--/);
      var a = parts[0] && parts[0].trim();
      var b = parts[1] && parts[1].trim();
      if (nodeByName[a]) {
        ensure(a).nbrs[b] = true;
        ensure(a).edges.push(g);
      }
      if (nodeByName[b]) {
        ensure(b).nbrs[a] = true;
        ensure(b).edges.push(g);
      }
    });

    var focused = null;
    var all = nodes.concat(edges);

    function clearFocus() {
      focused = null;
      svg.classList.remove("gv-has-focus");
      all.forEach(function (g) {
        g.classList.remove("gv-dim", "gv-lit", "gv-root");
      });
    }
    function focusNode(name) {
      focused = name;
      svg.classList.add("gv-has-focus");
      var keep = {};
      keep[name] = true;
      var a = adj[name] || { nbrs: {}, edges: [] };
      Object.keys(a.nbrs).forEach(function (n) {
        keep[n] = true;
      });
      nodes.forEach(function (g) {
        var nm = titleOf(g);
        g.classList.toggle("gv-lit", !!keep[nm]);
        g.classList.toggle("gv-dim", !keep[nm]);
        g.classList.toggle("gv-root", nm === name);
      });
      edges.forEach(function (g) {
        var on = a.edges.indexOf(g) !== -1;
        g.classList.toggle("gv-lit", on);
        g.classList.toggle("gv-dim", !on);
      });
    }

    nodes.forEach(function (g) {
      g.style.cursor = "pointer";
      g.addEventListener("click", function (e) {
        if (wasDrag()) return;
        e.stopPropagation();
        var nm = titleOf(g);
        if (focused === nm) clearFocus();
        else focusNode(nm);
      });
    });
    svg.addEventListener("click", function () {
      if (wasDrag()) return;
      clearFocus();
    });
  }

  function buildGraphControls(handlers) {
    var wrap = document.createElement("div");
    wrap.className = "gv-controls";
    var specs = [
      ["+", handlers.zoomIn, "Zoom in"],
      ["−", handlers.zoomOut, "Zoom out"],
      ["⤢", handlers.reset, "Fit to view"],
    ];
    specs.forEach(function (spec) {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "gv-btn";
      b.textContent = spec[0];
      b.title = spec[2];
      b.addEventListener("click", function (e) {
        e.stopPropagation();
        spec[1]();
      });
      // Don't let interacting with the controls start a pan or clear focus.
      b.addEventListener("pointerdown", function (e) {
        e.stopPropagation();
      });
      wrap.appendChild(b);
    });
    return wrap;
  }

  function renderTable(text) {
    var data;
    try {
      data = JSON.parse(text);
    } catch (err) {
      return renderRaw(text);
    }
    var rows = Array.isArray(data) ? data : toRows(data);
    if (!Array.isArray(rows) || rows.length === 0) {
      setContent(rawPre("(empty)"));
      return;
    }
    var columns = collectColumns(rows);
    var table = document.createElement("table");
    table.className = "render-table";

    var thead = document.createElement("thead");
    var headRow = document.createElement("tr");
    columns.forEach(function (col) {
      var th = document.createElement("th");
      th.textContent = col;
      headRow.appendChild(th);
    });
    thead.appendChild(headRow);
    table.appendChild(thead);

    var tbody = document.createElement("tbody");
    rows.forEach(function (row) {
      var tr = document.createElement("tr");
      columns.forEach(function (col) {
        var td = document.createElement("td");
        td.textContent = formatCell(row[col]);
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    setContent(table);
  }

  // toRows turns a plain object (not an array) into a list of {key, value}
  // rows so arbitrary JSON (e.g. {"a.go": 3, "b.go": 1}) still tabulates.
  function toRows(obj) {
    if (obj === null || typeof obj !== "object") {
      return [{ value: String(obj) }];
    }
    return Object.keys(obj).map(function (key) {
      return { key: key, value: obj[key] };
    });
  }

  function collectColumns(rows) {
    var seen = [];
    var set = {};
    rows.forEach(function (row) {
      if (row === null || typeof row !== "object") return;
      Object.keys(row).forEach(function (k) {
        if (!set[k]) {
          set[k] = true;
          seen.push(k);
        }
      });
    });
    return seen.length > 0 ? seen : ["value"];
  }

  function formatCell(v) {
    if (v === undefined) return "";
    if (v === null) return "null";
    if (typeof v === "object") return JSON.stringify(v);
    return String(v);
  }

  function renderRaw(text) {
    setContent(rawPre(text));
  }

  function rawPre(text) {
    var pre = document.createElement("pre");
    pre.className = "render-raw";
    pre.textContent = text;
    return pre;
  }

  function setContent(node) {
    els.content.innerHTML = "";
    els.content.appendChild(node);
  }

  // ---- Small utils ----

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  function relativeTime(iso) {
    if (!iso) return "";
    var then = new Date(iso).getTime();
    if (isNaN(then)) return iso;
    var seconds = Math.max(0, Math.floor((Date.now() - then) / 1000));
    if (seconds < 60) return "generated just now";
    var minutes = Math.floor(seconds / 60);
    if (minutes < 60) return "generated " + minutes + "m ago";
    var hours = Math.floor(minutes / 60);
    if (hours < 24) return "generated " + hours + "h ago";
    var days = Math.floor(hours / 24);
    return "generated " + days + "d ago";
  }
})();
