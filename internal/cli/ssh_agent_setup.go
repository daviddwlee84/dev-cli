package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/daviddwlee84/dev-cli/internal/sshhost"
)

var errSSHFleetImportAgent = errors.New("named-agent keys are not supported while importing connection or hop profiles FROM fleet; import with existing controller authentication or --config-only, then run regular dev ssh setup <local-alias> to select its agent key. Registering a regular alias TO fleet with --to fleet|both remains supported")

func validateSSHAgentOptions(options sshSetupOptions) error {
	selected := options.identityAgent != "" || options.agentRef != nil || options.keyCandidate != nil && options.keyCandidate.Agent != nil || options.keyPlan != nil && options.keyPlan.Agent != nil
	if !selected {
		return nil
	}
	if options.generateKey || options.configOnly || options.auth != "" {
		return errors.New("--identity-agent selects an existing agent key and cannot be combined with --generate-key, --config-only or --auth")
	}
	if options.identityFileChanged || options.identitiesOnlyChanged && !options.identitiesOnly {
		return errors.New("a named-agent key supplies its own public IdentityFile and IdentitiesOnly=yes; do not combine it with --identity-file or --identities-only=false")
	}
	if options.fleetImportKeyPicker || strings.HasPrefix(options.from, "fleet:") || strings.HasPrefix(options.from, "fleet-ssh:") {
		return errSSHFleetImportAgent
	}
	return nil
}

// prepareSSHAgentPublication keeps the selected agent as durable controller
// intent. An existing public copy never turns it into a local private signer.
func prepareSSHAgentPublication(ctx context.Context, service *sshhost.Service, alias, class string, options sshSetupOptions, plan sshhost.KeyPlan, definition *sshhost.ManagedDefinition) (*sshhost.KeyPlan, error) {
	if plan.Agent == nil {
		return nil, nil
	}
	if err := validateSSHAgentOptions(options); err != nil {
		return nil, err
	}
	if options.keyCandidate == nil {
		return nil, errors.New("agent key selection has no catalog candidate; select the key again")
	}
	publicPath := service.DefaultAgentPublicPath(plan.Agent.Provider, alias)
	if class == "foreign" {
		if options.dryRun {
			return nil, sshAgentManualError(alias, *plan.Agent, publicPath)
		}
		if err := service.VerifyAgentKeyPolicy(ctx, alias, *options.keyCandidate); err != nil {
			return nil, errors.Join(sshAgentManualError(alias, *plan.Agent, publicPath), err)
		}
		return nil, nil
	}
	publication, err := service.PlanKey(ctx, sshhost.KeyRequest{Operation: sshhost.KeyPublishAgent, Candidate: *options.keyCandidate, PublicDestination: publicPath})
	if err != nil {
		return nil, err
	}
	if !publication.Ready() {
		return nil, sshKeyPlanBlockedError(publication)
	}
	definition.IdentityFile, definition.IdentityAgent = publication.PublicPath, plan.Agent.Socket
	enabled := true
	definition.IdentitiesOnly = &enabled
	return &publication, nil
}

func validateSSHFleetImportAgentPlans(item sshOnboardItem) error {
	if item.Import == nil {
		return nil
	}
	if item.AgentPublicPlan != nil || item.KeyPlan != nil && item.KeyPlan.Agent != nil {
		return errSSHFleetImportAgent
	}
	for _, plan := range item.HopKeyPlans {
		if plan.Agent != nil {
			return errSSHFleetImportAgent
		}
	}
	return nil
}

func sshAgentPreview(item sshOnboardItem) []string {
	if item.KeyPlan == nil || item.KeyPlan.Agent == nil {
		return nil
	}
	ref := item.KeyPlan.Agent
	notes := []string{fmt.Sprintf("Selected %s SSH agent: %s", sshhost.AgentProviderLabel(ref.Provider), ref.Socket)}
	if publication := item.AgentPublicPlan; publication != nil {
		notes = append(notes,
			fmt.Sprintf("Public key only: %s %s (no private key file is created).", publication.Action, publication.PublicPath),
			"Managed SSH v2: IdentityAgent "+ref.Socket,
			"IdentityFile "+publication.PublicPath+"; IdentitiesOnly yes.",
			"dev v0.2.37 and older cannot manage this v2 fragment.")
	} else {
		notes = append(notes, "Existing foreign SSH agent policy matches; no SSH configuration or public key file will be written.")
	}
	return notes
}
