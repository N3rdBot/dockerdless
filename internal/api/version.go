package api

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
)

const (
	// VersionedPathPattern documents the Docker version-prefix route contract.
	VersionedPathPattern = "/v{version:[0-9.]+}"
	// AdvertisedAPIVersion is the highest API version this MVP advertises.
	AdvertisedAPIVersion = "1.44"
	// MinimumAPIVersion is the oldest API version accepted by this MVP.
	MinimumAPIVersion = "1.24"
	// AdvertisedBuilderVersion is the BuildKit builder version advertised by the daemon.
	AdvertisedBuilderVersion = "2"
)

// VersionMiddleware performs Docker API version negotiation and applies the
// compatibility headers expected on every response.
type VersionMiddleware struct {
	advertisedVersion string
	minimumVersion    string
	builderVersion    string
}

// NewVersionMiddleware creates the default Docker API version middleware.
func NewVersionMiddleware() VersionMiddleware {
	return VersionMiddleware{
		advertisedVersion: AdvertisedAPIVersion,
		minimumVersion:    MinimumAPIVersion,
		builderVersion:    AdvertisedBuilderVersion,
	}
}

// Wrap applies version negotiation to the supplied handler.
func (m VersionMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", m.advertisedVersion)
		w.Header().Set("Builder-Version", m.builderVersion)
		w.Header().Set("Ostype", runtime.GOOS)

		requestedVersion, versioned, err := versionFromPath(r.URL.Path)
		if err != nil {
			WriteDockerError(w, NewInvalidParameter(err.Error()))
			return
		}
		if versioned {
			comparison, compareErr := compareAPIVersions(requestedVersion, m.minimumVersion)
			if compareErr != nil {
				WriteDockerError(w, NewInvalidParameter(compareErr.Error()))
				return
			}
			if comparison < 0 {
				WriteDockerError(w, NewInvalidParameter(fmt.Sprintf(
					"client version %s is too old. Minimum supported API version is %s",
					requestedVersion,
					m.minimumVersion,
				)))
				return
			}

			comparison, compareErr = compareAPIVersions(requestedVersion, m.advertisedVersion)
			if compareErr != nil {
				WriteDockerError(w, NewInvalidParameter(compareErr.Error()))
				return
			}
			if comparison > 0 {
				WriteDockerError(w, NewInvalidParameter(fmt.Sprintf(
					"client version %s is too new. Maximum supported API version is %s",
					requestedVersion,
					m.advertisedVersion,
				)))
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func versionFromPath(path string) (string, bool, error) {
	if !strings.HasPrefix(path, "/v") {
		return "", false, nil
	}

	rest := strings.TrimPrefix(path, "/v")
	end := strings.IndexByte(rest, '/')
	if end < 0 {
		end = len(rest)
	}
	version := rest[:end]
	if version == "" || !allVersionCharacters(version) {
		return "", false, nil
	}
	if _, err := parseAPIVersion(version); err != nil {
		return "", true, fmt.Errorf("invalid API version %q: %w", version, err)
	}
	return version, true, nil
}

func allVersionCharacters(version string) bool {
	for _, character := range version {
		if (character < '0' || character > '9') && character != '.' {
			return false
		}
	}
	return true
}

func parseAPIVersion(version string) ([]int, error) {
	parts := strings.Split(version, ".")
	if len(parts) == 0 {
		return nil, errors.New("version is empty")
	}

	parsed := make([]int, len(parts))
	for index, part := range parts {
		if part == "" {
			return nil, errors.New("version contains an empty component")
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return nil, fmt.Errorf("version component %q is not a non-negative integer", part)
		}
		parsed[index] = value
	}
	return parsed, nil
}

func compareAPIVersions(left, right string) (int, error) {
	leftParts, err := parseAPIVersion(left)
	if err != nil {
		return 0, err
	}
	rightParts, err := parseAPIVersion(right)
	if err != nil {
		return 0, err
	}

	length := len(leftParts)
	length = max(length, len(rightParts))
	for index := range length {
		leftPart, rightPart := 0, 0
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		switch {
		case leftPart < rightPart:
			return -1, nil
		case leftPart > rightPart:
			return 1, nil
		}
	}
	return 0, nil
}

func unversionedPath(path string) string {
	version, versioned, err := versionFromPath(path)
	if err != nil || !versioned {
		return path
	}

	path = strings.TrimPrefix(path, "/v"+version)
	if path == "" {
		return "/"
	}
	return path
}
