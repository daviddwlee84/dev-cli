//go:build windows

package machineregistry

import "context"

func planPermissions(path string) (PermissionPlan, error) {
	diagnostic := &PathError{Path: path, Reason: "automatic registry ACL repair is unavailable; inspect this path's owner and ACL in Windows Security and recheck", Owner: "inspect Windows Security", ExpectedOwner: "current user; private protected ACL"}
	return PermissionPlan{Diagnostics: []*PathError{diagnostic}}, nil
}
func applyPermissions(context.Context, *permissionState) (PermissionResult, error) {
	return PermissionResult{}, ErrInvalidPlan
}
