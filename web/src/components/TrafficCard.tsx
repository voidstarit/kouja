import { formatBytes, formatTime } from "../format";
import type { MetricsErrorsView, MetricsView, TargetMetricsView } from "../types";

function rate(value: number, available: boolean): string {
  return available ? `${formatBytes(value)}/s` : "—";
}

function commandRate(value: number, available: boolean): string {
  return available ? `${value.toFixed(1)}/s` : "—";
}

function errorDetails(errors: MetricsErrorsView): string[] {
  const parts: string[] = [];
  if (errors.login_failures > 0) parts.push(`login ${errors.login_failures}`);
  if (errors.authentication_failures > 0) parts.push(`auth ${errors.authentication_failures}`);
  if (errors.authorization_failures > 0) parts.push(`authorize ${errors.authorization_failures}`);
  if (errors.negotiation_failures > 0) parts.push(`negotiate ${errors.negotiation_failures}`);
  if (errors.other_login_failures > 0) parts.push(`other login ${errors.other_login_failures}`);
  if (errors.abnormal_logouts > 0) parts.push(`abnormal logout ${errors.abnormal_logouts}`);
  if (errors.format_errors > 0) parts.push(`format ${errors.format_errors}`);
  if (errors.connection_errors > 0) parts.push(`connection ${errors.connection_errors}`);
  if (errors.digest_errors > 0) parts.push(`digest ${errors.digest_errors}`);
  if (errors.connection_timeouts > 0) parts.push(`timeout ${errors.connection_timeouts}`);
  if (errors.connection_digest_errors > 0) parts.push(`conn digest ${errors.connection_digest_errors}`);
  if (errors.aborts_complete > 0) parts.push(`aborts ${errors.aborts_complete}`);
  if (errors.aborts_no_task > 0) parts.push(`aborts no-task ${errors.aborts_no_task}`);
  if (errors.resets > 0) parts.push(`resets ${errors.resets}`);
  return parts;
}

function targetLabel(target: TargetMetricsView): string {
  if (target.volume_id) {
    return `${target.volume_owner}/${target.volume_id}`;
  }
  return "unassigned target";
}

function targetErrors(target: TargetMetricsView): string {
  const details = errorDetails(target.errors);
  if (target.error_count === 0) {
    return "none";
  }
  return details.length > 0 ? details.join(" · ") : `${target.error_count} error(s)`;
}

function endpoint(address: string, port: number): string {
  return `${address}:${port}`;
}

function connectionTargetLabel(target: TargetMetricsView | undefined, ambiguous: boolean): string {
  if (target) {
    return targetLabel(target);
  }
  return ambiguous ? "ambiguous target" : "unmatched";
}

export function TrafficCard({
  metrics,
  iscsiEnabled,
}: {
  metrics: MetricsView;
  iscsiEnabled: boolean;
}) {
  const showRates = metrics.rates_available;
  const sessions = metrics.targets.reduce((sum, target) => sum + target.sessions, 0);
  return (
    <section class="card">
      <div class="section-heading">
        <h2>iSCSI traffic</h2>
        <span class="muted">
          {metrics.available ? `sampled ${formatTime(metrics.sampled_at)}` : "unavailable"}
        </span>
      </div>
      {!metrics.available ? (
        <p class={metrics.error ? "error" : "muted"}>
          {metrics.error ||
            (iscsiEnabled
              ? "Live iSCSI metrics are unavailable."
              : "iSCSI is disabled; no live target metrics are available.")}
        </p>
      ) : (
        <>
          <p class="muted">
            Counters are cumulative since target/session creation. Rates compare the current and
            previous successful sample; the first sample has no rate yet.
          </p>
          <div class="grid">
            <div>
              <span class="muted">Read</span>
              <strong>{rate(metrics.read_bytes_per_second, showRates)}</strong>
              <span class="muted">total {formatBytes(metrics.read_bytes)}</span>
            </div>
            <div>
              <span class="muted">Write</span>
              <strong>{rate(metrics.write_bytes_per_second, showRates)}</strong>
              <span class="muted">total {formatBytes(metrics.write_bytes)}</span>
            </div>
            <div>
              <span class="muted">Commands</span>
              <strong>{commandRate(metrics.commands_per_second, showRates)}</strong>
              <span class="muted">total {metrics.commands}</span>
            </div>
            <div>
              <span class="muted">Sessions</span>
              <strong>{sessions}</strong>
              <span class={metrics.error_count > 0 ? "error" : "muted"}>
                {metrics.error_count} error(s)
              </span>
            </div>
          </div>
          {!showRates && <p class="notice">Rates will appear after the next status refresh.</p>}
          <h3>Client connections</h3>
          <p class="muted">
            Established TCP client endpoints on the target portals. Target mapping is marked
            ambiguous when several targets share the same initiator address.
          </p>
          {metrics.connections_error && <p class="error">{metrics.connections_error}</p>}
          {metrics.connections.length === 0 ? (
            <p class="muted">No active client TCP connections detected on the target portals.</p>
          ) : (
            <div class="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>Client</th>
                    <th>Portal</th>
                    <th>Volume / target</th>
                    <th>Initiator</th>
                  </tr>
                </thead>
                <tbody>
                  {metrics.connections.map((connection) => {
                    const target = metrics.targets.find((entry) => entry.target === connection.target);
                    return (
                      <tr key={`${connection.local_address}:${connection.local_port}/${connection.client_address}:${connection.client_port}`}>
                        <td class="mono">
                          {endpoint(connection.client_address, connection.client_port)}
                        </td>
                        <td class="mono">
                          {endpoint(connection.local_address, connection.local_port)}
                        </td>
                        <td>
                          {connectionTargetLabel(target, connection.ambiguous)}
                          {connection.target && <div class="mono muted">{connection.target}</div>}
                        </td>
                        <td class="mono">{connection.initiator || "unknown"}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          <h3>Targets</h3>
          <div class="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Volume / target</th>
                  <th>Initiator</th>
                  <th>Client IP:port</th>
                  <th>Read</th>
                  <th>Write</th>
                  <th>Read/s</th>
                  <th>Write/s</th>
                  <th>Commands</th>
                  <th>Errors</th>
                </tr>
              </thead>
              <tbody>
                {metrics.targets.length === 0 && (
                  <tr>
                    <td colSpan={9} class="empty">
                      No iSCSI target metrics.
                    </td>
                  </tr>
                )}
                {metrics.targets.map((target) => {
                  const targetRates = showRates && target.rates_available;
                  const targetConnections = metrics.connections.filter(
                    (connection) => connection.target === target.target,
                  );
                  return (
                    <tr key={target.target}>
                      <td>
                        <strong>{targetLabel(target)}</strong>
                        <div class="mono muted">{target.target}</div>
                        {target.luns.length > 0 && (
                          <div class="muted">
                            {target.luns
                              .map(
                                (lun) =>
                                  `LUN ${lun.lun} ${lun.status} · busy ${lun.busy_count}${lun.resets > 0 ? ` · ${lun.resets} reset(s)` : ""}`,
                              )
                              .join(" · ")}
                          </div>
                        )}
                        {target.error && <div class="error">{target.error}</div>}
                      </td>
                      <td>
                        {target.initiators.length > 0 ? (
                          target.initiators.map((initiator) => (
                            <div key={initiator.initiator}>
                              <span class="mono">{initiator.initiator}</span>
                              <div class="muted">
                                RX {formatBytes(initiator.rx_bytes)} · TX{" "}
                                {formatBytes(initiator.tx_bytes)} · cmd {initiator.command_pdus}
                              </div>
                              {(initiator.connection_timeout_errors > 0 ||
                                initiator.connection_digest_errors > 0) && (
                                <div class="error">
                                  timeout {initiator.connection_timeout_errors} · digest{" "}
                                  {initiator.connection_digest_errors}
                                </div>
                              )}
                            </div>
                          ))
                        ) : (
                          "none"
                        )}
                        <div class="muted">sessions {target.sessions}</div>
                      </td>
                      <td class="mono">
                        {targetConnections.length > 0
                          ? targetConnections
                              .map((connection) =>
                                endpoint(connection.client_address, connection.client_port),
                              )
                              .join(", ")
                          : target.client_addresses.length > 0
                            ? `${target.client_addresses.join(", ")} (port unavailable)`
                            : "—"}
                      </td>
                      <td>{formatBytes(target.read_bytes)}</td>
                      <td>{formatBytes(target.write_bytes)}</td>
                      <td>{rate(target.read_bytes_per_second, targetRates)}</td>
                      <td>{rate(target.write_bytes_per_second, targetRates)}</td>
                      <td>
                        {target.commands}
                        <div class="muted">{commandRate(target.commands_per_second, targetRates)}</div>
                      </td>
                      <td class={target.error_count > 0 ? "error" : "muted"}>{targetErrors(target)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
    </section>
  );
}
