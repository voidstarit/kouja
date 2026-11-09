import { useEffect, useRef, useState } from "preact/hooks";
import { getClasses, getConfig, getStatus, getVGs } from "./api";
import { ConfigPage } from "./components/ConfigPage";
import { StatusPage } from "./components/StatusPage";
import type {
  ConfigResponse,
  SaveConfigResponse,
  StatusResponse,
  VGStatus,
} from "./types";

type Tab = "status" | "config";

function tabFromLocation(): Tab {
  return window.location.hash.replace(/^#\/?/, "") === "config" ? "config" : "status";
}

export function App() {
  const [tab, setTab] = useState<Tab>(tabFromLocation);
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [config, setConfig] = useState<ConfigResponse | null>(null);
  const [vgs, setVGs] = useState<VGStatus[]>([]);
  const [classes, setClasses] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [configError, setConfigError] = useState("");
  const [configDirty, setConfigDirty] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  async function refreshStatus() {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    try {
      const next = await getStatus(controller.signal);
      setStatus(next);
      setError("");
    } catch (caught) {
      if (caught instanceof DOMException && caught.name === "AbortError") {
        return;
      }
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      if (abortRef.current === controller) {
        setLoading(false);
      }
    }
  }

  async function refreshConfig() {
    try {
      const next = await getConfig();
      setConfig(next);
      setConfigError("");
    } catch (caught) {
      setConfigError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  async function refreshVGs() {
    try {
      const [nextVGs, nextClasses] = await Promise.all([getVGs(), getClasses()]);
      setVGs(nextVGs.vgs ?? []);
      setClasses(nextClasses.classes ?? {});
    } catch (caught) {
      setConfigError(caught instanceof Error ? caught.message : String(caught));
    }
  }

  async function refreshAll() {
    await Promise.all([refreshStatus(), refreshConfig(), refreshVGs()]);
  }

  useEffect(() => {
    if (!window.location.hash) {
      window.history.replaceState(null, "", "#/status");
    }
    void refreshAll();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void refreshStatus();
      }
    }, 5000);
    return () => {
      window.clearInterval(timer);
      abortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    if (tab === "config") {
      void refreshVGs();
    }
  }, [tab]);

  useEffect(() => {
    const onHashChange = () => {
      const next = tabFromLocation();
      if (next === tab) {
        return;
      }
      if (tab === "config" && configDirty && !window.confirm("Discard unsaved configuration changes?")) {
        window.history.replaceState(null, "", `#/${tab}`);
        return;
      }
      setConfigDirty(false);
      setTab(next);
    };
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, [tab, configDirty]);

  function selectTab(next: Tab) {
    if (next === tab) {
      return;
    }
    if (tab === "config" && configDirty && !window.confirm("Discard unsaved configuration changes?")) {
      return;
    }
    setConfigDirty(false);
    window.location.hash = `/${next}`;
  }

  const onSaved = (_result: SaveConfigResponse) => {
    void refreshStatus();
  };

  return (
    <div class="app">
      <header class="app-header">
        <div>
          <h1>kouja admin</h1>
          <p class="muted">Local-only configuration and status. No authentication.</p>
        </div>
        <nav class="tabs" aria-label="Main navigation" role="tablist">
          <button
            role="tab"
            aria-selected={tab === "status"}
            class={tab === "status" ? "active" : ""}
            onClick={() => selectTab("status")}
          >
            Status
          </button>
          <button
            role="tab"
            aria-selected={tab === "config"}
            class={tab === "config" ? "active" : ""}
            onClick={() => selectTab("config")}
          >
            Configuration
          </button>
        </nav>
      </header>
      <main>
        {tab === "status" && (
          <StatusPage
            status={status}
            loading={loading}
            error={error}
            onRefresh={() => void refreshStatus()}
          />
        )}
        {tab === "config" &&
          (config ? (
            <ConfigPage
              config={config}
              vgs={vgs}
              classes={classes}
              onSaved={onSaved}
              onDirtyChange={setConfigDirty}
              onRefreshVGs={refreshVGs}
              onReloadConfig={refreshConfig}
            />
          ) : (
            <section class="card">
              <h2>Configuration</h2>
              <p class={configError ? "error" : "muted"}>
                {configError || "Loading configuration…"}
              </p>
              <button onClick={() => void refreshConfig()}>Retry</button>
            </section>
          ))}
      </main>
    </div>
  );
}
