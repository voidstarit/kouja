package adminui

import (
	"context"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/internal/state"
)

func (server *Server) writeAudit(ctx context.Context, action, operationID, preState, postState, result, errorCode, remote string) error {
	if operationID == "" {
		operationID = uuid.NewString()
	}
	sourceIP := remote
	if host, _, err := net.SplitHostPort(remote); err == nil {
		sourceIP = host
	}
	now := time.Now().UTC()
	return server.options.Store.Transact(ctx, func(tx state.Tx) error {
		return tx.PutAudit(state.Audit{
			Timestamp: now, Caller: "admin-ui", OperationID: operationID,
			Action: action, PreState: preState, PostState: postState,
			Result: result, ErrorCode: errorCode, SourceIP: sourceIP,
		})
	})
}
