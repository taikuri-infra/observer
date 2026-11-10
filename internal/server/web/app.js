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

    // ---- git-aware map: View one ref / Compare two refs ----
    gitRefsPromise: null, // shared in-flight/resolved /api/gitrefs fetch
    gitRefs: null, // { current, branches, tags } once loaded
    // Per (tab, ref) cache of a built graph.json from /api/graphat. A ref's
    // graph is immutable per commit (the backend caches per commit too), so
    // re-selecting a ref is instant. "" / "working" never goes through here
    // (it's the already-loaded artifact). Promise-valued so concurrent
    // selects share one in-flight fetch.
    graphAtCache: Object.create(null), // "tab\nref" -> Promise<graphData>
    // Staleness guard: bumped on every (re)render AND every selection
    // change, so a late /api/graphat response for a since-superseded
    // selection is ignored instead of painting a stale graph.
    mapGen: 0,
    // Per-map UI state, sticky across re-renders WITHIN a tab (reset when
    // the active tab changes): which mode and which refs are selected.
    mapView: null, // { mode, viewRef, fromRef, toRef, tabName, workingData }
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
    // Invalidate any in-flight /api/graphat map build before we repaint:
    // refreshMapView() only bumps state.mapGen on map→map transitions, so
    // switching from a map tab to a NON-map tab (or reloading the same
    // tab's artifact) would otherwise let a late build's .then() paint a
    // stale map over this tab's content. Bumping here fences all of them.
    state.mapGen++;
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
    // The working tree is about to change (a regenerate, and the user may
    // have just switched branches), so the client-side per-ref graph cache
    // and the cached ref list are both potentially stale: a branch that
    // advanced would otherwise keep serving its old graph, and a
    // newly-created branch would never appear in the pickers. Drop both;
    // the per-commit SERVER cache (keyed by SHA, not ref name) stays valid
    // and remains the real cache.
    state.graphAtCache = Object.create(null);
    state.gitRefsPromise = null;
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
    // The map renderer owns the whole content area (its own rail + canvas,
    // no outer scroll) — every other renderer uses the padded, scrollable
    // default. Toggled on every dispatch so leaving the map tab restores
    // normal layout.
    els.content.classList.toggle("content--map", tab.renderer === "map");
    switch (tab.renderer) {
      case "markdown":
        return renderMarkdown(text);
      case "svg":
        return renderSVG(text);
      case "mermaid":
        return renderMermaid(text);
      case "graph":
        return renderGraph(text);
      case "map":
        return renderMap(text);
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

  // ---- Service map renderer ("map") ----
  //
  // Ported from the design mockup (observer-dagre.html): real dagre
  // hierarchical layout, SVG/CSS nodes & edges, filter rail, pan/zoom,
  // neighbour-focus, and a detail side panel. The artifact is graph.json
  // (see docs/superpowers/specs/2026-11-08-canvas-service-map-design.md
  // §4) — already carries precomputed metrics/location/description/links,
  // so (unlike the mockup) this renderer does no degree/doc computation of
  // its own, only layout and drawing.
  //
  // Security: every dynamic string (ids, labels, doc text, task ids) is set
  // via textContent, never innerHTML/outerHTML — this renderer never calls
  // safeHTML/safeSVG because it never builds HTML from strings at all.
  var MAP_KIND_VAR = {
    entry: "var(--map-entry)",
    service: "var(--map-service)",
    adapter: "var(--map-adapter)",
    external: "var(--map-external)",
    table: "var(--map-table)",
    module: "var(--map-module)",
    func: "var(--map-func)",
  };
  var MAP_KIND_NAME = {
    entry: "Entry / HTTP",
    service: "Application",
    adapter: "Adapter / DB",
    external: "External",
    table: "Table",
    module: "Module",
    func: "Function",
  };
  var MAP_KIND_GLYPH = {
    entry: "⇄",
    service: "◆",
    adapter: "⌿",
    external: "◇",
    table: "▦",
    module: "▣",
    func: "ƒ",
  };
  var MAP_KIND_ORDER = ["entry", "service", "adapter", "external", "table", "module", "func"];
  // Human labels for the meta.sizeBy/colorBy metric keys (legend text).
  // Unknown keys fall back to the raw key, so a future runtime metric
  // (reqPerSec, latencyMs, …) still labels sensibly.
  var MAP_METRIC_LABEL = {
    dependents: "dependents (in-degree)",
    imports: "imports (out-degree)",
    churn: "churn",
    loc: "lines of code",
    callers: "callers (in-degree)",
    callees: "callees (out-degree)",
    columns: "columns",
    fkIn: "foreign keys in",
    fkOut: "foreign keys out",
    coverage: "test coverage (static proxy)",
  };
  function mapMetricLabel(key) {
    return MAP_METRIC_LABEL[key] || key;
  }

  // Short labels for the metric cells (hover card + detail panel). Per-kind
  // metrics are data-driven off these, not hardcoded to Dependents/Imports/
  // Churn — so a "table" node shows Columns/FK in/FK out and a "func" node
  // shows Callers/Callees, instead of three meaningless zeros.
  var MAP_METRIC_CELL = {
    dependents: "Dependents",
    imports: "Imports",
    churn: "Churn",
    loc: "LOC",
    columns: "Columns",
    fkIn: "FK in",
    fkOut: "FK out",
    callers: "Callers",
    callees: "Callees",
    coverage: "Coverage %",
  };
  // Compact forms for the on-canvas node sublabel (space is tight there).
  var MAP_METRIC_ABBR = {
    dependents: "dep",
    imports: "imp",
    churn: "churn",
    loc: "loc",
    columns: "cols",
    fkIn: "fk←",
    fkOut: "fk→",
    callers: "in",
    callees: "out",
    coverage: "cov",
  };
  // The order metrics are considered in for display (most salient first).
  // coverage is LAST on purpose: the sublabel shows only the first 2 and the
  // hover card the first 3 entries, so coverage appears in the full detail
  // panel without crowding the canvas (the coverage overlay conveys it via
  // ring color anyway).
  var MAP_METRIC_ORDER = [
    "dependents",
    "imports",
    "callers",
    "callees",
    "columns",
    "fkIn",
    "fkOut",
    "churn",
    "loc",
    "coverage",
  ];
  // nodeMetricEntries returns a node's non-zero metrics as [key, value]
  // pairs in MAP_METRIC_ORDER — the shared basis for the sublabel, hover
  // card, and panel so each node kind shows only the metrics it actually
  // carries.
  function nodeMetricEntries(n) {
    var m = n.metrics || {};
    var out = [];
    MAP_METRIC_ORDER.forEach(function (k) {
      if (typeof m[k] === "number" && m[k] !== 0 && MAP_METRIC_CELL[k]) {
        out.push([k, m[k]]);
      }
    });
    return out;
  }

  function svgEl(tag, attrs) {
    var e = document.createElementNS("http://www.w3.org/2000/svg", tag);
    for (var k in attrs) {
      if (Object.prototype.hasOwnProperty.call(attrs, k)) e.setAttribute(k, attrs[k]);
    }
    return e;
  }

  function mapEdgePath(points) {
    if (!points || points.length < 2) return "";
    var d = "M" + points[0].x + "," + points[0].y;
    for (var i = 1; i < points.length; i++) {
      var p = points[i],
        prev = points[i - 1],
        mx = (prev.x + p.x) / 2,
        my = (prev.y + p.y) / 2;
      d += " Q" + prev.x + "," + prev.y + " " + mx + "," + my;
    }
    var last = points[points.length - 1];
    d += " L" + last.x + "," + last.y;
    return d;
  }

  // ---- git-aware map: data loading (View / Compare across refs) ----
  //
  // /api/gitrefs and /api/graphat are plain same-origin GETs (no mutation,
  // so — like /api/project and /api/artifact — no X-Observer CSRF header
  // is needed); the browser sends the observer_token cookie automatically.

  function fetchGitRefs() {
    if (state.gitRefsPromise) return state.gitRefsPromise;
    state.gitRefsPromise = fetch("/api/gitrefs")
      .then(function (r) {
        if (!r.ok) throw new Error("GET /api/gitrefs: " + r.status);
        return r.json();
      })
      .then(function (data) {
        state.gitRefs = data;
        return data;
      })
      .catch(function (err) {
        state.gitRefsPromise = null; // allow a retry on the next map render
        throw err;
      });
    return state.gitRefsPromise;
  }

  // isWorkingRef treats "" and "working" as the current working tree — the
  // already-loaded artifact, never a /api/graphat fetch.
  function isWorkingRef(ref) {
    return ref === "" || ref === "working";
  }

  // fetchGraphAt builds (server-side, in an isolated worktree, cached per
  // commit) tab's graph.json as it existed at ref, and parses it. On a ref
  // whose code doesn't build the server returns a JSON { error } with a
  // non-2xx status; we surface that error's message so the caller can show
  // it in the canvas instead of a broken UI.
  function fetchGraphAt(tabName, ref) {
    return fetch("/api/graphat?tab=" + encodeURIComponent(tabName) + "&ref=" + encodeURIComponent(ref))
      .then(function (r) {
        return r.text().then(function (body) {
          if (!r.ok) {
            var msg = body;
            try {
              var j = JSON.parse(body);
              if (j && j.error) msg = j.error;
            } catch (e) {
              /* non-JSON error body: use the raw text */
            }
            throw new Error(msg || "GET /api/graphat: " + r.status);
          }
          var data = JSON.parse(body);
          if (!data || !Array.isArray(data.nodes)) {
            throw new Error('graph at "' + ref + '" has no "nodes" array');
          }
          return data;
        });
      });
  }

  // graphForRef resolves tab's graph at ref: the current working tree
  // (workingData, instant — no fetch) for a working ref, else a cached
  // /api/graphat build. A failed build is NOT cached, so changing to a
  // buildable ref and back retries.
  function graphForRef(tabName, ref, workingData) {
    if (isWorkingRef(ref)) return Promise.resolve(workingData);
    var key = tabName + "\n" + ref;
    if (state.graphAtCache[key]) return state.graphAtCache[key];
    var p = fetchGraphAt(tabName, ref);
    state.graphAtCache[key] = p;
    p.catch(function () {
      delete state.graphAtCache[key];
    });
    return p;
  }

  // defaultBaseRef is the ref Compare mode defaults its "From" to: the
  // repo's main/master if present, else the currently checked-out branch,
  // else the first branch — matching the old overlay's base default.
  function defaultBaseRef(refs) {
    if (!refs) return "";
    var branches = refs.branches || [];
    if (branches.indexOf("main") >= 0) return "main";
    if (branches.indexOf("master") >= 0) return "master";
    if (refs.current) return refs.current;
    return branches[0] || "";
  }

  // ---- git-aware map: structural diff of two graphs ----
  //
  // Compare mode diffs the From and To graphs purely by node id + local
  // structure (no git/file knowledge — that's /api/graphat's job):
  //   - added    = id in To, not in From
  //   - removed  = id in From, not in To (listed in the rail; not drawn,
  //                since the canvas shows the To graph)
  //   - changed  = id in both, but its sorted outgoing-edge target ids OR
  //                its structural metrics differ
  //   - unchanged = everything else
  //
  // KNOWN LIMITATION (v1, acceptable): a function whose BODY changed but
  // whose calls + metrics didn't is seen as "unchanged" — structural diff
  // can't see inside a body. Noted, not fixed here.

  // STRUCTURAL_METRIC_KEYS are the metrics that reflect a node's shape in
  // the graph (degree/cardinality), NOT its volatile history. churn and loc
  // are deliberately EXCLUDED: both legitimately differ between two refs
  // (churn counts commits up to each ref; loc tracks file size) even when a
  // node's structure is identical, so including them would mark almost
  // everything "changed".
  var STRUCTURAL_METRIC_KEYS = [
    "dependents",
    "imports",
    "callers",
    "callees",
    "columns",
    "fkIn",
    "fkOut",
  ];

  // buildAdjacency returns a from-id -> sorted unique target-id[] map for a
  // graph's edges, computed in ONE pass. Precomputing this once per graph
  // turns diffGraphs from O(N·E) (outgoingTargets rescanned every edge per
  // node) into O(N + E).
  function buildAdjacency(edges) {
    var seen = Object.create(null); // from -> { to -> true }
    var adj = Object.create(null); // from -> [to]
    edges.forEach(function (e) {
      var s = seen[e.from];
      if (!s) {
        s = seen[e.from] = Object.create(null);
        adj[e.from] = [];
      }
      if (!s[e.to]) {
        s[e.to] = true;
        adj[e.from].push(e.to);
      }
    });
    Object.keys(adj).forEach(function (from) {
      adj[from].sort();
    });
    return adj;
  }

  // targetsFor returns a node id's sorted outgoing targets from a prebuilt
  // adjacency map (empty array when the node has no outgoing edges).
  function targetsFor(adj, id) {
    return adj[id] || [];
  }

  function sameStringArray(a, b) {
    if (a.length !== b.length) return false;
    for (var i = 0; i < a.length; i++) {
      if (a[i] !== b[i]) return false;
    }
    return true;
  }

  function structuralMetricsDiffer(a, b) {
    var ma = (a && a.metrics) || {};
    var mb = (b && b.metrics) || {};
    for (var i = 0; i < STRUCTURAL_METRIC_KEYS.length; i++) {
      var k = STRUCTURAL_METRIC_KEYS[i];
      if ((ma[k] || 0) !== (mb[k] || 0)) return true;
    }
    return false;
  }

  function nodeStructurallyDiffers(fromNode, toNode, fromAdj, toAdj) {
    if (!sameStringArray(targetsFor(fromAdj, fromNode.id), targetsFor(toAdj, toNode.id))) {
      return true;
    }
    return structuralMetricsDiffer(fromNode, toNode);
  }

  // diffGraphs returns { status: {id->"added"|"changed"|"unchanged"} for
  // nodes IN THE TO GRAPH, removed: [ids] present only in From, added,
  // changed, removedCount }.
  function diffGraphs(fromData, toData) {
    var fromEdges = Array.isArray(fromData.edges) ? fromData.edges : [];
    var toEdges = Array.isArray(toData.edges) ? toData.edges : [];
    var fromAdj = buildAdjacency(fromEdges);
    var toAdj = buildAdjacency(toEdges);
    var fromById = Object.create(null);
    (fromData.nodes || []).forEach(function (n) {
      fromById[n.id] = n;
    });
    var toById = Object.create(null);
    (toData.nodes || []).forEach(function (n) {
      toById[n.id] = n;
    });

    var status = Object.create(null);
    var added = 0,
      changed = 0;
    (toData.nodes || []).forEach(function (n) {
      var prev = fromById[n.id];
      if (!prev) {
        status[n.id] = "added";
        added++;
      } else if (nodeStructurallyDiffers(prev, n, fromAdj, toAdj)) {
        status[n.id] = "changed";
        changed++;
      } else {
        status[n.id] = "unchanged";
      }
    });
    var removed = [];
    (fromData.nodes || []).forEach(function (n) {
      if (!toById[n.id]) removed.push(n.id);
    });
    removed.sort();
    return { status: status, removed: removed, added: added, changed: changed, removedCount: removed.length };
  }

  // renderMap is the map CONTROLLER (the dispatcher's entry for
  // renderer:"map"). It owns the View/Compare mode + ref selection
  // (state.mapView), kicks off the needed /api/graphat builds with a
  // spinner + staleness guard, and hands the resolved graph(s) to drawMap.
  // The working-copy graph is `text` (the already-loaded artifact) — always
  // instant, never re-fetched.
  function renderMap(text) {
    var workingData;
    try {
      workingData = JSON.parse(text);
    } catch (err) {
      return renderRaw(text);
    }
    if (!workingData || !Array.isArray(workingData.nodes)) {
      els.content.innerHTML = '<div class="error-banner">graph.json has no "nodes" array</div>';
      return;
    }
    if (!window.dagre) {
      els.content.innerHTML = '<div class="error-banner">dagre.js not available</div>';
      return;
    }

    var tab = state.activeTab || {};
    var tabName = tab.name || "";
    var isChangesTab = (workingData.meta || {}).source === "changes";

    // (Re)initialize per-tab view state when the active tab changes. The
    // Changes tab opens in Compare mode (From = default base, To = working
    // copy) so it keeps working as before; every other tab opens in View
    // mode on the working copy.
    var mv = state.mapView;
    if (!mv || mv.tabName !== tabName) {
      mv = {
        tabName: tabName,
        mode: isChangesTab ? "compare" : "view",
        viewRef: "",
        fromRef: isChangesTab ? "__default__" : "",
        toRef: "",
      };
      state.mapView = mv;
    }
    mv.workingData = workingData;

    refreshMapView();
  }

  // refreshMapView resolves the graph(s) the current mode+selection needs
  // and (re)paints the map, with a spinner while any /api/graphat build is
  // in flight and a staleness guard so a late build for a superseded
  // selection never paints. It bumps state.mapGen; any async continuation
  // bails unless its captured gen still matches.
  function refreshMapView() {
    var gen = ++state.mapGen;
    var mv = state.mapView;
    var tabName = mv.tabName;

    fetchGitRefs()
      .catch(function () {
        return null; // refs are nice-to-have for the pickers; proceed without
      })
      .then(function (refs) {
        if (gen !== state.mapGen) return;

        // Resolve "__default__" (compare From's initial sentinel) now that
        // refs are known.
        if (mv.fromRef === "__default__") {
          mv.fromRef = refs ? defaultBaseRef(refs) : "";
        }
        // Drop a selected ref that no longer exists (deleted branch, other
        // repo) back to the working copy, so we never ask for a bad ref.
        if (refs) {
          var known = (refs.branches || []).concat(refs.tags || []);
          function prune(ref) {
            return ref && !isWorkingRef(ref) && known.indexOf(ref) < 0 ? "" : ref;
          }
          mv.viewRef = prune(mv.viewRef);
          mv.fromRef = prune(mv.fromRef);
          mv.toRef = prune(mv.toRef);
        }

        if (mv.mode === "compare") {
          refreshCompare(gen, refs);
        } else {
          refreshViewMode(gen, refs);
        }
      });
  }

  function refreshViewMode(gen, refs) {
    var mv = state.mapView;
    var ref = mv.viewRef;
    if (isWorkingRef(ref)) {
      // Instant: the working-copy artifact is already parsed.
      drawMap(mv.workingData, { mode: "view", refs: refs });
      return;
    }
    showMapLoading("Analyzing " + ref + "…");
    graphForRef(mv.tabName, ref, mv.workingData)
      .then(function (data) {
        if (gen !== state.mapGen) return;
        drawMap(data, { mode: "view", refs: refs });
      })
      .catch(function (err) {
        if (gen !== state.mapGen) return;
        showMapError("Couldn’t build the graph at " + ref + ".", err);
      });
  }

  function refreshCompare(gen, refs) {
    var mv = state.mapView;
    var fromRef = mv.fromRef,
      toRef = mv.toRef;
    showMapLoading("Comparing " + refLabel(fromRef) + " → " + refLabel(toRef) + "…");
    Promise.all([
      graphForRef(mv.tabName, fromRef, mv.workingData),
      graphForRef(mv.tabName, toRef, mv.workingData),
    ])
      .then(function (both) {
        if (gen !== state.mapGen) return;
        var fromData = both[0],
          toData = both[1];
        var diff = diffGraphs(fromData, toData);
        drawMap(toData, {
          mode: "compare",
          refs: refs,
          fromRef: fromRef,
          toRef: toRef,
          status: diff.status,
          removed: diff.removed,
          summary: { added: diff.added, changed: diff.changed, removedCount: diff.removedCount },
        });
      })
      .catch(function (err) {
        if (gen !== state.mapGen) return;
        showMapError(
          "Couldn’t compare " + refLabel(fromRef) + " → " + refLabel(toRef) + ".",
          err
        );
      });
  }

  // refLabel renders a ref for UI text: the working tree reads "working
  // copy", everything else is its own name.
  function refLabel(ref) {
    return isWorkingRef(ref) ? "working copy" : ref;
  }

  // showMapLoading paints a centered spinner + message in the content
  // area. It fully replaces content (the rail is rebuilt by the subsequent
  // drawMap) — simple and robust; the selection that triggered it lives in
  // state.mapView, so nothing is lost.
  function showMapLoading(message) {
    var wrap = document.createElement("div");
    wrap.className = "map-loading-full";
    var spin = document.createElement("div");
    spin.className = "map-spinner";
    var msg = document.createElement("div");
    msg.className = "map-loading-msg";
    msg.textContent = message;
    wrap.appendChild(spin);
    wrap.appendChild(msg);
    setContent(wrap);
  }

  // showMapError paints an error state (a ref whose code didn't build, or a
  // failed fetch) plus a button to return to the working copy, so the UI is
  // never left broken. Both strings go through textContent.
  function showMapError(headline, err) {
    var wrap = document.createElement("div");
    wrap.className = "map-error-full";
    var h = document.createElement("div");
    h.className = "map-error-headline";
    h.textContent = headline;
    var detail = document.createElement("div");
    detail.className = "map-error-detail";
    detail.textContent = err && err.message ? err.message : String(err || "");
    var back = document.createElement("button");
    back.type = "button";
    back.className = "btn";
    back.textContent = "Back to working copy";
    back.addEventListener("click", function () {
      var mv = state.mapView;
      mv.mode = "view";
      mv.viewRef = "";
      refreshMapView();
    });
    wrap.appendChild(h);
    wrap.appendChild(detail);
    wrap.appendChild(back);
    setContent(wrap);
  }

  // drawMap renders ONE graph (data) onto the canvas, optionally with a
  // Compare overlay (overlay.status / overlay.removed). It fully rebuilds
  // the map DOM (rail + stage) each call; the mode toggle + ref selects are
  // rebuilt too, restoring their values from state.mapView. overlay:
  //   { mode:"view"|"compare", refs, fromRef?, toRef?, status?, removed?,
  //     summary? }
  function drawMap(data, overlay) {
    overlay = overlay || { mode: "view" };
    var nodes = data.nodes;
    var edges = Array.isArray(data.edges) ? data.edges : [];

    // Compare overlay: per-node diff status (added/changed/unchanged) for
    // nodes present in this (To) graph. null in View mode.
    var diffStatus = overlay.mode === "compare" ? overlay.status || Object.create(null) : null;
    function nodeDiffStatus(n) {
      return diffStatus ? diffStatus[n.id] || "unchanged" : null;
    }
    function nodeIsDiffHot(n) {
      var st = nodeDiffStatus(n);
      return st === "added" || st === "changed";
    }
    // In Compare mode, dim unchanged nodes so the added/changed stand out
    // (same emphasis the old overlay gave). View mode never dims by diff.
    function diffDim(n) {
      return !!diffStatus && !nodeIsDiffHot(n);
    }

    // Honor the meta seam: node size is driven by meta.sizeBy and ring
    // color by meta.colorBy, so a future adapter emitting runtime metrics
    // (reqPerSec/latencyMs/errorRate) reuses this renderer unchanged — only
    // meta changes. Color is NORMALIZED to the graph's current max for the
    // colorBy metric (not absolute churn thresholds), so any metric's range
    // maps onto the calm→hot ramp.
    var meta = data.meta || {};
    var sizeByKey = meta.sizeBy || "dependents";
    // colorBy defaults to the graph's own meta.colorBy ("churn"), but the
    // rail's "Color by" control can override it per-tab via
    // state.mapView.colorBy — e.g. switch the Service map to the coverage
    // overlay. The override is only honored if some node actually carries
    // that metric, so a choice made on one tab can't mis-color another
    // graph that lacks it (every ER/call-graph node would read 0).
    function anyNodeHasMetric(key) {
      for (var i = 0; i < nodes.length; i++) {
        if (nodes[i].metrics && typeof nodes[i].metrics[key] === "number") return true;
      }
      return false;
    }
    var mvColorBy = state.mapView && state.mapView.colorBy;
    var colorByKey =
      mvColorBy && anyNodeHasMetric(mvColorBy) ? mvColorBy : meta.colorBy || "churn";
    // coverage is "good when high", so its ring heat is INVERTED: an
    // untested package (low %) burns hot (red), a well-covered one stays
    // calm. It's also scaled against an ABSOLUTE 0–100 range (not the
    // graph's own max like other metrics) so 50% always reads as mid-heat.
    var COLOR_INVERTED = { coverage: true };
    var isInvertedColor = !!COLOR_INVERTED[colorByKey];

    // Repo ids are attacker-influenced strings (a package literally named
    // "__proto__" is legal); Object.create(null) keeps such a key from
    // colliding with Object.prototype and corrupting lookups.
    var byId = Object.create(null);
    var maxSize = 1; // >=1 so radiusFor never divides by zero
    var maxColor = 0; // 0 => every ring calm (no colorBy signal)
    function metricVal(n, key) {
      return n.metrics && typeof n.metrics[key] === "number" ? n.metrics[key] : 0;
    }
    nodes.forEach(function (n) {
      byId[n.id] = n;
      var s = metricVal(n, sizeByKey);
      if (s > maxSize) maxSize = s;
      var c = metricVal(n, colorByKey);
      if (c > maxColor) maxColor = c;
    });
    function radiusFor(n) {
      return 9 + 17 * (metricVal(n, sizeByKey) / maxSize);
    }
    function colorFrac(n) {
      if (isInvertedColor) {
        // Absolute 0–100 scale, inverted: 0% → 1 (hot), 100% → 0 (calm).
        var v = metricVal(n, colorByKey);
        if (v < 0) v = 0;
        if (v > 100) v = 100;
        return (100 - v) / 100;
      }
      return maxColor > 0 ? metricVal(n, colorByKey) / maxColor : 0;
    }
    function heatColor(n) {
      var f = colorFrac(n);
      return f >= 0.66 ? "var(--map-hot)" : f >= 0.33 ? "var(--map-warm)" : "var(--map-calm)";
    }

    // ---- ER table cards ----
    //
    // A "table" node carrying a Table.Columns list is drawn as an ER card
    // (header + one row per column) instead of a circle. Sizes are computed
    // once up front (cardInfo) so the SAME width/height feed the layout
    // (dagre node box / force collide) and the draw step — otherwise cards
    // would overlap. Everything is built SVG-native (createElementNS +
    // textContent) in draw(); no innerHTML/foreignObject of dynamic content.
    var CARD = {
      headerH: 26, // table-name band
      rowH: 20, // per-column row
      padX: 10,
      gutter: 36, // left zone for PK/FK badges
      gap: 14, // min gap between a column name and its type
      charW: 6.7, // ~width of one mono char at the row font-size
      headCharW: 7.4, // header name is a touch larger
      minW: 150,
      maxW: 380,
      maxRows: 12, // clamp tall tables; the rest go to a "+K more" row + the panel
    };
    function isTableCard(n) {
      return (
        n.kind === "table" &&
        n.table &&
        Array.isArray(n.table.columns) &&
        n.table.columns.length > 0
      );
    }
    // fitText truncates s (with a trailing ellipsis) to roughly maxPx at
    // charW px/char, so a long column name/type can't overflow the card.
    function fitText(s, maxPx, charW) {
      s = String(s == null ? "" : s);
      var maxChars = Math.max(1, Math.floor(maxPx / charW));
      if (s.length <= maxChars) return s;
      if (maxChars <= 1) return "…";
      return s.slice(0, maxChars - 1) + "…";
    }
    function computeCardInfo(n) {
      var cols = n.table.columns;
      var visible = cols.slice(0, CARD.maxRows);
      var hidden = cols.length - visible.length;
      var widest = 0;
      visible.forEach(function (c) {
        var nameW = String(c.name || "").length * CARD.charW;
        var typeW = String(c.type || "").length * CARD.charW;
        var rowW = CARD.gutter + nameW + CARD.gap + typeW;
        if (rowW > widest) widest = rowW;
      });
      var headerW = String(n.label || n.id || "").length * CARD.headCharW + 20;
      var w = Math.max(CARD.minW, headerW, widest + 2 * CARD.padX);
      w = Math.min(CARD.maxW, w);
      var rowsCount = visible.length + (hidden > 0 ? 1 : 0);
      var h = CARD.headerH + rowsCount * CARD.rowH + 6;
      return { w: w, h: h, visible: visible, hidden: hidden };
    }
    // cardInfo caches per-table-node dimensions; non-table nodes aren't keyed.
    var cardInfo = Object.create(null);
    nodes.forEach(function (n) {
      if (isTableCard(n)) cardInfo[n.id] = computeCardInfo(n);
    });
    // nodeExtent is a node's half-width/half-height for layout: a card's box
    // for table nodes, the circle radius (both axes) otherwise.
    function nodeExtent(n) {
      if (isTableCard(n)) {
        var ci = cardInfo[n.id];
        return { rw: ci.w / 2, rh: ci.h / 2 };
      }
      var r = radiusFor(n);
      return { rw: r, rh: r };
    }
    // roundedTopRectPath draws a rect with only the TOP two corners rounded
    // (square bottom) — the ER card's header band, sitting flush on the
    // card body below it.
    function roundedTopRectPath(x, y, w, h, r) {
      return (
        "M" + (x + r) + "," + y +
        " h" + (w - 2 * r) +
        " a" + r + "," + r + " 0 0 1 " + r + "," + r +
        " v" + (h - r) +
        " h" + -w +
        " v" + -(h - r) +
        " a" + r + "," + r + " 0 0 1 " + r + "," + -r +
        " z"
      );
    }
    // drawTableCard appends an ER card (rounded body + kind-colored header +
    // one row per column with PK/FK badges) to grp, centered on pos. Purely
    // SVG nodes built via svgEl/textContent — no HTML strings. Used only for
    // table nodes; every other kind keeps its circle.
    function drawTableCard(grp, n, pos, heat, color) {
      var ci = cardInfo[n.id];
      var w = ci.w,
        h = ci.h;
      var left = pos.x - w / 2,
        top = pos.y - h / 2;

      // Body (rounded, stroke by colorBy heat like a circle's ring).
      grp.appendChild(
        svgEl("rect", {
          class: "map-card-rect",
          x: left,
          y: top,
          width: w,
          height: h,
          rx: 8,
          ry: 8,
          stroke: heat,
        })
      );
      // Header band, filled with the kind color.
      var hdr = svgEl("path", { class: "map-card-hdr", d: roundedTopRectPath(left, top, w, CARD.headerH, 8) });
      hdr.style.fill = color;
      grp.appendChild(hdr);
      var title = svgEl("text", {
        class: "map-card-title",
        x: left + CARD.padX,
        y: top + CARD.headerH / 2,
        "dominant-baseline": "central",
      });
      title.textContent = fitText(n.label || n.id, w - 2 * CARD.padX, CARD.headCharW);
      grp.appendChild(title);

      var rowsTop = top + CARD.headerH;
      ci.visible.forEach(function (c, i) {
        var rowY = rowsTop + i * CARD.rowH;
        var centerY = rowY + CARD.rowH / 2;
        if (i % 2 === 1) {
          grp.appendChild(
            svgEl("rect", { class: "map-col-zebra", x: left + 1, y: rowY, width: w - 2, height: CARD.rowH })
          );
        }
        var gutterX = left + CARD.padX;
        var nameX = left + CARD.padX + CARD.gutter;
        var typeX = left + w - CARD.padX;

        // PK / FK badges in the left gutter.
        var badgeX = gutterX;
        if (c.pk) {
          var pk = svgEl("text", { class: "map-key map-key-pk", x: badgeX, y: centerY, "dominant-baseline": "central" });
          pk.textContent = "PK";
          grp.appendChild(pk);
          badgeX += 18;
        }
        if (c.fk) {
          var fk = svgEl("text", { class: "map-key map-key-fk", x: badgeX, y: centerY, "dominant-baseline": "central" });
          fk.textContent = "FK";
          grp.appendChild(fk);
        }

        // Type on the right (reserve ~45% of the inner width), name fills
        // the rest — both truncated so neither overflows the card.
        var innerW = w - CARD.gutter - 2 * CARD.padX;
        var typeStr = fitText(c.type, innerW * 0.45, CARD.charW);
        var typeW = typeStr.length * CARD.charW;
        var nameStr = fitText(c.name, typeX - nameX - typeW - CARD.gap, CARD.charW);

        var nameEl = svgEl("text", { class: "map-col-name", x: nameX, y: centerY, "dominant-baseline": "central" });
        nameEl.textContent = nameStr;
        grp.appendChild(nameEl);

        var typeEl = svgEl("text", {
          class: "map-col-type",
          x: typeX,
          y: centerY,
          "text-anchor": "end",
          "dominant-baseline": "central",
        });
        typeEl.textContent = typeStr;
        grp.appendChild(typeEl);
      });

      if (ci.hidden > 0) {
        var moreY = rowsTop + ci.visible.length * CARD.rowH + CARD.rowH / 2;
        var more = svgEl("text", {
          class: "map-col-more",
          x: left + CARD.padX,
          y: moreY,
          "dominant-baseline": "central",
        });
        more.textContent = "+" + ci.hidden + " more column" + (ci.hidden === 1 ? "" : "s");
        grp.appendChild(more);
      }
    }

    // ---- DOM scaffold ----
    var root = document.createElement("div");
    root.className = "render-map";

    var rail = document.createElement("aside");
    rail.className = "map-rail";

    var searchWrap = document.createElement("div");
    var searchLabel = document.createElement("h4");
    searchLabel.textContent = "Filter";
    var search = document.createElement("input");
    search.className = "map-search";
    search.type = "text";
    search.placeholder = "Filter packages…";
    searchWrap.appendChild(searchLabel);
    searchWrap.appendChild(search);
    rail.appendChild(searchWrap);

    // ---- View / Compare across git refs ----
    //
    // Built from state.mapView (restores the current mode + ref selections
    // after drawMap rebuilds the rail) and overlay.refs (the branch/tag
    // lists). Each control mutates state.mapView and calls refreshMapView(),
    // which re-resolves the needed graph(s) and repaints.
    var mv = state.mapView;
    var refs = overlay.refs || state.gitRefs || { branches: [], tags: [] };

    // addRefOptions appends a "working copy" option + Branches/Tags
    // optgroups to a <select>, then selects `current`. All option text via
    // textContent.
    function addRefOptions(sel, current) {
      var wc = document.createElement("option");
      wc.value = "";
      wc.textContent = "working copy";
      sel.appendChild(wc);
      function group(label, names) {
        if (!names || names.length === 0) return;
        var g = document.createElement("optgroup");
        g.label = label;
        names.forEach(function (name) {
          var o = document.createElement("option");
          o.value = name;
          o.textContent = name;
          g.appendChild(o);
        });
        sel.appendChild(g);
      }
      group("Branches", refs.branches);
      group("Tags", refs.tags);
      sel.value = isWorkingRef(current) ? "" : current;
    }

    var gitWrap = document.createElement("div");
    var gitLabel = document.createElement("h4");
    gitLabel.textContent = "Git";
    gitWrap.appendChild(gitLabel);

    // Mode toggle: View | Compare.
    var modeToggle = document.createElement("div");
    modeToggle.className = "map-seg";
    var btnView = document.createElement("button");
    btnView.type = "button";
    btnView.textContent = "View";
    btnView.classList.toggle("on", mv.mode === "view");
    var btnCompare = document.createElement("button");
    btnCompare.type = "button";
    btnCompare.textContent = "Compare";
    btnCompare.classList.toggle("on", mv.mode === "compare");
    btnView.addEventListener("click", function () {
      if (mv.mode === "view") return;
      mv.mode = "view";
      refreshMapView();
    });
    btnCompare.addEventListener("click", function () {
      if (mv.mode === "compare") return;
      mv.mode = "compare";
      // First time into Compare on this tab with no From yet: default it.
      if (mv.fromRef === "" && mv.toRef === "") {
        mv.fromRef = defaultBaseRef(refs);
      }
      refreshMapView();
    });
    modeToggle.appendChild(btnView);
    modeToggle.appendChild(btnCompare);
    gitWrap.appendChild(modeToggle);

    if (mv.mode === "view") {
      var showLabel = document.createElement("label");
      showLabel.className = "map-sublabel";
      showLabel.textContent = "Showing";
      var viewSelect = document.createElement("select");
      viewSelect.className = "map-select";
      addRefOptions(viewSelect, mv.viewRef);
      viewSelect.addEventListener("change", function () {
        mv.viewRef = viewSelect.value;
        refreshMapView();
      });
      gitWrap.appendChild(showLabel);
      gitWrap.appendChild(viewSelect);
    } else {
      var fromLabel = document.createElement("label");
      fromLabel.className = "map-sublabel";
      fromLabel.textContent = "From";
      var fromSelect = document.createElement("select");
      fromSelect.className = "map-select";
      addRefOptions(fromSelect, mv.fromRef);
      fromSelect.addEventListener("change", function () {
        mv.fromRef = fromSelect.value;
        refreshMapView();
      });
      var toLabel = document.createElement("label");
      toLabel.className = "map-sublabel";
      toLabel.textContent = "To";
      var toSelect = document.createElement("select");
      toSelect.className = "map-select";
      addRefOptions(toSelect, mv.toRef);
      toSelect.addEventListener("change", function () {
        mv.toRef = toSelect.value;
        refreshMapView();
      });
      gitWrap.appendChild(fromLabel);
      gitWrap.appendChild(fromSelect);
      gitWrap.appendChild(toLabel);
      gitWrap.appendChild(toSelect);

      // Summary + diff legend + removed list.
      var compareSummary = document.createElement("div");
      compareSummary.className = "map-compare-summary";
      var sm = overlay.summary || { added: 0, changed: 0, removedCount: 0 };
      compareSummary.textContent =
        "From " + refLabel(mv.fromRef) + " → To " + refLabel(mv.toRef) + ": " +
        sm.added + " added · " + sm.removedCount + " removed · " + sm.changed + " changed";
      gitWrap.appendChild(compareSummary);

      var diffLegend = document.createElement("div");
      diffLegend.className = "map-diff-legend";
      function diffLegendRow(cls, label) {
        var row = document.createElement("div");
        row.className = "map-diff-legend-row";
        var sw = document.createElement("span");
        sw.className = "sw " + cls;
        var lbl = document.createElement("span");
        lbl.textContent = label;
        row.appendChild(sw);
        row.appendChild(lbl);
        return row;
      }
      diffLegend.appendChild(diffLegendRow("added", "Added in To"));
      diffLegend.appendChild(diffLegendRow("changed", "Changed"));
      gitWrap.appendChild(diffLegend);

      var removed = Array.isArray(overlay.removed) ? overlay.removed : [];
      if (removed.length > 0) {
        var remLabel = document.createElement("label");
        remLabel.className = "map-sublabel";
        remLabel.textContent = "Removed (" + removed.length + ")";
        gitWrap.appendChild(remLabel);
        var remList = document.createElement("ul");
        remList.className = "map-removed-list";
        removed.forEach(function (id) {
          var li = document.createElement("li");
          li.textContent = id;
          remList.appendChild(li);
        });
        gitWrap.appendChild(remList);
      }
    }
    rail.appendChild(gitWrap);

    var kindCounts = Object.create(null);
    nodes.forEach(function (n) {
      kindCounts[n.kind] = (kindCounts[n.kind] || 0) + 1;
    });
    var activeKinds = Object.create(null);
    var kindWrap = document.createElement("div");
    var kindLabel = document.createElement("h4");
    kindLabel.textContent = "Kind";
    kindWrap.appendChild(kindLabel);
    var chips = document.createElement("div");
    chips.className = "map-chips";
    MAP_KIND_ORDER.filter(function (k) {
      return kindCounts[k] > 0;
    }).forEach(function (k) {
      activeKinds[k] = true;
      var chip = document.createElement("div");
      chip.className = "map-chip on";
      chip.dataset.kind = k;
      var sw = document.createElement("span");
      sw.className = "sw";
      sw.style.background = MAP_KIND_VAR[k];
      var label = document.createElement("span");
      label.textContent = MAP_KIND_NAME[k] || k;
      var ct = document.createElement("span");
      ct.className = "ct";
      ct.textContent = String(kindCounts[k]);
      chip.appendChild(sw);
      chip.appendChild(label);
      chip.appendChild(ct);
      chip.addEventListener("click", function () {
        activeKinds[k] = !activeKinds[k];
        chip.classList.toggle("on", activeKinds[k]);
        applyFilters();
      });
      chips.appendChild(chip);
    });
    kindWrap.appendChild(chips);
    rail.appendChild(kindWrap);

    var sizeWrap = document.createElement("div");
    var sizeLabel = document.createElement("h4");
    sizeLabel.textContent = "Node size — " + mapMetricLabel(sizeByKey);
    var sizes = document.createElement("div");
    sizes.className = "map-legend";
    var sizesRow = document.createElement("div");
    sizesRow.className = "map-sizes";
    [12, 20, 30].forEach(function (px) {
      var i = document.createElement("i");
      i.style.width = px + "px";
      i.style.height = px + "px";
      sizesRow.appendChild(i);
    });
    var sizesNote = document.createElement("span");
    sizesNote.style.fontSize = "11px";
    sizesNote.style.color = "var(--text-faint)";
    sizesNote.style.fontFamily = "var(--mono)";
    sizesNote.textContent = sizeByKey;
    sizesRow.appendChild(sizesNote);
    sizes.appendChild(sizesRow);
    sizeWrap.appendChild(sizeLabel);
    sizeWrap.appendChild(sizes);
    rail.appendChild(sizeWrap);

    var heatWrap = document.createElement("div");
    var heatLabel = document.createElement("h4");
    heatLabel.textContent = "Ring color — " + mapMetricLabel(colorByKey);
    heatWrap.appendChild(heatLabel);

    // "Color by" picker: offered only when the graph actually carries more
    // than one color-able metric (so the Service map gains a churn↔coverage
    // toggle, while ER/call-graph maps keep their single static legend).
    // Candidates are the graph's default colorBy plus coverage when present.
    var colorOptions = [];
    [meta.colorBy || "churn", "coverage"].forEach(function (k) {
      if (anyNodeHasMetric(k) && colorOptions.indexOf(k) < 0) colorOptions.push(k);
    });
    if (colorOptions.length > 1) {
      var colorSelect = document.createElement("select");
      colorSelect.className = "map-select";
      colorOptions.forEach(function (k) {
        var o = document.createElement("option");
        o.value = k;
        o.textContent = mapMetricLabel(k);
        colorSelect.appendChild(o);
      });
      colorSelect.value = colorByKey;
      colorSelect.addEventListener("change", function () {
        // Default colorBy stays implicit (undefined) so a tab switch resets
        // naturally; only a non-default choice is stored.
        state.mapView.colorBy =
          colorSelect.value === (meta.colorBy || "churn") ? undefined : colorSelect.value;
        refreshMapView();
      });
      heatWrap.appendChild(colorSelect);
    }

    var heatLegend = document.createElement("div");
    heatLegend.className = "map-legend";
    var heatbar = document.createElement("div");
    heatbar.className = "map-heatbar";
    var heatLabels = document.createElement("div");
    heatLabels.className = "map-heat-labels";
    var calmLabel = document.createElement("span");
    var hotLabel = document.createElement("span");
    // For coverage the ramp is inverted and absolute: calm = fully tested,
    // hot = untested. Other metrics read calm→hot over the graph's range.
    calmLabel.textContent = isInvertedColor ? "100% tested" : "calm";
    hotLabel.textContent = isInvertedColor ? "untested" : "hot";
    heatLabels.appendChild(calmLabel);
    heatLabels.appendChild(hotLabel);
    heatLegend.appendChild(heatbar);
    heatLegend.appendChild(heatLabels);
    heatWrap.appendChild(heatLegend);
    rail.appendChild(heatWrap);

    var layoutWrap = document.createElement("div");
    var layoutLabel = document.createElement("h4");
    layoutLabel.textContent = "Layout";
    var dirSeg = document.createElement("div");
    dirSeg.className = "map-seg";
    var btnLR = document.createElement("button");
    btnLR.type = "button";
    btnLR.textContent = "L → R";
    btnLR.className = "on";
    var btnTB = document.createElement("button");
    btnTB.type = "button";
    btnTB.textContent = "Top ↓";
    dirSeg.appendChild(btnLR);
    dirSeg.appendChild(btnTB);
    var modeSeg = document.createElement("div");
    modeSeg.className = "map-seg";
    modeSeg.style.marginTop = "6px";
    var btnHier = document.createElement("button");
    btnHier.type = "button";
    btnHier.textContent = "Hierarchical";
    var btnForce = document.createElement("button");
    btnForce.type = "button";
    btnForce.textContent = "Force";
    if (!window.d3 || typeof window.d3.forceSimulation !== "function") {
      // d3-force failed to load — keep hierarchical only rather than a dead toggle.
      btnForce.disabled = true;
      btnForce.title = "d3-force not available";
    }
    modeSeg.appendChild(btnHier);
    modeSeg.appendChild(btnForce);
    layoutWrap.appendChild(layoutLabel);
    layoutWrap.appendChild(dirSeg);
    layoutWrap.appendChild(modeSeg);
    rail.appendChild(layoutWrap);

    var stage = document.createElement("div");
    stage.className = "map-stage";
    var svg = svgEl("svg", { preserveAspectRatio: "xMidYMid meet" });
    var gEdges = svgEl("g", {});
    var gNodes = svgEl("g", {});
    svg.appendChild(gEdges);
    svg.appendChild(gNodes);
    var card = document.createElement("div");
    card.className = "map-card";
    var hint = document.createElement("div");
    hint.className = "map-hint";
    hint.textContent =
      "Hover for metrics · click a node for its detail panel · drag to pan · scroll to zoom";
    var panel = document.createElement("aside");
    panel.className = "map-panel";
    var panelInner = document.createElement("div");
    panelInner.className = "map-panel-inner";
    panel.appendChild(panelInner);

    var zoomRail = document.createElement("div");
    zoomRail.className = "map-zoom";
    function zoomBtn(label, title) {
      var b = document.createElement("button");
      b.type = "button";
      b.textContent = label;
      b.title = title;
      return b;
    }
    var zoomInBtn = zoomBtn("+", "Zoom in");
    var zoomOutBtn = zoomBtn("−", "Zoom out");
    var zoomFitBtn = zoomBtn("⤢", "Fit to view");
    zoomRail.appendChild(zoomInBtn);
    zoomRail.appendChild(zoomOutBtn);
    zoomRail.appendChild(zoomFitBtn);

    stage.appendChild(svg);
    stage.appendChild(card);
    stage.appendChild(hint);
    stage.appendChild(zoomRail);
    stage.appendChild(panel);

    // Compare-mode banner: the From → To summary, floated on the canvas so
    // it's visible without scanning the rail. View mode draws no banner.
    if (overlay.mode === "compare") {
      var banner = document.createElement("div");
      banner.className = "map-changes-banner has-changes";
      var sm2 = overlay.summary || { added: 0, changed: 0, removedCount: 0 };
      banner.textContent =
        "From " + refLabel(overlay.fromRef) + " → To " + refLabel(overlay.toRef) + ": " +
        sm2.added + " added · " + sm2.removedCount + " removed · " + sm2.changed + " changed";
      stage.appendChild(banner);
    }

    root.appendChild(rail);
    root.appendChild(stage);
    setContent(root);

    // ---- layout + draw ----
    var nodeEls = Object.create(null);
    var edgeEls = [];
    var focused = null;
    var vb = { x: 0, y: 0, w: 1000, h: 640 };
    var baseW = 1000,
      baseH = 640; // last layout's extent, for zoom clamping
    // Start in the layout meta asks for (defaults to hierarchical). Force
    // is only honored when d3-force actually loaded.
    var forceAvailable = !!(window.d3 && typeof window.d3.forceSimulation === "function");
    var layoutMode = meta.defaultLayout === "force" && forceAvailable ? "force" : "hierarchical";
    var curDir = "LR";
    var dragMoved = false; // set by a real pan; read by the svg background-click handler

    // hierarchicalLayout runs dagre and returns a renderer-neutral result:
    // { width, height, pos: {id:{x,y}}, links: [{a,b,points:[…]}] }.
    function hierarchicalLayout(rankdir) {
      var g = new window.dagre.graphlib.Graph();
      g.setGraph({ rankdir: rankdir, nodesep: 24, ranksep: 68, marginx: 36, marginy: 36 });
      g.setDefaultEdgeLabel(function () {
        return {};
      });
      nodes.forEach(function (n) {
        if (isTableCard(n)) {
          var ci = cardInfo[n.id];
          g.setNode(n.id, { width: ci.w + 12, height: ci.h + 12 });
        } else {
          var r = radiusFor(n);
          g.setNode(n.id, { width: r * 2 + 12, height: r * 2 + 12 });
        }
      });
      edges.forEach(function (e) {
        if (byId[e.from] && byId[e.to]) g.setEdge(e.from, e.to);
      });
      window.dagre.layout(g);
      var gg = g.graph();
      var pos = Object.create(null);
      g.nodes().forEach(function (id) {
        var p = g.node(id);
        if (p) pos[id] = { x: p.x, y: p.y };
      });
      var links = g.edges().map(function (e) {
        return { a: e.v, b: e.w, points: g.edge(e).points };
      });
      return { width: gg.width || 1, height: gg.height || 1, pos: pos, links: links };
    }

    // forceLayout runs a d3-force simulation SYNCHRONOUSLY for a fixed tick
    // budget then reads final positions — no continuous animation, no timer
    // left running. Edges become straight lines between node centers. Same
    // neutral result shape as hierarchicalLayout.
    function forceLayout() {
      var d3 = window.d3;
      var simNodes = nodes.map(function (n) {
        var e = nodeExtent(n);
        // r (collide radius) is the box's half-diagonal so cards don't
        // overlap; rw/rh drive the viewBox extent so a wide/tall card near
        // the edge isn't clipped.
        return { id: n.id, rw: e.rw, rh: e.rh, r: Math.sqrt(e.rw * e.rw + e.rh * e.rh) };
      });
      var idx = Object.create(null);
      simNodes.forEach(function (sn) {
        idx[sn.id] = sn;
      });
      var simLinks = [];
      edges.forEach(function (e) {
        if (idx[e.from] && idx[e.to]) simLinks.push({ source: e.from, target: e.to });
      });
      var sim = d3
        .forceSimulation(simNodes)
        .force(
          "link",
          d3
            .forceLink(simLinks)
            .id(function (d) {
              return d.id;
            })
            .distance(96)
            .strength(0.35)
        )
        .force("charge", d3.forceManyBody().strength(-280))
        .force("center", d3.forceCenter(0, 0))
        .force(
          "collide",
          d3.forceCollide().radius(function (d) {
            return d.r + 10;
          })
        )
        .stop();
      for (var i = 0; i < 300; i++) sim.tick();

      // Translate so the whole graph sits at a positive margin, and derive
      // the viewBox extent.
      var margin = 48;
      var minX = Infinity,
        minY = Infinity,
        maxX = -Infinity,
        maxY = -Infinity;
      simNodes.forEach(function (sn) {
        minX = Math.min(minX, sn.x - sn.rw);
        minY = Math.min(minY, sn.y - sn.rh);
        maxX = Math.max(maxX, sn.x + sn.rw);
        maxY = Math.max(maxY, sn.y + sn.rh);
      });
      if (!isFinite(minX)) {
        minX = minY = 0;
        maxX = maxY = 1;
      }
      var pos = Object.create(null);
      simNodes.forEach(function (sn) {
        pos[sn.id] = { x: sn.x - minX + margin, y: sn.y - minY + margin };
      });
      var links = simLinks.map(function (l) {
        var a = typeof l.source === "object" ? l.source.id : l.source;
        var b = typeof l.target === "object" ? l.target.id : l.target;
        return { a: a, b: b, points: [pos[a], pos[b]] };
      });
      return {
        width: maxX - minX + 2 * margin,
        height: maxY - minY + 2 * margin,
        pos: pos,
        links: links,
      };
    }

    function computeLayout() {
      if (layoutMode === "force" && forceAvailable) return forceLayout();
      return hierarchicalLayout(curDir);
    }

    function draw() {
      gEdges.innerHTML = "";
      gNodes.innerHTML = "";
      nodeEls = Object.create(null);
      edgeEls = [];

      var layout = computeLayout();
      baseW = layout.width;
      baseH = layout.height;
      vb = { x: 0, y: 0, w: layout.width, h: layout.height };
      setViewBox();

      layout.links.forEach(function (lk) {
        var p = svgEl("path", { d: mapEdgePath(lk.points), class: "map-edge" });
        gEdges.appendChild(p);
        edgeEls.push({ el: p, a: lk.a, b: lk.b });
      });

      nodes.forEach(function (n) {
        var id = n.id;
        var pos = layout.pos[id];
        if (!pos) return;
        var r = radiusFor(n);
        var color = MAP_KIND_VAR[n.kind] || "var(--text-faint)";
        var heat = heatColor(n);

        var grp = svgEl("g", { class: "map-node", "data-id": id, "data-kind": n.kind });
        var st = nodeDiffStatus(n);
        if (st === "added") grp.classList.add("map-diff-added");
        else if (st === "changed") grp.classList.add("map-diff-changed");
        grp.style.color = heat;

        if (isTableCard(n)) {
          drawTableCard(grp, n, pos, heat, color);
        } else {
          grp.appendChild(svgEl("circle", { class: "ring", cx: pos.x, cy: pos.y, r: r, stroke: heat }));

          var gl = svgEl("text", {
            class: "glyph",
            x: pos.x,
            y: pos.y + 0.5,
            "text-anchor": "middle",
            "dominant-baseline": "central",
            fill: color,
          });
          gl.textContent = MAP_KIND_GLYPH[n.kind] || "●";
          grp.appendChild(gl);

          var lb = svgEl("text", { class: "label", x: pos.x + r + 6, y: pos.y - 3, "text-anchor": "start" });
          lb.textContent = n.label || n.id;
          grp.appendChild(lb);

          var sb = svgEl("text", { class: "sub", x: pos.x + r + 6, y: pos.y + 8, "text-anchor": "start" });
          // First two non-zero metrics for THIS node's kind (e.g. "cols 8 ·
          // fk← 1" for a table, "in 3 · out 2" for a func), not a hardcoded
          // dependents/imports pair that reads "dep 0 · imp 0" on either.
          sb.textContent = nodeMetricEntries(n)
            .slice(0, 2)
            .map(function (e) {
              return MAP_METRIC_ABBR[e[0]] + " " + e[1];
            })
            .join(" · ");
          grp.appendChild(sb);
        }

        gNodes.appendChild(grp);
        nodeEls[id] = grp;

        grp.addEventListener("mouseenter", function () {
          showCard(n);
          highlightNeighbours(id);
        });
        grp.addEventListener("mousemove", moveCard);
        grp.addEventListener("mouseleave", function () {
          hideCard();
          // Returning to the focused node's view, not a blank slate: if a
          // node is focused, re-assert its neighbourhood highlight;
          // otherwise fall back to the filter state.
          if (focused) highlightNeighbours(focused);
          else clearHighlight();
        });
        grp.addEventListener("click", function (ev) {
          ev.stopPropagation();
          if (focused === id) {
            focused = null;
            clearHighlight();
            closePanel();
          } else {
            focused = id;
            highlightNeighbours(id);
            openPanel(n);
          }
        });
      });

      applyFilters();
    }

    svg.addEventListener("click", function () {
      // A pan ends with a background click; don't let it dismiss the panel.
      if (dragMoved) {
        dragMoved = false;
        return;
      }
      focused = null;
      clearHighlight();
      closePanel();
    });

    function neighbours(id) {
      var s = Object.create(null);
      s[id] = true;
      edges.forEach(function (e) {
        if (e.from === id) s[e.to] = true;
        if (e.to === id) s[e.from] = true;
      });
      return s;
    }
    // nodeVisible reports whether a node passes the current kind-chip +
    // search filters. Highlighting respects this so focusing a node never
    // un-dims something the user has filtered out.
    function nodeVisible(n, q) {
      return activeKinds[n.kind] && (!q || n.id.toLowerCase().indexOf(q) >= 0);
    }
    function currentQuery() {
      return search.value.toLowerCase().trim();
    }
    function highlightNeighbours(id) {
      var nb = neighbours(id);
      var q = currentQuery();
      nodes.forEach(function (n) {
        var g = nodeEls[n.id];
        if (!g) return;
        // Lit only if it's a neighbour AND passes the filters; a
        // filtered-out neighbour stays dim.
        var lit = !!nb[n.id] && nodeVisible(n, q);
        g.classList.toggle("map-lit", lit);
        g.classList.toggle("map-dim", !lit);
      });
      edgeEls.forEach(function (x) {
        var na = byId[x.a],
          nb2 = byId[x.b];
        var touches = x.a === id || x.b === id;
        var on = touches && na && nb2 && nodeVisible(na, q) && nodeVisible(nb2, q);
        x.el.classList.toggle("map-dim", !on);
        x.el.classList.toggle("flow", on);
      });
    }
    function clearHighlight() {
      nodes.forEach(function (n) {
        var g = nodeEls[n.id];
        if (g) g.classList.remove("map-dim", "map-lit");
      });
      edgeEls.forEach(function (x) {
        x.el.classList.remove("map-dim", "flow");
      });
      applyFilters();
    }

    // applyFilters dims nodes/edges that fail the kind-chip + search
    // filters. It stays live even while a node is focused: when focused, it
    // re-asserts the neighbourhood highlight (which itself respects the
    // filters) so chips/search keep working during focus.
    function applyFilters() {
      if (focused) {
        highlightNeighbours(focused);
        return;
      }
      var q = currentQuery();
      nodes.forEach(function (n) {
        var g = nodeEls[n.id];
        if (!g) return;
        g.classList.toggle("map-dim", !nodeVisible(n, q) || diffDim(n));
      });
      edgeEls.forEach(function (x) {
        var na = byId[x.a],
          nbNode = byId[x.b];
        var visEdge = na && nbNode && nodeVisible(na, q) && nodeVisible(nbNode, q);
        // In Compare mode an edge stays lit only if it touches an
        // added/changed node, so the diff reads clearly; View mode never
        // dims by diff.
        var edgeDiffDim =
          !!diffStatus && !((na && nodeIsDiffHot(na)) || (nbNode && nodeIsDiffHot(nbNode)));
        x.el.classList.toggle("map-dim", !visEdge || edgeDiffDim);
      });
    }
    search.addEventListener("input", applyFilters);

    // ---- hover metric card ----
    function showCard(n) {
      card.innerHTML = "";
      var hd = document.createElement("div");
      hd.className = "hd";
      var dot = document.createElement("span");
      dot.className = "tdot";
      dot.style.background = MAP_KIND_VAR[n.kind] || "var(--text-faint)";
      var b = document.createElement("b");
      b.textContent = n.id;
      var em = document.createElement("em");
      em.textContent = MAP_KIND_NAME[n.kind] || n.kind;
      hd.appendChild(dot);
      hd.appendChild(b);
      hd.appendChild(em);

      var metrics = document.createElement("div");
      metrics.className = "metrics";
      // Up to three non-zero metrics for this node's kind (the card's grid
      // is three columns wide). A node with no non-zero metric gets a single
      // muted placeholder rather than three zeros.
      var cardEntries = nodeMetricEntries(n).slice(0, 3);
      if (cardEntries.length === 0) {
        var emptyCell = document.createElement("div");
        var ek = document.createElement("div");
        ek.className = "k";
        ek.textContent = "Metrics";
        var ev = document.createElement("div");
        ev.className = "v";
        ev.textContent = "—";
        emptyCell.appendChild(ek);
        emptyCell.appendChild(ev);
        metrics.appendChild(emptyCell);
      } else {
        cardEntries.forEach(function (pair) {
          var cell = document.createElement("div");
          var k = document.createElement("div");
          k.className = "k";
          k.textContent = MAP_METRIC_CELL[pair[0]] || pair[0];
          var v = document.createElement("div");
          v.className = "v";
          v.textContent = String(pair[1]);
          cell.appendChild(k);
          cell.appendChild(v);
          metrics.appendChild(cell);
        });
      }

      var foot = document.createElement("div");
      foot.className = "foot";
      var hc = document.createElement("span");
      hc.className = "hc";
      hc.style.background = heatColor(n);
      var frac = colorFrac(n);
      var footText = document.createElement("span");
      // Described relative to the graph's own max for the colorBy metric.
      var level = frac >= 0.66 ? "High" : frac >= 0.33 ? "Moderate" : "Low";
      footText.textContent = level + " " + mapMetricLabel(colorByKey);
      foot.appendChild(hc);
      foot.appendChild(footText);

      card.appendChild(hd);
      card.appendChild(metrics);
      card.appendChild(foot);
      card.classList.add("show");
    }
    function moveCard(ev) {
      var b = stage.getBoundingClientRect();
      var x = ev.clientX - b.left + 16,
        y = ev.clientY - b.top + 14;
      if (x + 220 > b.width) x = ev.clientX - b.left - 220;
      if (y + 140 > b.height) y = b.height - 146;
      card.style.left = x + "px";
      card.style.top = y + "px";
    }
    function hideCard() {
      card.classList.remove("show");
    }

    // ---- detail side panel ----
    function metricCell(label, value) {
      var cell = document.createElement("div");
      cell.className = "cell";
      var k = document.createElement("div");
      k.className = "k";
      k.textContent = label;
      var v = document.createElement("div");
      v.className = "v";
      v.textContent = String(value);
      cell.appendChild(k);
      cell.appendChild(v);
      return cell;
    }

    function openPanel(n) {
      panelInner.innerHTML = "";
      var loc = n.location || {};
      var desc = n.description || { text: "", source: "none" };

      var ph = document.createElement("div");
      ph.className = "map-ph";
      var dot = document.createElement("span");
      dot.className = "tdot";
      dot.style.background = MAP_KIND_VAR[n.kind] || "var(--text-faint)";
      var tt = document.createElement("div");
      tt.className = "tt";
      var ttB = document.createElement("b");
      ttB.textContent = n.id;
      var ttSpan = document.createElement("span");
      ttSpan.textContent = MAP_KIND_NAME[n.kind] || n.kind;
      tt.appendChild(ttB);
      tt.appendChild(ttSpan);
      var closeBtn = document.createElement("button");
      closeBtn.type = "button";
      closeBtn.className = "close-btn";
      closeBtn.title = "Close";
      closeBtn.textContent = "×";
      closeBtn.addEventListener("click", function (ev) {
        ev.stopPropagation();
        focused = null;
        clearHighlight();
        closePanel();
      });
      ph.appendChild(dot);
      ph.appendChild(tt);
      ph.appendChild(closeBtn);

      var pb = document.createElement("div");
      pb.className = "map-pb";

      // Address
      var addrSec = document.createElement("div");
      addrSec.className = "map-psec";
      var addrH = document.createElement("h5");
      addrH.textContent = "Address";
      var addr = document.createElement("div");
      addr.className = "map-addr";
      var addrPath = document.createTextNode(loc.path || n.id);
      addr.appendChild(addrPath);
      if (loc.primaryFile) {
        addr.appendChild(document.createElement("br"));
        var pf = document.createElement("small");
        pf.textContent = loc.primaryFile + (loc.files ? " · " + loc.files + " files" : "");
        addr.appendChild(pf);
      } else if (loc.files) {
        addr.appendChild(document.createElement("br"));
        var fc = document.createElement("small");
        fc.textContent = loc.files + " files";
        addr.appendChild(fc);
      }
      addrSec.appendChild(addrH);
      addrSec.appendChild(addr);
      pb.appendChild(addrSec);

      // Description
      var descSec = document.createElement("div");
      descSec.className = "map-psec";
      var descH = document.createElement("h5");
      descH.textContent = "Description";
      descSec.appendChild(descH);
      if (desc.source && desc.source !== "none" && desc.text) {
        var tag = document.createElement("span");
        tag.className = "map-tag";
        tag.textContent = desc.source === "observer-annotation" ? "from observer" : "from " + desc.source;
        var body = document.createElement("div");
        body.className = "map-desc";
        body.textContent = desc.text;
        descSec.appendChild(tag);
        descSec.appendChild(body);
      } else {
        var emptyDoc = document.createElement("div");
        emptyDoc.className = "map-empty-doc";
        emptyDoc.textContent =
          "No description in the code yet. Observer can ask your AI assistant to write a short, structured summary and store it beside the code — so this panel (and search) understand what this package does.";
        var genBtn = document.createElement("button");
        genBtn.type = "button";
        genBtn.className = "map-gen-btn";
        genBtn.textContent = "✦ Generate with Observer AI";
        genBtn.title = "Coming soon — the //observer:summary annotation + AI generator skill is a fast follow (design spec §3, non-goals).";
        genBtn.disabled = true;
        descSec.appendChild(emptyDoc);
        descSec.appendChild(genBtn);
      }
      pb.appendChild(descSec);

      // Metrics
      var metSec = document.createElement("div");
      metSec.className = "map-psec";
      var metH = document.createElement("h5");
      metH.textContent = "Metrics";
      var mrow = document.createElement("div");
      mrow.className = "map-mrow";
      // Every non-zero metric this node's kind carries — go-deps shows
      // Dependents/Imports/Churn, a table shows Columns/FK in/FK out, a
      // func shows Callers/Callees — instead of a hardcoded three.
      var panelEntries = nodeMetricEntries(n);
      if (panelEntries.length === 0) {
        mrow.appendChild(metricCell("Metrics", "—"));
      } else {
        panelEntries.forEach(function (e) {
          mrow.appendChild(metricCell(MAP_METRIC_CELL[e[0]] || e[0], e[1]));
        });
      }
      metSec.appendChild(metH);
      metSec.appendChild(mrow);
      pb.appendChild(metSec);

      // Columns (only for "table" nodes — the full list, including any the
      // card clamped away with its "+K more" row).
      if (isTableCard(n)) {
        var colSec = document.createElement("div");
        colSec.className = "map-psec";
        var cols = n.table.columns;
        var colH = document.createElement("h5");
        colH.textContent = "Columns (" + cols.length + ")";
        colSec.appendChild(colH);
        var colList = document.createElement("ul");
        colList.className = "map-col-list";
        cols.forEach(function (c) {
          var li = document.createElement("li");
          var nm = document.createElement("span");
          nm.className = "cn";
          nm.textContent = c.name;
          li.appendChild(nm);
          if (c.pk) {
            var pkB = document.createElement("span");
            pkB.className = "ck ck-pk";
            pkB.textContent = "PK";
            li.appendChild(pkB);
          }
          if (c.fk) {
            var fkB = document.createElement("span");
            fkB.className = "ck ck-fk";
            fkB.textContent = "FK";
            li.appendChild(fkB);
          }
          var ty = document.createElement("span");
          ty.className = "ct";
          ty.textContent = c.type;
          li.appendChild(ty);
          colList.appendChild(li);
        });
        colSec.appendChild(colList);
        pb.appendChild(colSec);
      }

      // Compare status: in Compare mode, note whether this node is
      // added/changed/unchanged vs the From ref.
      if (diffStatus) {
        var cmpSec = document.createElement("div");
        cmpSec.className = "map-psec";
        var cmpH = document.createElement("h5");
        cmpH.textContent = "Compare " + refLabel(overlay.fromRef) + " → " + refLabel(overlay.toRef);
        cmpSec.appendChild(cmpH);
        var st = nodeDiffStatus(n);
        var badge = document.createElement("span");
        badge.className = "map-tag map-diff-badge-" + st;
        badge.textContent = st === "added" ? "Added" : st === "changed" ? "Changed" : "Unchanged";
        cmpSec.appendChild(badge);
        var cmpNote = document.createElement("div");
        cmpNote.className = "map-empty-doc";
        cmpNote.textContent =
          st === "added"
            ? "Present in To, not in From."
            : st === "changed"
            ? "Its outgoing calls or structural metrics differ between the two refs."
            : "Structurally identical between the two refs.";
        cmpSec.appendChild(cmpNote);
        pb.appendChild(cmpSec);
      }

      // Linked work
      var linkSec = document.createElement("div");
      linkSec.className = "map-psec map-links";
      var linkH = document.createElement("h5");
      linkH.textContent = "Linked work";
      linkSec.appendChild(linkH);
      var links = Array.isArray(n.links) ? n.links : [];
      if (links.length === 0) {
        var noLinks = document.createElement("div");
        noLinks.className = "map-empty-doc";
        noLinks.textContent = "No linked work yet.";
        linkSec.appendChild(noLinks);
      } else {
        links.forEach(function (l) {
          var lk = document.createElement("div");
          lk.className = "lk";
          var ic = document.createElement("span");
          ic.className = "ic";
          ic.textContent = l.type === "jira" ? "JR" : "SP";
          var label = document.createElement("span");
          label.textContent = (l.type === "jira" ? "Jira issue " : "ServicePlus task ") + (l.id || "");
          var em = document.createElement("em");
          em.textContent = "connect later";
          lk.appendChild(ic);
          lk.appendChild(label);
          lk.appendChild(em);
          linkSec.appendChild(lk);
        });
      }
      pb.appendChild(linkSec);

      panelInner.appendChild(ph);
      panelInner.appendChild(pb);
      panel.classList.add("open");
    }
    function closePanel() {
      panel.classList.remove("open");
    }

    // ---- pan / zoom-to-cursor ----
    function setViewBox() {
      svg.setAttribute("viewBox", vb.x + " " + vb.y + " " + vb.w + " " + vb.h);
    }

    // zoomBy scales the viewBox about its own center (not the cursor — that's
    // the wheel handler's job) by factor, clamped to the same [0.15, 6] *
    // baseW/baseH range the wheel handler uses so the +/- buttons can never
    // collapse or blow out the view. zoomFit resets to the last layout's
    // full extent.
    function zoomBy(factor) {
      var cx = vb.x + vb.w / 2,
        cy = vb.y + vb.h / 2;
      var minW = baseW * 0.15,
        maxW = baseW * 6;
      var nw = vb.w * factor;
      if (nw < minW) factor = minW / vb.w;
      else if (nw > maxW) factor = maxW / vb.w;
      nw = vb.w * factor;
      var nh = vb.h * factor;
      vb.x = cx - nw / 2;
      vb.y = cy - nh / 2;
      vb.w = nw;
      vb.h = nh;
      setViewBox();
    }
    function zoomFit() {
      vb = { x: 0, y: 0, w: baseW, h: baseH };
      setViewBox();
    }
    zoomInBtn.addEventListener("click", function () {
      zoomBy(0.8);
    });
    zoomOutBtn.addEventListener("click", function () {
      zoomBy(1.25);
    });
    zoomFitBtn.addEventListener("click", zoomFit);

    stage.addEventListener(
      "wheel",
      function (ev) {
        ev.preventDefault();
        var b = svg.getBoundingClientRect();
        if (b.width === 0 || b.height === 0) return;
        var mx = vb.x + ((ev.clientX - b.left) / b.width) * vb.w;
        var my = vb.y + ((ev.clientY - b.top) / b.height) * vb.h;
        var f = ev.deltaY < 0 ? 0.88 : 1.14;
        // Clamp the zoom so the viewBox can neither collapse toward 0 (NaN /
        // divide-by-zero on the next wheel) nor explode far past the graph.
        var minW = baseW * 0.15,
          maxW = baseW * 6;
        var nw = vb.w * f;
        if (nw < minW) f = minW / vb.w;
        else if (nw > maxW) f = maxW / vb.w;
        nw = vb.w * f;
        var nh = vb.h * f;
        vb.x = mx - (mx - vb.x) * (nw / vb.w);
        vb.y = my - (my - vb.y) * (nh / vb.h);
        vb.w = nw;
        vb.h = nh;
        setViewBox();
      },
      { passive: false }
    );
    var dragging = false,
      lastX = 0,
      lastY = 0,
      downX = 0,
      downY = 0;
    stage.addEventListener("pointerdown", function (ev) {
      dragging = true;
      lastX = downX = ev.clientX;
      lastY = downY = ev.clientY;
      dragMoved = false;
      stage.classList.add("grabbing");
    });
    stage.addEventListener("pointermove", function (ev) {
      if (!dragging) return;
      if (Math.abs(ev.clientX - downX) > 4 || Math.abs(ev.clientY - downY) > 4) dragMoved = true;
      var b = svg.getBoundingClientRect();
      if (b.width === 0 || b.height === 0) return;
      vb.x -= ((ev.clientX - lastX) / b.width) * vb.w;
      vb.y -= ((ev.clientY - lastY) / b.height) * vb.h;
      lastX = ev.clientX;
      lastY = ev.clientY;
      setViewBox();
    });
    function stopDrag() {
      dragging = false;
      stage.classList.remove("grabbing");
    }
    stage.addEventListener("pointerup", stopDrag);
    stage.addEventListener("pointerleave", stopDrag);

    // ---- layout toggles (direction + hierarchical ⇄ force) ----
    function refreshLayoutButtons() {
      btnLR.classList.toggle("on", layoutMode === "hierarchical" && curDir === "LR");
      btnTB.classList.toggle("on", layoutMode === "hierarchical" && curDir === "TB");
      btnHier.classList.toggle("on", layoutMode === "hierarchical");
      btnForce.classList.toggle("on", layoutMode === "force");
      // Direction only applies to the hierarchical layout.
      var dirN = layoutMode === "hierarchical";
      btnLR.disabled = !dirN;
      btnTB.disabled = !dirN;
    }
    function redraw() {
      focused = null;
      closePanel();
      draw();
      refreshLayoutButtons();
    }
    btnLR.addEventListener("click", function () {
      layoutMode = "hierarchical";
      curDir = "LR";
      redraw();
    });
    btnTB.addEventListener("click", function () {
      layoutMode = "hierarchical";
      curDir = "TB";
      redraw();
    });
    btnHier.addEventListener("click", function () {
      layoutMode = "hierarchical";
      redraw();
    });
    btnForce.addEventListener("click", function () {
      if (!forceAvailable) return;
      layoutMode = "force";
      redraw();
    });

    refreshLayoutButtons();
    draw();
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
