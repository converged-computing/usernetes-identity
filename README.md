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

Clone!

```bash
git clone https://github.com/converged-computing/usernetes-identity
```

Then use the Makfile:

```bash
make
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

Deploy the Seccomp Profile. Usernetes applies Seccomp profiles via the Kubelet. Create the profile in Kubelet's rootless data directory.

```console
mkdir -p ~/.local/share/usernetes/kubelet/seccomp/
vim ~/.local/share/usernetes/kubelet/seccomp/hpc-profile.json
hpc-profile.json:
```
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

And for a pod manifest, we need a seccomp profile:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: hpc-workload
spec:
  securityContext:
    seccompProfile:
      type: Localhost
      localhostProfile: hpc-profile.json
  containers:
  - name: hpc-app
    image: my-registry/hpc-app:latest
    command: ["/bin/sh", "-c"]
    args: ["id && mpirun ..."]
```

## License

HPCIC DevTools is distributed under the terms of the MIT license.
All new contributions must be made under this license.

See [LICENSE](https://github.com/converged-computing/cloud-select/blob/main/LICENSE),
[COPYRIGHT](https://github.com/converged-computing/cloud-select/blob/main/COPYRIGHT), and
[NOTICE](https://github.com/converged-computing/cloud-select/blob/main/NOTICE) for details.

SPDX-License-Identifier: (MIT)

LLNL-CODE- 842614