package docker

import (
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
)

// containerInfoFromInspect builds a ContainerInfo from an inspect result.
func containerInfoFromInspect(data container.InspectResponse, status string) ContainerInfo {
	image := ""
	if data.Config != nil {
		image = data.Config.Image
	}

	var labels map[string]string
	if data.Config != nil {
		labels = data.Config.Labels
	}

	// Build a minimal Summary so we can reuse resolveServiceKey
	name := data.Name
	if strings.HasPrefix(name, "/") {
		name = name[1:]
	}
	summary := container.Summary{
		ID:      data.ID,
		Names:   []string{"/" + name},
		Image:   image,
		ImageID: data.Image,
		Labels:  labels,
	}

	key := resolveServiceKey(summary)

	var createdAt int64
	if data.Created != "" {
		if t, err := time.Parse(time.RFC3339Nano, data.Created); err == nil {
			createdAt = t.UTC().Unix()
		}
	}

	return ContainerInfo{
		ID:         data.ID,
		Name:       name,
		Image:      image,
		ImageRepo:  imageRepo(image),
		ImageID:    data.Image,
		ServiceKey: key,
		ServiceID:  serviceID(key),
		Status:     status,
		CreatedAt:  createdAt,
	}
}
