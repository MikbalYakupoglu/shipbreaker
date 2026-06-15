package docker

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// resolveServiceKey returns the canonical service_key for a container.
// Format: "<source>:<value>" — first non-empty source wins.
func resolveServiceKey(c container.Summary) string {
	labels := c.Labels

	// 1. Compose
	project := labels["com.docker.compose.project"]
	service := labels["com.docker.compose.service"]
	if project != "" && service != "" {
		return "compose:" + project + "/" + service
	}

	// 2. Swarm
	if svc := labels["com.docker.swarm.service.name"]; svc != "" {
		return "swarm:" + svc
	}

	// 3. Explicit label
	if v := labels["shipbreaker.service"]; v != "" {
		return "label:" + v
	}

	// 4. Image repo (strip tag and digest)
	repo := imageRepo(c.Image)
	if repo != "" {
		return "image:" + repo
	}

	// 5. Container name (last resort; strip leading "/")
	if len(c.Names) > 0 {
		name := strings.TrimPrefix(c.Names[0], "/")
		return "name:" + name
	}
	return "name:" + c.ID[:12]
}

// serviceID returns the first 16 hex chars of SHA-256(key) — URL-safe opaque identifier.
func serviceID(key string) string {
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", h[:8]) // 8 bytes = 16 hex chars
}

// imageRepo strips tag and digest from a full image reference.
// "ghcr.io/acme/api:1.2" → "ghcr.io/acme/api"
// "redis:alpine"          → "redis"
// "sha256:abc..."         → "" (bare digest, treat as unknown)
func imageRepo(image string) string {
	if strings.HasPrefix(image, "sha256:") {
		return ""
	}
	// strip digest
	if i := strings.Index(image, "@"); i != -1 {
		image = image[:i]
	}
	// strip tag — only after the last slash (registry:5000/repo has colon in host)
	slash := strings.LastIndex(image, "/")
	base := image[slash+1:]
	if i := strings.Index(base, ":"); i != -1 {
		image = image[:slash+1] + base[:i]
	}
	return image
}
