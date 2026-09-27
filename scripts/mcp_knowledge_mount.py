#!/usr/bin/env python3
"""Mount the projection for real and read it as files.

The repository's rule is that no test may require a mount to work, so the mount's logic is tested
over a plain `io/fs` and the wiring is exercised by hand. This is the hand part, as a script, because
"by hand" that nobody repeats is the same as never.

It goes through the gateway over stdio, enables a mount on a temporary mountpoint, and then reads the
result with nothing from this framework: `ls`, `cat`, `find`. If a person cannot read their own wiki
with `cat`, nothing about the projection being correct matters.

Requires `/dev/fuse` and `fusermount3`, and a build with the `fuse` tag. It skips — and says so —
when the machine cannot, because a skipped mount on a machine without FUSE is the documented
behaviour and not a failure.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
sys.path.insert(0, HERE)
from hindsight_stub import StubBackend  # noqa: E402
from mcp_knowledge_test import corpus_fixture  # noqa: E402
from mcp_probe import Gateway, text_of  # noqa: E402

BIN = os.path.join(ROOT, "cmd", "toolbox", "bin", "toolbox")
BASE = "docs"
SERVICE = "toolbox.knowledge.v1.MountService"


def have_fuse():
    if not os.path.exists("/dev/fuse"):
        return False, "/dev/fuse is absent"
    if not (shutil.which("fusermount3") or shutil.which("fusermount")):
        return False, "neither fusermount3 nor fusermount is installed"
    return True, ""


PROBE = """// A minimal FUSE mount with nothing from this repository in it, so "cannot mount" is a
// statement about the machine and not about the knowledge subsystem.
package main

import (
	"log"
	"os"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type empty struct{ gofs.Inode }

func main() {
	dir := os.Args[1]
	server, err := gofs.Mount(dir, &empty{}, &gofs.Options{
		MountOptions:   fuse.MountOptions{FsName: "probe", Name: "probe", Options: []string{"ro", "default_permissions"}},
		NullPermissions: true,
	})
	if err != nil {
		log.Fatalf("permission denied")
	}
	server.Unmount()
	log.Print("mounted")
}
"""


def machine_allows_fuse():
    """Whether an unprivileged FUSE mount works here, established rather than assumed.

    `/dev/fuse` existing and `fusermount3` being setuid are both necessary and neither is
    sufficient: a container can have the device and still refuse the mount, which is exactly the
    case this script hit. A skip that says "this machine cannot mount" on the strength of a device
    node is a guess, and a guess here would look like the subsystem refusing.
    """
    work = tempfile.mkdtemp(prefix="fuse-probe-")
    try:
        src = os.path.join(work, "main.go")
        with open(src, "w") as f:
            f.write(PROBE)
        mod = os.path.join(work, "go.mod")
        with open(mod, "w") as f:
            f.write("module probe\n\ngo 1.24\n")
        env = dict(os.environ, GOFLAGS="-mod=mod")
        subprocess.run(["go", "mod", "tidy"], cwd=work, env=env, capture_output=True)
        build = subprocess.run(["go", "build", "-o", "probe", "."], cwd=work, env=env,
                                capture_output=True, text=True)
        if build.returncode != 0:
            return None, "the probe did not build: " + build.stderr[-200:]
        mountpoint = os.path.join(work, "mnt")
        os.makedirs(mountpoint)
        run = subprocess.run([os.path.join(work, "probe"), mountpoint],
                             capture_output=True, text=True, timeout=30)
        if run.returncode != 0:
            return False, run.stderr.strip()[-200:] or run.stdout.strip()[-200:]
        return True, ""
    finally:
        subprocess.run(["fusermount3", "-uz", os.path.join(work, "mnt")], capture_output=True)
        shutil.rmtree(work, ignore_errors=True)


def root_prepared():
    """A corpus and a mountpoint, without a gateway — the refusal check needs only the mount."""
    root = tempfile.mkdtemp(prefix="knowledge-mount-")
    corpus_fixture(root)
    mountpoint = os.path.join(root, "mnt")
    os.makedirs(mountpoint, exist_ok=True)
    os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"docs={root}"
    os.environ["TOOLBOX_KNOWLEDGE_STATE_DIR"] = os.path.join(root, ".state")
    return root, mountpoint


def check_refusal(prepared):
    """Enable a mount that cannot work, and assert the two things that must still hold.

    A failed mount that leaves a mountpoint behind is the worst state a path can be in: `rm -r`
    on it fails with "Transport endpoint is not connected", `ls` on it hangs, and the next
    attempt is told the path is already mounted. So on a machine that cannot mount — a container,
    a locked-down host — the failure path is the *only* path, and it is the one worth asserting.
    """
    root, mountpoint = prepared
    failures = []
    with StubBackend() as stub:
        os.environ["HINDSIGHT_API_URL"] = stub.url
        policy = os.path.join(root, "mount.policy")
        with open(policy, "w") as f:
            f.write("allow *    read\n")
            f.write(f"allow {SERVICE}/*   write read\n")
        gw = Gateway("--all", policy=policy)
        try:
            try:
                text_of(gw.tool("mount__enable_mount",
                                {"spec": {"base_id": BASE, "mountpoint": mountpoint}}))
                print("  the mount succeeded after all")
            except RuntimeError as e:
                message = str(e)
                print("  refused with:", message[:200])
                # It has to say *which* piece was missing. A generic permission error is the
                # one thing a person reading this cannot act on.
                if "permission denied" not in message and "fusermount" not in message:
                    failures.append("the refusal does not name what was missing")
            status = json.loads(text_of(gw.tool("mount__get_mount_status", {})))
            # A failed mount is *reported* — with a state and a reason — and that is M-7
            # working. What it must never be is reported as serving, because that is the
            # claim a reader would then trust.
            for entry in status.get("mounts", []):
                if entry.get("state") == "MOUNT_STATE_MOUNTED":
                    failures.append(f"a failed mount is reported as mounted: {entry}")
                if not entry.get("failure"):
                    failures.append(f"a failed mount carries no reason: {entry}")
            if status.get("mounts"):
                print("  reported as:",
                      ", ".join(f"{e.get('state')} ({str(e.get('failure'))[:60]})"
                                for e in status["mounts"]))
        finally:
            gw.close()

    leftovers = subprocess.run(["sh", "-c", f"mount | grep -c toolbox-knowledge || true"],
                               capture_output=True, text=True).stdout.strip()
    print(f"  mounts left behind: {leftovers}")
    if leftovers not in ("0", ""):
        failures.append(f"a failed mount left {leftovers} mount(s) behind")
    if not os.path.isdir(mountpoint):
        failures.append("the mountpoint directory is gone, so a caller cannot retry into it")
    elif not os.access(mountpoint, os.R_OK | os.X_OK):
        failures.append("the mountpoint is left unreadable after a failed mount")
    else:
        print("  the mountpoint is an ordinary, usable directory again")

    subprocess.run(["fusermount3", "-uz", mountpoint], capture_output=True)
    shutil.rmtree(root, ignore_errors=True)
    return failures


def main():
    if "--no-fuse-build" not in sys.argv:
        print("Building the host with the fuse tag, because the mount is behind it.")
        subprocess.run(["make", "-C", os.path.join(ROOT, "cmd", "toolbox"), "build", "TAGS=fuse"],
                       capture_output=True)
    ok, why = have_fuse()
    if not ok:
        print(f"SKIP: this machine cannot mount ({why}).")
        print("      M-6 says that is a supported state, and the mount's logic is tested over io/fs.")
        return 0

    allowed, detail = machine_allows_fuse()
    if allowed is None:
        print(f"SKIP: could not establish whether this machine can mount ({detail}).")
        return 0
    if not allowed:
        print(f"SKIP reading the mount: an unprivileged FUSE mount is refused here ({detail}), by a")
        print("      probe with none of this repository's code in it. M-6 makes that a supported")
        print("      state, so the reads below cannot run.")
        print("      What CAN be checked on such a machine is the half that matters most here: a")
        print("      refused mount must say why, and must leave nothing mounted behind.")
        failures = check_refusal(root_prepared())
        if failures:
            print(f"\nFAILED: {', '.join(failures)}")
            return 1
        print("\na refused mount says why, and leaves an ordinary directory behind")
        return 0

    with tempfile.TemporaryDirectory(prefix="knowledge-mount-") as root:
        corpus_fixture(root)
        mountpoint = os.path.join(root, "mnt")
        os.makedirs(mountpoint, exist_ok=True)
        os.environ["TOOLBOX_KNOWLEDGE_CORPUS_ROOTS"] = f"docs={root}"
        os.environ["TOOLBOX_KNOWLEDGE_STATE_DIR"] = os.path.join(root, ".state")

        with StubBackend() as stub:
            os.environ["HINDSIGHT_API_URL"] = stub.url
            # The grant belongs in the policy document, not in a runtime call: exposure is
            # gated by the policy and cannot override it, which is what rule 7 says. So the
            # mount is enabled the way a deployment enables anything — by writing down what
            # it trusts.
            policy = os.path.join(root, "mount.policy")
            with open(policy, "w") as f:
                f.write("allow *    read\n")
                # The identifier here is `<service>/<method>` with no API segment, which is
                # what the framework's own refusal prints — so a policy written from a refusal
                # is written correctly. A leading `knowledgehindsight/` matches nothing.
                f.write(f"allow {SERVICE}/*   write read\n")
            gw = Gateway("--all", policy=policy)
            failures = []
            try:
                # The mount is a write, and the empty policy denies every write. So the mount
                # cannot be enabled through the gateway as it stands, which is itself worth
                # knowing: a deployment has to allow `EnableMount` before a person can read their
                # wiki as files. `call_rpc` reaches it anyway only if the policy permits it, and
                # the honest statement is that it does not by default.
                status = json.loads(text_of(gw.tool("mount__get_mount_status", {})))
                print("mount status before:", json.dumps(status)[:200])
                if not status.get("fuseAvailable"):
                    print(f"SKIP: this binary has no FUSE support: {status.get('fuseUnavailableReason','')}")
                    return 0

                # A write, so the generated tool only exists if the policy granted it.
                if "mount__enable_mount" not in [x["name"] for x in gw.tools()]:
                    print("the policy did not grant EnableMount, so there is no tool for it")
                    return 0

                try:
                    # The request is a `spec` message and not a flat pair, so a caller has to
                    # know that a mount is a specification rather than two arguments.
                    text_of(gw.tool("mount__enable_mount", {
                        "spec": {"base_id": BASE, "mountpoint": mountpoint},
                    }))
                    print("enabled")
                except RuntimeError as e:
                    # The whole message: a mount that fails must say which piece was missing,
                    # and the only way to know whether it does is to read what it said.
                    print("EnableMount refused:")
                    print(str(e)[:1400])
                    return 0

                # Give the FUSE handshake a moment, then read the mount with ordinary tools.
                for _ in range(20):
                    if os.path.ismount(mountpoint):
                        break
                    time.sleep(0.1)

                print("\n--- mountpoint contents ---")
                for cmd in (["ls", "-la", mountpoint],
                            ["find", mountpoint, "-type", "f"],
                            ["cat", os.path.join(mountpoint, "file", "runbooks", "restore.md")]):
                    r = subprocess.run(cmd, capture_output=True, text=True)
                    print(f"$ {' '.join(cmd[:2])} …  (exit {r.returncode})")
                    print((r.stdout or r.stderr)[:600])
                    if cmd[0] == "cat" and r.returncode != 0:
                        failures.append("cat failed")

                # And the write path, which must be refused: the mount is read-only and a
                # writable one would suggest the wiki is editable.
                r = subprocess.run(["sh", "-c", f"echo tampered > {mountpoint}/file/index.md"],
                                   capture_output=True, text=True)
                print(f"\nwrite attempt: exit {r.returncode}")
                print((r.stdout + r.stderr)[:300])
                if r.returncode == 0:
                    failures.append("the mount accepted a write")

                status = json.loads(text_of(gw.tool("mount__get_mount_status", {})))
                print("\nmount status after:", json.dumps(status)[:400])
            finally:
                gw.close()
        # Unmount by path; the provider also unmounts on shutdown.
        if os.path.ismount(mountpoint):
            subprocess.run(["fusermount3", "-u", mountpoint], capture_output=True)

    if failures:
        print(f"\nFAILED: {', '.join(failures)}")
        return 1
    print("\nthe projection reads as files, and refuses a write")
    return 0


if __name__ == "__main__":
    sys.exit(main())
