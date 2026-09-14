package sshvault

type resultState struct {
	plan      *planState
	status    string
	binding   string
	native    string
	endpoints string
	receipt   Receipt
}

// CanSelectAgentKey permits matching a successful, unchanged Apply receipt to a
// fresh provider-agent inventory. It neither queries an agent nor proves agent
// signing or SSH authentication. Serialized, fabricated or modified plans and
// results carry no selection authority.
func CanSelectAgentKey(plan Plan, result Result) bool {
	state := result.state
	if plan.state == nil || state == nil || state.plan != plan.state || !plan.state.service.ownsPlan(plan) || result.Receipt == nil {
		return false
	}
	if result.Status != StatusCreated || result.Status != state.status || result.BindingStatus != state.binding || result.NativeContextStatus != state.native || result.EndpointStatus != state.endpoints || *result.Receipt != state.receipt || result.Receipt.Destination != plan.Destination {
		return false
	}
	line, fingerprint, err := publicIdentity(result.Receipt.PublicLine)
	if err != nil || line != result.Receipt.PublicLine || fingerprint != result.Receipt.Fingerprint {
		return false
	}
	switch plan.Destination.Provider {
	case OnePassword:
		return opIDPattern.MatchString(result.Receipt.ItemID) && result.BindingStatus == BindingVerified && result.NativeContextStatus == "" && result.EndpointStatus == ""
	case Bitwarden:
		return bwIDPattern.MatchString(result.Receipt.ItemID) && plan.Experimental && plan.NativeContextApproved && plan.state.native != nil && result.BindingStatus == BindingUnknown && result.NativeContextStatus == NativeObservedConsistent && result.EndpointStatus == EndpointUnverified
	default:
		return false
	}
}
