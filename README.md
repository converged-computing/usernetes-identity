# Usernetes Identity 

**A userspace Identity virtualization layer for HPC workloads.**

Designed for rootless container environments like Usernetes, this proxy allows containers to utilize the full 65,535 UID space while strictly confining host-side operations to a constrained UID pool (e.g., a 2,000 UID allocation). This is common practice for a multi-tenant HPC system. We cannot allocate the full range of identifiers to every user.

It achieves this through a "Double Proxy" architecture:

1. **Filesystem Identity (FUSE):** A FUSE daemon that deterministically maps container UIDs to the host pool and persists the true UID in extended attributes (xattrs).
2. **Process Identity (Seccomp):** A Seccomp-notify supervisor that intercepts identity syscalls (e.g., `getuid`) and spoofs the return values so HPC runtimes (like MPI) see the IDs they expect.

## How does it work?

We map high UIDs ($0-65535$) into a small host pool ($1-1999$) using a stable hash. Node A and Node B will always map Container UID 500 to the exact same Host UID, preserving HPC fabric integrity without a centralized database. File ownership collisions are resolved by storing the original Container UID in `user.usernetes.uid`. We build with statically linked CGO (`libseccomp`) and pure-Go networking/user resolvers, ensuring it runs on any HPC node regardless of local `glibc` versions. Finally, the Seccomp supervisor validates PID lifecycles before responding to notifications to prevent PID-reuse attacks.


## Prerequisites

* **Linux Kernel:** 5.0+ (Required for Seccomp User Notifications).
* **Build Dependencies:** Go 1.20+ and the `libseccomp` C headers.

```bash
sudo apt-get update && sudo apt-get install libseccomp-dev
```

## Building

If you need libseccomp:

```bash
wget https://github.com/seccomp/libseccomp/releases/download/v2.5.5/libseccomp-2.5.5.tar.gz
tar -xzf libseccomp-2.5.5.tar.gz
cd libseccomp-2.5.5
./configure --enable-static GPERF=/bin/true --prefix=/usr/workspace/usernetes/install
make
make install
```

Clone!

```bash
git clone https://github.com/converged-computing/usernetes-identity
```

Then use the Makfile:

```bash
make
```
```bash
chmod +x bin/usernetes-identity
mv bin/usernetes-identity /usr/workspace/usernetes/install/bin/
```

Note: The `-tags netgo,osusergo` flag is important to bypass glibc's dynamic NSS dependencies. I think without that if we built and moved it we would have a problem. I have not yet tried building and deploying elsewhere (but maybe could).

## Deployment (Control Plane)

Deploying to a Usernetes node requires configuring both the container storage layer and the Kubelet. First, install the binary.

```bash
mkdir -p ~/.local/bin
cp bin/usernetes-identity ~/.local/bin/usernetes-identity
chmod 755 ~/.local/bin/usernetes-identity
```

Tell the storage driver to use this proxy instead of standard fuse-overlayfs.

```bash
vim ~/.config/containers/storage.conf
```
```console
[storage.options.overlay]
mount_program = "/home/your_user/.local/bin/usernetes-identity"
mountopt = "nodev,nosuid"
```

E.g.,

```console
[storage]
  driver = "overlay"
  runroot = "/var/tmp/sochat1/run-34633/containers"
  graphroot = "/var/tmp/sochat1/config/containers/storage"
[storage.options.overlay]
  mount_program = "/usr/workspace/usernetes/install/bin/usernetes-identity"
  mountopt = "nodev,nosuid"
[storage.options.vfs]
  ignore_chown_errors = "true"
```

Note that the seccomp profile is added to the container in the kubelet rootless data directory `/var/lib/kubelet/seccomp/`. It looks like this:

```console
{
    "defaultAction": "SCMP_ACT_ALLOW",
    "architectures": ["SCMP_ARCH_X86_64"],
    "syscalls": [
        {
            "names": ["getuid", "geteuid", "getgid", "getegid"],
            "action": "SCMP_ACT_NOTIFY"
        }
    ]
}
```

Start usernetes as you typically would. We assume the following user namespace mapping via build flags for the node:

- 0:0:1 (Root is pinned)
- 1:1:1999 (The 1,999 slot deterministic pool)
- 65534:2000:2 (Nobody is pinned)

When you bring up the control plane node, you need to update the config.toml inside (before install-calico) to be:

```bash
# explicitly use v2 config format
version = 2

# 1. Snapshotter Configuration
[plugins."io.containerd.snapshotter.v1.fuse-overlayfs"]
  binary_path = "/usr/bin/usernetes-identity"
  # Optional: explicitly set the root for snapshot data
  root_path = "/var/lib/containerd/io.containerd.snapshotter.v1.fuse-overlayfs"

# 2. CRI Plugin Configuration
[plugins."io.containerd.grpc.v1.cri"]
  # Use fixed sandbox image for stability in HPC/rootless
  sandbox_image = "registry.k8s.io/pause:3.10"
  tolerate_missing_hugepages_controller = true
  # Mandatory for rootless (UserNS)
  restrict_oom_score_adj = true

  [plugins."io.containerd.grpc.v1.cri".containerd]
    # Link CRI to your identity-aware snapshotter
    snapshotter = "fuse-overlayfs"
    discard_unpacked_layers = true
    default_runtime_name = "runc"

    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
      runtime_type = "io.containerd.runc.v2"
      base_runtime_spec = "/etc/containerd/cri-base.json"
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
        SystemdCgroup = true

    # Runtime class for Kubernetes tests
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.test-handler]
      runtime_type = "io.containerd.runc.v2"
      base_runtime_spec = "/etc/containerd/cri-base.json"
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.test-handler.options]
        SystemdCgroup = true
```

And ensure usernetes-identity is at that path. Debugging containerd and the setup:

```bash
# On the inside for containerd
journalctl -u containerd -n 100

# Outside for the cluster (e.g., kubelet)
make logs
```

## Testing

```bash
mkdir -p /tmp/u7s-test/{lower,upper,work,merged}
touch /tmp/u7s-test/lower/test-file
touch /tmp/u7s-test/merged/identity-check
git pull && make && cp ./bin/usernetes-identity /tmp/sochat1/usernetes/
fusermount -u /tmp/u7s-test/merged && cp usernetes-identity /usr/bin/ && bash /tmp/test.sh 
```
And test.sh

```bash
#!/bin/bash

/usr/bin/usernetes-identity     -o lowerdir=/tmp/u7s-test/lower,upperdir=/tmp/u7s-test/upper,workdir=/tmp/u7s-test/work     /tmp/u7s-test/merged

# 1. Create a file in the virtualized mount
touch /tmp/u7s-test/merged/identity-check

# 2. Change ownership to a high UID/GID (e.g., 60000)
chown 60000:60000 /tmp/u7s-test/merged/identity-check

# 3. Verify the "Virtual" view (Inside the mount)
# If spoofing is working, this should show 60000.
# If it shows 65535, the kernel doesn't recognize the ID.
ls -ln /tmp/u7s-test/merged/identity-check

# 4. Verify the "Real" view (On the host/upper directory)
# This should show a hashed ID between 1 and 1999 (e.g., 1633)
ls -ln /tmp/u7s-test/upper/identity-check

# 5. Verify the Extended Attributes
# This confirms the original UID is persisted to disk
getfattr -d -m "user.usernetes.*" /tmp/u7s-test/upper/identity-check

# Test UID hashing only
touch /tmp/u7s-test/merged/test-uid
chown 60000:0 /tmp/u7s-test/merged/test-uid
ls -ln /tmp/u7s-test/upper/test-uid  # Should see mapped UID, GID 0

# Test GID hashing only
touch /tmp/u7s-test/merged/test-gid
chown 0:60000 /tmp/u7s-test/merged/test-gid
ls -ln /tmp/u7s-test/upper/test-gid  # Should see UID 0, mapped GID

touch /tmp/u7s-test/merged/test-nobody
chown 65534:65534 /tmp/u7s-test/merged/test-nobody
ls -ln /tmp/u7s-test/upper/test-nobody # Should show UID 2000
```

This should be the correct output:

```bash
root@u7s-ipa8:/usernetes# bash /tmp/test.sh 
-rw-r--r-- 1 60000 60000 0 May 22 11:27 /tmp/u7s-test/merged/identity-check
-rw-r--r-- 1 1633 1633 0 May 22 11:27 /tmp/u7s-test/upper/identity-check
getfattr: Removing leading '/' from absolute path names
# file: tmp/u7s-test/upper/identity-check
user.usernetes.gid="60000"
user.usernetes.uid="60000"

-rw-r--r-- 1 1633 0 0 May 22 11:27 /tmp/u7s-test/upper/test-uid
-rw-r--r-- 1 0 1633 0 May 22 11:27 /tmp/u7s-test/upper/test-gid
-rw-r--r-- 1 2000 2000 0 May 22 11:27 /tmp/u7s-test/upper/test-nobody
```

Here is a more manual test. Create a test alpine pod.

```yaml
# kubectl apply -f test-pod.yaml
apiVersion: v1
kind: Pod
metadata:
  name: uid-test-pod
spec:
  containers:
  - name: test-container
    image: docker.io/library/alpine:latest
    command: ["sleep", "infinity"]
```

Create uids for it.

```bash
kubectl exec uid-test-pod -- sh -c "
>   touch /usernetes-fuse-test-1500
>   chown 1500:1500 /usernetes-fuse-test-1500
>   
>   touch /usernetes-fuse-test-nobody
>   chown 65534:65534 /usernetes-fuse-test-nobody
>   
>   ls -ln /usernetes-fuse-test-*
> "
-rw-r--r--    1 1500     1500             0 May  7 15:12 /usernetes-fuse-test-1500
-rw-r--r--    1 65534    65534            0 May  7 15:12 /usernetes-fuse-test-nobody
```

And for a pod manifest, we need a seccomp profile. Here is to test.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: identity-test
spec:
  securityContext:
    # 1. Apply the Seccomp profile we created
    seccompProfile:
      type: Localhost
      localhostProfile: hpc-profile.json
    # 2. Force the pod to run as a high UID
    runAsUser: 60000
    runAsGroup: 60000
  containers:
  - name: alpine
    image: alpine:latest
    command: ["/bin/sh", "-c"]
    args: ["sleep 3600"]
```

## License

HPCIC DevTools is distributed under the terms of the MIT license.
All new contributions must be made under this license.

See [LICENSE](https://github.com/converged-computing/cloud-select/blob/main/LICENSE),
[COPYRIGHT](https://github.com/converged-computing/cloud-select/blob/main/COPYRIGHT), and
[NOTICE](https://github.com/converged-computing/cloud-select/blob/main/NOTICE) for details.

SPDX-License-Identifier: (MIT)

LLNL-CODE- 842614
