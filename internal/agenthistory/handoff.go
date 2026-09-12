package agenthistory

import (
	"context"
	"errors"

	"github.com/daviddwlee84/dev-cli/internal/configedit"
)

func (s *Service) PolicyFingerprint(ctx context.Context) (string, error) {
	a, e := configedit.InspectToken(ctx, s.policyPath())
	if e != nil {
		return "", e
	}
	b, e := configedit.InspectToken(ctx, s.bindingPath())
	if e != nil {
		return "", e
	}
	capture, e := s.captureFingerprint(ctx)
	if e != nil {
		return "", e
	}
	return hash([]byte(a + "\x00" + b + "\x00" + s.Binding.CaptureDir + "\x00" + capture)), nil
}

func (s *Service) LocateSession(ctx context.Context, session string) (string, error) {
	if e := s.validateCapture(ctx); e != nil {
		return "", e
	}
	if s.Policy.Source != "specstory" {
		return "", errors.New("prepared sessions require SpecStory; use artifact archive --file for exported plans")
	}
	p, id, e := parseSession(session)
	if e != nil {
		return "", e
	}
	files, e := s.sources(ctx)
	if e != nil {
		return "", e
	}
	var paths []string
	for _, file := range files {
		path := s.capturePath(file)
		provider, found, err := prefixIdentity(path)
		if err == nil && provider == p && found == id {
			paths = append(paths, path)
		}
	}
	if len(paths) != 1 {
		return "", errors.New("session does not identify one exact SpecStory transcript")
	}
	return paths[0], nil
}

func (s *Service) CheckSessionPlan(ctx context.Context, id, session string) error {
	p, e := s.load(ctx, id)
	if e != nil {
		return e
	}
	provider, sid, e := parseSession(session)
	if e != nil {
		return e
	}
	if p.View.Kind != "artifact_archive" || p.Root != s.Root || p.Policy.ProjectID != s.Policy.ProjectID || len(p.Snapshots) != 1 || p.Snapshots[0].Provider != provider || p.Snapshots[0].SessionID != sid {
		return errors.New("archive plan does not match the prepared session")
	}
	return nil
}
