package dotfile

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strings"
)

type SetupRequest struct {
	RepoURL string
	Preset  string
	Apply   bool
}

// SetupPlan only authorizes initialization when both source and native config
// are absent. Existing configuration is useful recovery evidence, not a blank
// installation to replace with a different repository.
type SetupPlan struct {
	Status  Status
	Mode    string
	RepoURL string
	Apply   bool

	options   Options
	request   SetupRequest
	authority setupAuthority
	ready     bool
}

type setupAuthority struct {
	ConfigState, ConfigPath, SourceState, SourceDir, SourceStateDir, WorkingTree, Platform string
	ConfigHash                                                                             [32]byte
	Installed                                                                              bool
}

func PlanSetup(ctx context.Context, options Options, request SetupRequest) (SetupPlan, error) {
	return planSetupStatus(options, request, Observe(ctx, options))
}

func planSetupStatus(options Options, request SetupRequest, s Status) (SetupPlan, error) {
	p := SetupPlan{Mode: "blocked", Apply: request.Apply, options: options, request: request}
	if request.RepoURL != "" && request.Preset != "" {
		return p, errors.New("choose only one of --repo and --preset")
	}
	if request.Preset != "" && request.Preset != "david" {
		return p, errors.New("unknown dotfile preset; available preset: david")
	}
	p.Status = s
	if s.SourceState == "present" {
		p.Mode = "existing-source"
		return p, nil
	}
	if s.SourceState != "absent" {
		return p, errors.New("cannot establish that the source is absent; resolve the configuration before initialization")
	}
	if s.ConfigState != "absent" {
		p.Mode = "existing-config"
		return p, nil
	}
	if request.RepoURL == "" && s.Recommendation != nil && s.Recommendation.Experimental {
		p.Mode = "bootstrap"
		return p, nil
	}
	if !s.Installed {
		p.Mode = "install-chezmoi"
		return p, nil
	}
	p.RepoURL = request.RepoURL
	if request.Preset == "david" {
		if s.Recommendation == nil {
			return p, errors.New("no david preset is available for this platform; choose --repo explicitly")
		}
		p.RepoURL = s.Recommendation.RepoURL
	}
	if p.RepoURL == "" {
		p.Mode = "choose-repository"
		return p, nil
	}
	if strings.HasPrefix(p.RepoURL, "-") || strings.ContainsAny(p.RepoURL, "\x00\r\n") {
		return p, errors.New("invalid repository URL")
	}
	p.Mode, p.ready = "initialize", true
	p.authority = setupIdentity(s)
	return p, nil
}

func setupIdentity(s Status) setupAuthority {
	a := setupAuthority{ConfigState: s.ConfigState, ConfigPath: s.ConfigPath, SourceState: s.SourceState, SourceDir: s.SourceDir, SourceStateDir: s.SourceStateDir, WorkingTree: s.WorkingTree, Platform: s.Platform, Installed: s.Installed}
	if s.ConfigPath != "" {
		if data, err := readBounded(s.ConfigPath, maxConfigBytes); err == nil {
			a.ConfigHash = sha256.Sum256(data)
		}
	}
	return a
}

// ApplySetup refreshes the reviewed authority immediately before native init.
// Native chezmoi retains its locking, prompts and arbitrary script semantics;
// dev does not promise rollback of native hooks or package installation.
func ApplySetup(ctx context.Context, plan SetupPlan, in io.Reader, out, errOut io.Writer) error {
	if !plan.ready || plan.Mode != "initialize" {
		return errors.New("setup plan does not authorize initialization")
	}
	fresh, err := PlanSetup(ctx, plan.options, plan.request)
	if err != nil || !fresh.ready || fresh.authority != plan.authority || fresh.RepoURL != plan.RepoURL || fresh.Apply != plan.Apply {
		return errors.New("dotfile configuration or source changed; review setup again")
	}
	args := []string{plan.RepoURL}
	if plan.Apply {
		args = append([]string{"--apply"}, args...)
	}
	return RunNative(ctx, plan.options.ConfigPath, "init", args, in, out, errOut)
}
