import { formatBytes, formatTime, formatUptime } from "../format";
import type { StatusPageProps } from "../types";
import { OperationsTable } from "./OperationsTable";
import { StateBadge } from "./StateBadge";
import { TrafficCard } from "./TrafficCard";
import { VolumesTable } from "./VolumesTable";

export function StatusPage({ status, loading, error, onRefresh }: StatusPageProps) {
  if (!status) {
    return (
      <section class="card">
        <h2>Status</h2>
        {error ? <p class="error">{error}</p> : <p class="muted">Loading status…</p>}
        <button onClick={onRefresh} disabled={loading}>
          Refresh
        </button>
      </section>
    );
  }
  const agent = status.agent;
  return (
    <div class="stack">
      {error && <p class="error">{error}</p>}
      {(agent.config_changed || agent.restart_required) && (
        <div class={agent.restart_required ? "banner warning" : "banner"}>
          {agent.restart_required
            ? `Binding socket changed (${agent.restart_reason}); restart the service manually.`
            : "The disk configuration differs from the running configuration."}
        </div>
      )}
      <section class="card">
        <div class="section-heading">
          <h2>Agent</h2>
          <button onClick={onRefresh} disabled={loading}>
            {loading ? "Refreshing…" : "Refresh"}
          </button>
        </div>
        <div class="grid">
          <div>
            <span class="muted">Readiness</span>
            <strong>{agent.ready ? "ready" : "not ready"}</strong>
          </div>
          <div>
            <span class="muted">Version</span>
            <strong>{agent.version}</strong>
          </div>
          <div>
            <span class="muted">Host UUID</span>
            <strong class="mono">{agent.host_uuid}</strong>
          </div>
          <div>
            <span class="muted">Uptime</span>
            <strong>{formatUptime(agent.uptime_seconds)}</strong>
          </div>
          <div>
            <span class="muted">Control listener</span>
            <strong class="mono">{agent.listen_address}</strong>
          </div>
          <div>
            <span class="muted">Admin listener</span>
            <strong class="mono">{agent.admin_address}</strong>
          </div>
          <div>
            <span class="muted">Protocols</span>
            <strong>{agent.protocols.join(", ") || "none"}</strong>
          </div>
          <div>
            <span class="muted">Started</span>
            <strong>{formatTime(agent.started_at)}</strong>
          </div>
        </div>
        {agent.last_good_error && <p class="error">Last-good write: {agent.last_good_error}</p>}
      </section>
      <section class="card">
        <h2>iSCSI</h2>
        <div class="grid">
          <div>
            <span class="muted">Enabled</span>
            <strong>{status.iscsi.enabled ? "yes" : "no"}</strong>
          </div>
          <div>
            <span class="muted">IQN prefix</span>
            <strong class="mono">{status.iscsi.iqn_prefix || "not configured"}</strong>
          </div>
          <div>
            <span class="muted">Portals</span>
            <strong class="mono">
              {status.iscsi.portals.length > 0 ? status.iscsi.portals.join(", ") : "none"}
            </strong>
          </div>
        </div>
      </section>
      <TrafficCard
        metrics={status.metrics}
        iscsiEnabled={status.iscsi.enabled}
        onRefresh={onRefresh}
      />
      <section class="card">
        <h2>Volume groups</h2>
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>State</th>
                <th>Class</th>
                <th>Total</th>
                <th>Free</th>
                <th>Allocatable</th>
                <th>Observed</th>
                <th>Error</th>
              </tr>
            </thead>
            <tbody>
              {status.vgs.length === 0 && (
                <tr>
                  <td colSpan={8} class="empty">
                    No volume groups.
                  </td>
                </tr>
              )}
              {status.vgs.map((vg) => (
                <tr key={vg.name}>
                  <td>
                    <strong>{vg.name}</strong>
                    <div class="muted">
                      {vg.configured ? "configured" : "live only"}
                    </div>
                  </td>
                  <td>
                    <StateBadge state={vg.state} />
                  </td>
                  <td>{vg.storage_class}</td>
                  <td>{formatBytes(vg.total_bytes)}</td>
                  <td>{formatBytes(vg.free_bytes)}</td>
                  <td>{formatBytes(vg.allocatable_bytes)}</td>
                  <td>{formatTime(vg.observed_at)}</td>
                  <td class="error">{vg.error}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      {status.errors.length > 0 && (
        <section class="card">
          <h2>Diagnostics</h2>
          <ul class="errors">
            {status.errors.map((entry, index) => (
              <li key={`${entry.code}-${index}`}>
                <strong>{entry.code}</strong>: {entry.message}
              </li>
            ))}
          </ul>
        </section>
      )}
      <VolumesTable
        volumes={status.volumes}
        truncated={status.counts.truncated_volumes}
        total={status.counts.volumes}
        onRefresh={onRefresh}
      />
      <OperationsTable
        operations={status.operations}
        truncated={status.counts.truncated_operations}
        total={status.counts.operations}
      />
    </div>
  );
}
