//go:build fuse

package knowledgehindsight

// The FUSE wiring, under the tag that compiles it.
//
// These are the only tests that touch the kernel's mount table, and they deliberately do not require
// a mount to succeed: the repository's rule is that no test may require a mount to work, so what is
// asserted here is the *decision* the failure path makes. `scripts/mcp_knowledge_mount.py` checks
// the consequence on a machine that can actually try, and skips with evidence when it cannot.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	knowledgev1 "github.com/Manu343726/toolbox/subsystems/knowledgehindsight/knowledgev1"
)

// A mount that cannot start must not leave a mountpoint behind.
//
// This is M-7, and it is the one mount requirement that a test over `io/fs` cannot check: the
// stale thing is a kernel mount point, and it is only reachable by actually asking the kernel to
// mount. So what is asserted here is the *decision* — that the failure path detaches — and
// `scripts/mcp_knowledge_mount.py` checks the consequence on a machine that can try.
func TestAFailedMountDetachesWhatItCreatedAndSaysSo(t *testing.T) {
	t.Parallel()
	// A path that is not a mount point, so `cleanupFailedMount` takes its "nothing to detach"
	// branch — which is the branch every failure before the kernel handshake takes.
	missing := filepath.Join(t.TempDir(), "never-mounted")
	require.NoError(t, cleanupFailedMount(missing))
	require.NoError(t, cleanupFailedMount(""), "an empty mountpoint is not an error; it is nothing to do")

	// A path that exists and is not a mount point: the same branch, reached through a real
	// directory, and the directory has to still be there afterwards.
	dir := t.TempDir()
	require.NoError(t, cleanupFailedMount(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err, "cleaning up a mountpoint must not remove the directory itself")
	assert.True(t, info.IsDir())

	// And the refusal says the mountpoint is clean, so a caller who retries is not walking into
	// the trap the requirement exists to prevent. The wording matters: "permission denied" on
	// its own leaves a reader believing a path is occupied.
	_, err = os.Stat("/dev/fuse")
	if err != nil {
		// Without the device there is nothing to attempt, and the build's own answer is the
		// one being tested elsewhere.
		t.Skip("this machine has no /dev/fuse, so a mount cannot be attempted at all")
	}
	provider := newProvider(t, newBackend(t), corpusFixture(t))
	mountpoint := filepath.Join(t.TempDir(), "mnt")
	require.NoError(t, os.Mkdir(mountpoint, 0o755))
	_, err = provider.mountHandler().EnableMount(context.Background(), connect.NewRequest(&knowledgev1.EnableMountRequest{
		Spec: &knowledgev1.MountSpec{BaseId: "docs", Mountpoint: mountpoint},
	}))
	if err == nil {
		_, _ = provider.mountHandler().DisableMount(context.Background(), connect.NewRequest(&knowledgev1.DisableMountRequest{
			MountId: "docs",
		}))
		t.Skip("this machine allowed the mount, so the failure path could not be reached")
	}
	assert.Contains(t, err.Error(), "nothing is left mounted there",
		"a refusal has to say the mountpoint is clean, or a caller cannot tell a stale path from a free one")
}
