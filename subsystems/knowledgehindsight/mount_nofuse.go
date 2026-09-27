//go:build !fuse

package knowledgehindsight

import (
	"context"
	"fmt"
	"time"

	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// The FUSE-free half of the mount.
//
// Everything here answers rather than pretending. A caller asking whether this build can mount gets
// a clear no and the tag that changes it; a caller trying to mount anyway gets an error naming the
// tag rather than a failure that looks like a host problem.

func fuseBuilt() bool { return false }

// preflight reports why this build cannot mount. There is exactly one reason and it is a build
// choice rather than a host condition, which is why it is reported differently.
func preflight() (string, error) {
	return fmt.Sprintf(
		"this binary was built without FUSE support. The mount is optional and everything else on this subsystem works: "+
			"call ExportWiki for the same projection as a bundle, or rebuild with %s to enable mounting", fuseBuildNote), nil
}

func start(_ context.Context, _ *Provider, _ *knowledgev1.MountSpec, _ string, _ time.Duration) (mountHandle, error) {
	return nil, notImplemented("mounting a knowledge base as a filesystem", fuseBuildNote)
}

func refresh(_ context.Context, _ mountHandle, _ string) (bool, error) {
	return false, nil
}
