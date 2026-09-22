package snippet

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func ValidateHost(host string) error {
	u, err := url.Parse("https://" + host)
	if err != nil || host == "" || u.Host != host || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(host, "\\\x00\r\n\t ") {
		return errors.New("snippet host must be an exact configured hostname")
	}
	return nil
}

func ValidateProject(project string) error {
	if project == "" || strings.ContainsAny(project, ":\\\x00\r\n?#%") || strings.TrimSpace(project) != project {
		return errors.New("GitLab project must be an ID or namespace/project path")
	}
	for _, part := range strings.Split(project, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid GitLab project path")
		}
	}
	return nil
}

func ValidateIdentity(id Identity) error {
	if id.Forge != GitHub && id.Forge != GitLab {
		return errors.New("snippet forge must be github or gitlab")
	}
	if err := ValidateHost(id.Host); err != nil {
		return err
	}
	if id.ID == "" {
		return errors.New("snippet ID is required")
	}
	if id.Forge == GitHub {
		for _, c := range id.ID {
			if !(c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' || c >= '0' && c <= '9') {
				return errors.New("GitHub gist ID must be hexadecimal")
			}
		}
		if len(id.ID) > 64 || id.ProjectID != 0 || id.Project != "" {
			return errors.New("invalid GitHub gist identity")
		}
	} else {
		value, err := strconv.ParseInt(id.ID, 10, 64)
		if err != nil || value <= 0 || strconv.FormatInt(value, 10) != id.ID {
			return errors.New("GitLab snippet ID must be a positive decimal integer")
		}
		if id.ProjectID < 0 {
			return errors.New("invalid GitLab project ID")
		}
		if id.Project != "" {
			return ValidateProject(id.Project)
		}
	}
	return nil
}

// ParseReference binds an ID/URL to configured hosts. It never guesses a forge
// from the characters in a bare ID, and never adopts an arbitrary URL host.
func ParseReference(raw string, defaultForge Kind, hosts map[Kind]string) (Identity, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Identity{}, errors.New("snippet reference is required")
	}
	id := Identity{Forge: defaultForge}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
			return Identity{}, errors.New("snippet URL must be a canonical HTTPS URL without credentials, query or fragment")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		githubHost := hosts[GitHub]
		gistHost := "gist." + githubHost
		if githubHost == "github.com" {
			gistHost = "gist.github.com"
		}
		switch {
		case githubHost != "" && strings.EqualFold(u.Host, gistHost):
			id.Forge, id.Host = GitHub, githubHost
			if len(parts) == 1 {
				id.ID = parts[0]
			} else if len(parts) == 2 && parts[0] != "" {
				id.ID = parts[1]
			} else {
				return Identity{}, errors.New("invalid GitHub gist URL")
			}
		case githubHost != "" && !strings.EqualFold(githubHost, "github.com") && strings.EqualFold(u.Host, githubHost) &&
			len(parts) == 3 && parts[0] == "gist" && parts[1] != "" && parts[1] != "." && parts[1] != "..":
			// Enterprise Server without subdomain isolation serves Gists below
			// the configured main host. Match this path before a GitLab host
			// match because both adapters may use the same configured hostname.
			id.Forge, id.Host, id.ID = GitHub, githubHost, parts[2]
		case hosts[GitLab] != "" && strings.EqualFold(u.Host, hosts[GitLab]):
			id.Forge, id.Host = GitLab, hosts[GitLab]
			if len(parts) == 3 && parts[0] == "-" && parts[1] == "snippets" {
				id.ID = parts[2]
			} else if len(parts) == 2 && parts[0] == "snippets" {
				id.ID = parts[1]
			} else {
				index := -1
				for i := 1; i < len(parts)-2; i++ {
					if parts[i] == "-" && parts[i+1] == "snippets" {
						index = i
						break
					}
				}
				if index > 0 && index+3 == len(parts) {
					id.Project = strings.Join(parts[:index], "/")
					id.ID = parts[index+2]
				} else {
					// Older GitLab versions omit the /-/ separator.
					if len(parts) < 4 || parts[len(parts)-2] != "snippets" {
						return Identity{}, errors.New("invalid GitLab snippet URL")
					}
					id.Project = strings.Join(parts[:len(parts)-2], "/")
					id.ID = parts[len(parts)-1]
				}
			}
		default:
			return Identity{}, errors.New("snippet URL does not belong to a configured forge host")
		}
	} else {
		if prefix, rest, ok := strings.Cut(raw, ":"); ok {
			id.Forge = Kind(prefix)
			raw = rest
		}
		id.ID = raw
		id.Host = hosts[id.Forge]
	}
	if defaultForge != "" && defaultForge != All && id.Forge != defaultForge {
		return Identity{}, fmt.Errorf("reference belongs to %s, not the selected %s forge", id.Forge, defaultForge)
	}
	if err := ValidateIdentity(id); err != nil {
		return Identity{}, err
	}
	return id, nil
}
