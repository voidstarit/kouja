import { useEffect, useState } from "preact/hooks";
import { formatBytes } from "../format";
import type { VgsPanelProps } from "../types";
import { StateBadge } from "./StateBadge";

export function VgsPanel({
  vgs,
  classes,
  classActionsDisabled,
  onRegister,
  onDeregister,
  onSetClass,
  onRemoveClass,
}: VgsPanelProps) {
  const [vgName, setVgName] = useState("");
  const [className, setClassName] = useState("");
  const [classVG, setClassVG] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  const registered = vgs.filter((vg) => vg.state === "active" || vg.state === "unavailable");

  useEffect(() => {
    if (registered.length > 0 && !registered.some((vg) => vg.name === classVG)) {
      setClassVG(registered[0].name);
    }
  }, [registered, classVG]);

  async function run(action: () => Promise<void>, success: string) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await action();
      setMessage(success);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section class="card">
      <h2>Live VG and class actions</h2>
      <p class="muted">
        These actions apply immediately and do not restart the service. Raw YAML
        <code>storage.vgs</code> is a first-start seed; raw <code>storage.classes</code>
        applies on restart.
      </p>
      {error && <p class="error">{error}</p>}
      {message && <p class="success">{message}</p>}
      <div class="form-row">
        <input
          value={vgName}
          onInput={(event) => setVgName((event.target as HTMLInputElement).value)}
          placeholder="volume group"
          aria-label="volume group name"
        />
        <button
          disabled={busy || !vgName.trim()}
          onClick={() =>
            run(async () => {
              await onRegister(vgName.trim());
              setVgName("");
            }, "Volume group registered.")
          }
        >
          Register VG
        </button>
      </div>
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>VG</th>
              <th>State</th>
              <th>Class</th>
              <th>Capacity</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {vgs.map((vg) => (
              <tr key={vg.name}>
                <td>{vg.name}</td>
                <td>
                  <StateBadge state={vg.state} />
                  {vg.error && <div class="error">{vg.error}</div>}
                </td>
                <td>{vg.storage_class}</td>
                <td>{vg.state === "unknown" ? "unknown" : formatBytes(vg.allocatable_bytes)}</td>
                <td>
                  {vg.state === "active" && (
                    <button
                      class="danger"
                      disabled={busy}
                      onClick={() =>
                        run(() => onDeregister(vg.name), "Volume group deregistered.")
                      }
                    >
                      Remove
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <h3>Storage classes</h3>
      <div class="form-row">
        <input
          value={className}
          onInput={(event) => setClassName((event.target as HTMLInputElement).value)}
          placeholder="class name"
          aria-label="storage class name"
          disabled={classActionsDisabled}
        />
        <select
          value={classVG}
          onChange={(event) => setClassVG((event.target as HTMLSelectElement).value)}
          disabled={classActionsDisabled}
          aria-label="storage class volume group"
        >
          {registered.map((vg) => (
            <option key={vg.name} value={vg.name}>
              {vg.name}
            </option>
          ))}
        </select>
        <button
          disabled={busy || classActionsDisabled || !className.trim() || !classVG}
          onClick={() =>
            run(async () => {
              await onSetClass(className.trim(), classVG);
              setClassName("");
            }, "Class mapping applied.")
          }
        >
          Add class
        </button>
      </div>
      {classActionsDisabled && (
        <p class="muted">Save or discard the YAML editor changes before changing classes.</p>
      )}
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Class</th>
              <th>VG</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {Object.entries(classes).length === 0 && (
              <tr>
                <td colSpan={3} class="empty">
                  No class mappings.
                </td>
              </tr>
            )}
            {Object.entries(classes).map(([name, vg]) => (
              <tr key={name}>
                <td>{name}</td>
                <td>{vg}</td>
                <td>
                  <button
                    class="danger"
                    disabled={busy || classActionsDisabled}
                    onClick={() => run(() => onRemoveClass(name), "Class mapping removed.")}
                  >
                    Remove
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
