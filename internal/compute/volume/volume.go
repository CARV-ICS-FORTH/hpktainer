package volume

import (
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// NotFoundBackoff is the recommended backoff for a resource that is required,
// but is not created yet. For instance, when mounting configmap volumes to pods.
// TODO: in future version, the backoff can be self-modified depending on the load of the controller.
var NotFoundBackoff = wait.Backoff{
	Steps:    4,
	Duration: 2 * time.Second,
	Factor:   5.0,
	Jitter:   0.1,
}
