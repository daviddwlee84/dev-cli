package hygiene

import (
	"errors"
	"strings"

	"github.com/google/uuid"
)

// WriterAgent is one recognized live agent whose working directory covers the
// checkout a hygiene or artifact transaction will change.
type WriterAgent struct {
	Caller   bool
	Blocking bool
	// Session is the runtime-reported "provider:id" identity, when known.
	Session string
}

// WriterTarget is one file the transaction will replace.
type WriterTarget struct {
	Artifact bool
	// Session is the anchored SpecStory preamble session id, when proven.
	Session string
}

const writerOccupied = "a recognized agent still occupies the checkout"

// CheckWriters applies the artifact writer policy. Other agents always block
// artifact edits. The calling agent may edit artifacts only when every target
// is provably another session's transcript, or when the caller explicitly
// asserts disjoint ownership; its own transcript is never editable while live.
func CheckWriters(agents []WriterAgent, targets []WriterTarget, allowCallerShared bool) error {
	artifact := false
	for _, target := range targets {
		artifact = artifact || target.Artifact
	}
	for _, agent := range agents {
		if !agent.Caller {
			if agent.Blocking || artifact {
				return errors.New(writerOccupied + "; stop the writer before applying")
			}
			continue
		}
		if agent.Blocking {
			return errors.New(writerOccupied + "; stop the writer before applying")
		}
		if !artifact {
			continue
		}
		caller := sessionID(agent.Session)
		proven := caller != ""
		for _, target := range targets {
			if !target.Artifact {
				continue
			}
			targetID := canonicalSessionID(target.Session)
			if caller != "" && targetID == caller {
				return errors.New(writerOccupied + "; a target is the calling agent's own live transcript")
			}
			proven = proven && targetID != ""
		}
		if !proven && !allowCallerShared {
			return errors.New(writerOccupied + "; the calling agent cannot prove the artifacts belong to another session (stop it, or pass --allow-shared-checkout after confirming the writer exited)")
		}
	}
	return nil
}

func sessionID(session string) string {
	if _, id, ok := strings.Cut(session, ":"); ok {
		return canonicalSessionID(strings.TrimSpace(id))
	}
	return canonicalSessionID(strings.TrimSpace(session))
}

func canonicalSessionID(value string) string {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || !strings.EqualFold(id.String(), value) {
		return ""
	}
	return id.String()
}
