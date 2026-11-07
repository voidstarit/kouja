import { formatBytes, formatTime } from "../format";
import type { VolumeStatus } from "../types";
import { StateBadge } from "./StateBadge";

export function VolumesTable({
  volumes,
  truncated,
  total,
}: {
  volumes: VolumeStatus[];
  truncated: boolean;
  total: number;
}) {
  return (
    <section class="card">
      <div class="section-heading">
        <h2>Volumes</h2>
        <span class="muted">
          {volumes.length} shown{truncated ? ` of ${total}` : ""}
        </span>
      </div>
      {truncated && (
        <p class="notice">The volume list is truncated. Use the mTLS API for a complete inventory.</p>
      )}
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Owner / volume</th>
              <th>VG / class</th>
              <th>Sizes</th>
              <th>State</th>
              <th>Serving</th>
              <th>Export</th>
              <th>Observation</th>
              <th>Updated</th>
            </tr>
          </thead>
          <tbody>
            {volumes.length === 0 && (
              <tr>
                <td colSpan={8} class="empty">
                  No volumes.
                </td>
              </tr>
            )}
            {volumes.map((volume) => {
              const observation = volume.observation;
              const exportObservation = volume.export_observation;
              const exportState = volume.export;
              return (
                <tr key={`${volume.owner}/${volume.volume_id}`}>
                  <td>
                    <strong>{volume.volume_id}</strong>
                    <div class="muted">{volume.owner}</div>
                    <div class="mono muted">{volume.resource_uuid}</div>
                  </td>
                  <td>
                    {volume.vg_name}
                    <div class="muted">{volume.storage_class}</div>
                  </td>
                  <td>
                    {formatBytes(volume.requested_size_bytes)}
                    <div class="muted">actual {formatBytes(volume.actual_size_bytes)}</div>
                  </td>
                  <td>
                    <StateBadge state={volume.state} />
                    {volume.drift_reasons.length > 0 && (
                      <div class="drift">{volume.drift_reasons.join(", ")}</div>
                    )}
                  </td>
                  <td>
                    <span class={volume.serving ? "status-ok" : "status-warn"}>
                      {volume.serving ? "yes" : "no / unknown"}
                    </span>
                  </td>
                  <td>
                    {exportState ? (
                      <>
                        <StateBadge state={exportState.state} />
                        <div class="muted">{exportState.protocol} · {exportState.initiator}</div>
                        <div class="mono muted">{exportState.portal}</div>
                        {exportObservation && (
                          <>
                            <div class="muted">
                              target {exportObservation.target_present ? "yes" : "no"} · ACL{" "}
                              {exportObservation.acl_present ? "yes" : "no"} · namespace{" "}
                              {exportObservation.namespace_present ? "yes" : "no"}
                            </div>
                            {exportObservation.observed_initiator && (
                              <div class="mono muted">
                                observed ACL: {exportObservation.observed_initiator}
                              </div>
                            )}
                            <div class="muted">
                              sessions:{" "}
                              {exportObservation.active_sessions === null
                                ? "unknown"
                                : exportObservation.active_sessions}{" "}
                              · session {exportObservation.session_state}
                              {exportObservation.stale ? " (stale)" : ""}
                            </div>
                          </>
                        )}
                      </>
                    ) : (
                      <span class="muted">unknown</span>
                    )}
                  </td>
                  <td>
                    {observation ? (
                      <>
                        <div>LV {observation.lv_present ? "present" : "missing"}</div>
                        <div class="muted">
                          {formatTime(observation.observed_at)}
                          {observation.stale ? " · stale" : ""}
                        </div>
                      </>
                    ) : (
                      <span class="muted">unknown</span>
                    )}
                  </td>
                  <td>{formatTime(volume.updated_at)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
