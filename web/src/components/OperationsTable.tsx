import { formatTime } from "../format";
import type { OperationView } from "../types";
import { StateBadge } from "./StateBadge";

export function OperationsTable({
  operations,
  truncated,
  total,
}: {
  operations: OperationView[];
  truncated: boolean;
  total: number;
}) {
  return (
    <section class="card">
      <div class="section-heading">
        <h2>Problem operations</h2>
        <span class="muted">
          {operations.length} shown{truncated ? ` of ${total}` : ""}
        </span>
      </div>
      {truncated && <p class="notice">The operation list is truncated.</p>}
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th>Operation</th>
              <th>Volume</th>
              <th>State</th>
              <th>Attempt</th>
              <th>Deadline</th>
              <th>Last error</th>
            </tr>
          </thead>
          <tbody>
            {operations.length === 0 && (
              <tr>
                <td colSpan={6} class="empty">
                  No problem operations.
                </td>
              </tr>
            )}
            {operations.map((operation) => (
              <tr key={operation.id}>
                <td>
                  <strong>{operation.action}</strong>
                  <div class="mono muted">{operation.id}</div>
                </td>
                <td>
                  {operation.volume_id}
                  <div class="muted">{operation.owner}</div>
                </td>
                <td>
                  <StateBadge state={operation.state} />
                </td>
                <td>
                  {operation.attempt}/{operation.max_attempts}
                </td>
                <td>{formatTime(operation.deadline_at)}</td>
                <td>
                  {operation.last_error_code || "none"}
                  {operation.last_error_retryable && <div class="muted">retryable</div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
