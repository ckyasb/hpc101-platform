# Controller Deployment

## Persistent State

The controller persists leases, registered SSH public keys, submission
records/results, and platform→CSOJ problem mappings to a JSON snapshot
file so state survives controller pod restarts.

### Configuration

| Env | Value | Purpose |
|-----|-------|---------|
| `HPC101_STORE_PATH` | `/data/state.json` | Path to the persistent JSON snapshot |
| `HPC101_CA_KEY_PATH` | `/etc/hpc101/ca_key` | Platform SSH CA private key (matched by bastion `ca.pub`) |

When `HPC101_STORE_PATH` is unset, the controller falls back to an
in-memory store that loses all state on restart — use only for local
development.

### Persistent Volume

`controller-state` is a `ReadWriteOnce` PVC (default 64Mi) mounted at
`/data`. The container root filesystem remains read-only
(`readOnlyRootFilesystem: true`); only `/data` is writable.

### State File Contents

`/data/state.json` is a single JSON object with four maps:

- `leases` — per-student service lease state (owner, container, host:port, lifecycle)
- `keys` — registered SSH public keys keyed by principal
- `submissions` — submission records and judging results keyed by submission ID
- `problem_map` — `{course}:{contest}:{platform_problem_id}` → CSOJ problem ID

Writes are atomic: the controller writes a `.tmp` file and renames it
over the target, so a crash cannot corrupt the snapshot.

### Backup and Restore

To back up state:

```bash
kubectl -n hpc101-platform cp <controller-pod>:/data/state.json state.json
```

To restore, write the snapshot back into the PVC before starting the
controller, or scale the Deployment to 0, replace the file, and scale
back up. The controller loads the existing snapshot on startup.

### Platform CA Trust

The controller signs student SSH certificates with the platform CA
private key mounted from the `bastion-ca-keys` secret (`ca_key` key).
The bastion trusts the matching public key (`ca.pub` in the same secret)
via `TrustedUserCAKeys`. Both must come from the same keypair so that
certificates signed by the controller are accepted by the bastion.

The `bastion-ca-keys` Secret must exist in **both** namespaces:
- `hpc101-platform` (controller): contains `ca_key` (PKCS8 PEM) + `ca.pub`
- `hpc101-bastion` (bastion): contains `ca.pub` only

The controller pod sets `fsGroup: 1000` so the mode-`0440` secret file is
readable by the non-root controller process (UID 1000).

#### Provisioning

Generate the CA and create both namespace secrets:

```bash
./deploy/bastion/provision-ca.sh
```

This creates an Ed25519 keypair in PKCS8 PEM format and populates the
secrets. Re-running regenerates the CA.

#### Verification

Confirm the controller's `ca_key` matches the bastion's `ca.pub` and that
a signed cert verifies against the bastion-trusted CA:

```bash
./deploy/bastion/verify-ca.sh
```
