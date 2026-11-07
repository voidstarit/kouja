import { useEffect, useState } from "preact/hooks";
import {
  deregisterVG,
  registerVG,
  removeClass,
  saveConfig,
  setClass,
} from "../api";
import { ApiError, type ConfigPageProps, type WarningView } from "../types";
import { CertificatesPanel } from "./CertificatesPanel";
import { VgsPanel } from "./VgsPanel";

export function ConfigPage({
  config,
  vgs,
  classes,
  onSaved,
  onDirtyChange,
  onRefreshVGs,
  onReloadConfig,
}: ConfigPageProps) {
  const [yaml, setYaml] = useState(config.disk_yaml);
  const [etag, setEtag] = useState(config.disk_etag);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [warnings, setWarnings] = useState<WarningView[]>([]);
  const [notice, setNotice] = useState("");

  useEffect(() => {
    if (!dirty) {
      setYaml(config.disk_yaml);
      setEtag(config.disk_etag);
    }
  }, [config.disk_yaml, config.disk_etag, dirty]);

  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);

  useEffect(() => {
    if (!dirty) {
      return;
    }
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [dirty]);

  function updateYaml(value: string) {
    setYaml(value);
    setDirty(true);
    setNotice("");
  }

  async function save() {
    setSaving(true);
    setError("");
    setWarnings([]);
    setNotice("");
    try {
      const result = await saveConfig(yaml, etag);
      setEtag(result.disk_etag);
      setDirty(false);
      setWarnings(result.warnings);
      setNotice(
        result.restart_required
          ? `Saved. Binding socket changed (${result.restart_reason}); restart the service manually.`
          : "Saved. No binding socket change.",
      );
      onSaved(result);
      await onReloadConfig();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
      if (caught instanceof ApiError && caught.status === 409) {
        await onReloadConfig();
      }
    } finally {
      setSaving(false);
    }
  }

  const saveDisabled =
    saving ||
    !dirty ||
    config.disk_error_code === "unsafe_path" ||
    config.disk_error_code === "read_error";

  return (
    <div class="stack">
      {(config.config_changed || config.restart_required) && (
        <div class={config.restart_required ? "banner warning" : "banner"}>
          {config.restart_required
            ? `Binding socket changed (${config.restart_reason}); restart the service manually.`
            : "The disk configuration differs from the effective running configuration."}
        </div>
      )}
      <section class="card">
        <h2>TLS server certificate</h2>
        {config.tls.error ? (
          <p class="error">{config.tls.error}</p>
        ) : (
          <p class="muted">
            Clients must connect using one of these names or addresses and trust the
            client CA. Use the exact value expected by the client’s TLS server-name
            verification.
          </p>
        )}
        <div class="grid">
          <div>
            <span class="muted">Subject</span>
            <strong>{config.tls.subject || "unknown"}</strong>
          </div>
          <div>
            <span class="muted">Expires</span>
            <strong>{config.tls.not_after || "unknown"}</strong>
          </div>
          <div>
            <span class="muted">TLS server name(s)</span>
            <strong class="mono">
              {config.tls.server_names.length > 0 ? config.tls.server_names.join(", ") : "none"}
            </strong>
          </div>
          <div>
            <span class="muted">IP addresses</span>
            <strong class="mono">
              {config.tls.ip_addresses.length > 0 ? config.tls.ip_addresses.join(", ") : "none"}
            </strong>
          </div>
        </div>
      </section>
      <section class="card">
        <div class="section-heading">
          <h2>Configuration</h2>
          <span class="mono muted">{config.path}</span>
        </div>
        {config.last_good_error && <p class="error">Last-good: {config.last_good_error}</p>}
        {config.disk_error && (
          <p class="error">
            {config.disk_error_code}: {config.disk_error}
          </p>
        )}
        {notice && <p class="success">{notice}</p>}
        {error && <p class="error">{error}</p>}
        {warnings.map((warning) => (
          <p class="notice" key={warning.code}>
            {warning.code}: {warning.message}
          </p>
        ))}
        <label class="field-label" for="config-editor">
          Disk YAML
        </label>
        <textarea
          id="config-editor"
          class="editor"
          value={yaml}
          onInput={(event) => updateYaml((event.target as HTMLTextAreaElement).value)}
          spellcheck={false}
        />
        <div class="form-row">
          <button onClick={save} disabled={saveDisabled}>
            {saving ? "Saving…" : "Save configuration"}
          </button>
          <button
            onClick={() => {
              setError("");
              setNotice("");
              void onReloadConfig();
            }}
            disabled={dirty || saving}
          >
            Reload
          </button>
          <span class="muted">
            Saves validate the full YAML and write atomically. The UI never restarts the service.
          </span>
        </div>
        <details>
          <summary>Effective running YAML</summary>
          <pre class="code">{config.running_yaml}</pre>
        </details>
      </section>
      <VgsPanel
        vgs={vgs}
        classes={classes}
        classActionsDisabled={dirty}
        onRegister={async (name) => {
          await registerVG(name);
          await onRefreshVGs();
        }}
        onDeregister={async (name) => {
          await deregisterVG(name);
          await onRefreshVGs();
        }}
        onSetClass={async (name, vg) => {
          await setClass(name, vg);
          await onRefreshVGs();
          await onReloadConfig();
        }}
        onRemoveClass={async (name) => {
          await removeClass(name);
          await onRefreshVGs();
          await onReloadConfig();
        }}
      />
      <CertificatesPanel />
    </div>
  );
}
