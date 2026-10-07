// Package registry provides repository registry management.
package registry

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/fsutil"
)

// ValidateName validates a repository name.
// Names must match ^[A-Za-z0-9._-]+$ AND not start with '.'
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	matched, _ := regexp.MatchString(`^[A-Za-z0-9._-]+$`, name)
	if !matched {
		return fmt.Errorf("invalid repository name %q: must match [A-Za-z0-9._-]+", name)
	}

	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid repository name %q: cannot start with '.'", name)
	}

	return nil
}

// RepoNameFromURL extracts a repository name from a URL.
// It strips trailing slashes, removes one .git suffix, and takes the basename.
func RepoNameFromURL(gitURL string) (string, error) {
	// Strip trailing slashes
	for strings.HasSuffix(gitURL, "/") {
		gitURL = gitURL[:len(gitURL)-1]
	}

	// Strip one .git suffix
	gitURL = strings.TrimSuffix(gitURL, ".git")

	// Take basename
	name := path.Base(gitURL)

	// Validate
	if err := ValidateName(name); err != nil {
		return "", err
	}

	return name, nil
}

// CanonicalRemoteIdentity normalizes a git URL into a canonical identity.
// This implements the exact algorithm from the reference implementation.
func CanonicalRemoteIdentity(gitURL string) (string, error) {
	gitURL = strings.TrimSpace(gitURL)
	if gitURL == "" {
		return "", fmt.Errorf("empty URL")
	}

	// Try SCP format first: [user@]host:path (with no ://)
	if !strings.Contains(gitURL, "://") {
		scpRegex := regexp.MustCompile(`^(?:([^/@:]+)@)?([^/:]+):(.+)$`)
		matches := scpRegex.FindStringSubmatch(gitURL)
		if matches != nil {
			user := matches[1]
			host := matches[2]
			pathPart := matches[3]

			// Process path: rstrip "/", strip one ".git", lstrip "/"
			pathPart = strings.TrimRight(pathPart, "/")
			pathPart = strings.TrimSuffix(pathPart, ".git")
			host = strings.ToLower(host)
			pathPart = strings.TrimLeft(pathPart, "/")

			// Lowercase path only for github.com
			if host == "github.com" {
				pathPart = strings.ToLower(pathPart)
			}

			if user == "git" {
				return fmt.Sprintf("repository:%s/%s", host, pathPart), nil
			}
			return fmt.Sprintf("scp:%s@%s/%s", user, host, pathPart), nil
		}

		// Not SCP, check if it's a file path
		u, err := url.Parse(gitURL)
		if err != nil || u.Scheme == "" {
			// Treat as file path
			fpath := gitURL
			if !filepath.IsAbs(fpath) {
				cwd, err := os.Getwd()
				if err != nil {
					return "", fmt.Errorf("failed to get working directory: %w", err)
				}
				fpath = filepath.Join(cwd, fpath)
			}
			resolved, err := filepath.EvalSymlinks(fpath)
			if err != nil {
				// Try without EvalSymlinks if the path doesn't exist
				resolved, _ = filepath.Abs(fpath)
			}
			return fmt.Sprintf("file:%s", resolved), nil
		}
	}

	// Parse as URL
	u, err := url.Parse(gitURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	switch u.Scheme {
	case "file":
		// file:// URLs
		if u.Host != "" && u.Host != "localhost" {
			return "", fmt.Errorf("unsupported file URL authority: %s", u.Host)
		}

		pathPart := url.QueryEscape(u.Path)
		pathPart, _ = url.QueryUnescape(pathPart)

		fpath := pathPart
		if !filepath.IsAbs(fpath) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("failed to get working directory: %w", err)
			}
			fpath = filepath.Join(cwd, fpath)
		}
		resolved, err := filepath.EvalSymlinks(fpath)
		if err != nil {
			// Try with Abs if EvalSymlinks fails
			resolved, _ = filepath.Abs(fpath)
		}
		return fmt.Sprintf("file:%s", resolved), nil

	case "http", "https", "git", "ssh":
		host := u.Hostname()
		if host == "" {
			return "", fmt.Errorf("remote URL has no host")
		}
		host = strings.ToLower(host)

		// Decode path
		pathPart := u.Path
		pathPart = strings.TrimRight(pathPart, "/")
		pathPart = strings.TrimSuffix(pathPart, ".git")
		pathPart = strings.TrimLeft(pathPart, "/")

		// Lowercase path only for github.com
		if host == "github.com" {
			pathPart = strings.ToLower(pathPart)
		}

		user := ""
		if u.User != nil {
			user = u.User.Username()
		}

		// Check if conventional transport
		isConventional := u.Port() == "" &&
			((u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "git") && user == "" ||
				u.Scheme == "ssh" && user == "git")

		if isConventional {
			return fmt.Sprintf("repository:%s/%s", host, pathPart), nil
		}

		// Non-conventional
		port := ""
		if u.Port() != "" {
			port = ":" + u.Port()
		}
		return fmt.Sprintf("network:%s://%s@%s%s/%s", u.Scheme, user, host, port, pathPart), nil

	default:
		return "", fmt.Errorf("unrecognized URL scheme: %s", u.Scheme)
	}
}

// Read reads the repository registry from a file.
func Read(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read registry: %w", err)
	}

	var names []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}

	return names, nil
}

// Write writes the repository registry to a file atomically.
// The list is sorted and deduplicated.
func Write(path string, names []string) error {
	// Deduplicate and sort
	unique := make(map[string]bool)
	var sorted []string
	for _, name := range names {
		if !unique[name] {
			sorted = append(sorted, name)
			unique[name] = true
		}
	}
	sort.Strings(sorted)

	// Write to file
	content := strings.Join(sorted, "\n")
	if len(sorted) > 0 {
		content += "\n" // Trailing newline
	}

	return fsutil.AtomicWrite(path, []byte(content), 0600)
}
