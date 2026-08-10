package backend

import (
	"github.com/Broderick-Westrope/anvil/internal/config"
	"github.com/Broderick-Westrope/anvil/internal/permission"
	"github.com/Broderick-Westrope/anvil/internal/proto"
)

// GrantPermission grants, denies, or persistently grants a permission
// request.
func (b *Backend) GrantPermission(workspaceID string, req proto.PermissionGrant) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	perm := permission.PermissionRequest{
		ID:          req.Permission.ID,
		SessionID:   req.Permission.SessionID,
		ToolCallID:  req.Permission.ToolCallID,
		ToolName:    req.Permission.ToolName,
		Description: req.Permission.Description,
		Action:      req.Permission.Action,
		Params:      req.Permission.Params,
		Path:        req.Permission.Path,
	}

	switch req.Action {
	case proto.PermissionAllow:
		ws.Permissions.Grant(perm)
	case proto.PermissionAllowForSession:
		ws.Permissions.GrantPersistent(perm)
	case proto.PermissionDeny:
		ws.Permissions.Deny(perm, "")
	default:
		return ErrInvalidPermissionAction
	}
	return nil
}

// SetPermissionsYoloLevel sets the yolo level for permission handling.
func (b *Backend) SetPermissionsYoloLevel(workspaceID string, level config.YoloLevel) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	ws.Permissions.SetYoloLevel(level)
	return nil
}

// GetPermissionsYoloLevel returns the current yolo level.
func (b *Backend) GetPermissionsYoloLevel(workspaceID string) (config.YoloLevel, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return config.YoloOff, err
	}

	return ws.Permissions.YoloLevel(), nil
}
