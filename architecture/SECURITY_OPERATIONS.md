# Messaging Platform - Security Operations Runbook

| | |
|---|---|
| **Document** | Security Operations Runbook v1.0 |
| **Audience** | On-call engineers, SRE, security, database operators, release managers |
| **Status** | Required operational procedure |
| **Scope** | Incident response, recovery, rotation, deployment, monitoring, and release evidence |
| **Sources of truth** | `SECURITY.md`, `SECURITY_SPEC.md`, `DEVOPS.md`, `ARCHITECTURE.md`, `DATABASE.md` |

This document turns the platform security and reliability standards into
operator actions. It complements the source documents; it does not redefine
authentication, cryptography, data retention, RPO/RTO, or product behavior. If
instructions conflict, stop the operation, preserve evidence, and escalate the
conflict for review.

Never place credentials, private keys, tokens, message content, personal data,
or unredacted production output in tickets, chat, commits, or command history.
Use the managed secret store and approved evidence repository.

---

## 1. Operating Model

### 1.1 Roles

| Role | Responsibility |
|---|---|
| Incident commander (IC) | Owns severity, priorities, timeline, and final recovery decision |
| Operations lead | Executes containment, failover, rebuild, and service restoration |
| Security lead | Owns evidence, scope, credential/key rotation, and disclosure input |
| Database lead | Owns database integrity, PITR, replication, and restore verification |
| Communications lead | Maintains internal updates and approved user/regulator communications |
| Scribe | Records timestamps, actions, actors, evidence locations, and decisions |
| Release manager | Owns deployment gates, artifact identity, rollback, and post-release watch |

One person may fill multiple roles for a small incident, but IC and scribe must
always be named. Production break-glass access is time-limited, individually
attributed, and audited.

### 1.2 Severity

| Severity | Examples | Response |
|---|---|---|
| SEV-0 Critical | Confirmed data exposure, signing-key compromise, destructive host/container compromise, unrecoverable primary loss, active account takeover at scale | Page security and primary/secondary on-call immediately; IC within 10 minutes; contain before investigation |
| SEV-1 High | Refresh-reuse storm, material authz bypass, active exploitation, regional outage, failed primary with healthy recovery path | Page owning teams immediately; IC within 15 minutes |
| SEV-2 Medium | Bounded suspicious activity, degraded redundancy, backup failure with an older verified restore point, high-risk vulnerability without exploitation | Notify on-call and security; owner within 1 hour |
| SEV-3 Low | Policy deviation without exposure, scan finding, non-sensitive operational anomaly | Ticket with owner and remediation date |

When uncertain, choose the higher severity. Downgrade only after evidence bounds
the blast radius.

### 1.3 Required Incident Record

Create the incident record before non-emergency investigation, or immediately
after urgent containment. Record:

- Detection time, reporter, alert, initial severity, and IC.
- Affected environments, services, users, data classes, credentials, keys, and
  time window.
- Every action with UTC timestamp, operator, command/change reference, result,
  and rollback status.
- Evidence hashes and storage locations. Preserve logs, audit records, traces,
  image digests, deployment manifests, database timelines, and cloud audit
  events before retention windows expire.
- Current containment state, customer impact, RPO/RTO measurements, and next
  update time.
- Regulatory/privacy assessment and approved communications. Never speculate
  publicly.

Do not modify suspected systems merely to inspect them when a snapshot or
read-only collection is available. Evidence access must itself be audited.

---

## 2. Incident Response

Every incident follows this sequence:

1. **Detect** - validate the signal from monitoring, audit, edge, provider, or
   external report. Preserve the original alert and timestamps.
2. **Triage** - assign severity, IC, affected trust boundaries, likely data
   classes, and immediate safety risks.
3. **Contain** - stop exposure and attacker persistence. For user-data or key
   incidents, containment takes priority over complete understanding.
4. **Investigate** - construct the timeline from immutable/audited sources;
   identify initial access, persistence, privilege changes, lateral movement,
   and exfiltration.
5. **Eradicate** - remove the vector by replacing untrusted systems, revoking
   credentials, fixing code/configuration, and closing exposed paths.
6. **Recover** - restore from trusted artifacts and verified backups, run
   migrations, health checks, business-flow tests, and staged traffic return.
7. **Verify** - rerun the original detection, validate audit/monitoring, and
   observe a defined clean window before closure.
8. **Learn** - produce a blameless post-mortem, update the threat model and
   runbooks, and track every corrective action to completion.

### 2.1 Immediate Containment Menu

Use only the controls required by the known blast radius:

- Revoke a session/device or all sessions for a user; bump token version and
  blacklist affected access-token `jti` values.
- Revoke a refresh-token family and alert on continued reuse.
- Disable an account, service principal, CI identity, API token, or provider
  credential.
- Add edge WAF/rate-limit blocks and close origin ingress. Preserve the rule
  change in code after emergency use.
- Remove an instance from load balancing; isolate the host/network; stop
  suspected containers without deleting evidence.
- Quarantine media or disable a feature by the approved kill switch.
- Disable writes or place the service in maintenance mode if integrity cannot
  be guaranteed.

### 2.2 Account Takeover / Refresh-Reuse Storm

**Detect:** refresh reuse, unusual device/geography, login spray, unexplained
session creation, or user report.

**Contain:** revoke the affected refresh family; suspend sessions/devices;
blacklist live access `jti` values; require identifier verification and a
trusted-device step where available. For broad reuse, invoke global revocation
only with IC approval because it signs every user out.

**Investigate:** correlate session, token-family, user, IP, device, request, and
audit identifiers. Determine whether the cause is device theft, credential
sharing, malicious client, server secret exposure, or token-store compromise.

**Recover:** rotate exposed credentials/keys as applicable, close the vector,
restore account control, notify affected users through the approved process,
and monitor reuse/login signals for at least one token lifetime plus refresh
grace window.

**Verify:** old refresh tokens fail, old access tokens are rejected/revoked,
unrecognized devices are absent, and the reuse alert no longer fires.

### 2.3 Secret or Signing-Key Exposure

**Detect:** secret scanner finding, public artifact/log, vault anomaly,
unauthorized signing, provider notification, or suspected host compromise.

1. Treat the secret as compromised even if exposure duration is unknown.
2. Revoke or disable it at the authority first; do not merely delete the leaked
   copy.
3. Inventory every environment, service, artifact, backup, and automation path
   that used it. Separate production from non-production rotations.
4. Issue a replacement through the secret manager; deploy consumers through
   the normal audited pipeline.
5. For JWT signing keys, follow the add-before-retire procedure in section 5.
6. Purge the value from logs/artifacts where supported, but do not rewrite git
   history without security and repository-owner approval. Revocation is the
   primary control.
7. Search audit logs for use during the exposure window and widen scope if the
   secret granted lateral access.
8. Verify old credentials fail and all consumers use the replacement.

### 2.4 Container or Host Compromise

A compromised container, image, writable layer, host, and attached writable
volumes are untrusted until proven otherwise. Follow `DEVOPS.md` section 27 and
these controls:

1. Isolate the host/container from ingress and egress; preserve `inspect`,
   process, network, image-digest, filesystem-diff, orchestration, and cloud
   audit evidence.
2. Determine whether host namespaces, container socket, credentials, sibling
   services, mounted volumes, CI, registry, or control plane were reachable.
3. Rotate every reachable credential and key. Assume environment variables and
   mounted secrets were read.
4. Destroy rather than clean the affected compute and writable volumes after
   evidence capture. Never return a repaired container to service.
5. Rebuild from reviewed source and a verified base-image digest. Recreate the
   schema from migrations or restore only from a backup proven to predate the
   compromise and pass integrity checks.
6. Scan the replacement image, generate/compare its SBOM, verify non-root and
   hardening controls, then run integration and smoke tests before traffic.
7. Search for the indicators across fleet, registry, CI, hosts, backups, and
   logs. A clean sibling container does not prove a clean host.

---

## 3. Database Recovery

PostgreSQL targets are RPO <= 5 minutes and RTO <= 1 hour. Restore operations
are led by the database lead and approved by the IC. Never restore over the
only surviving copy.

### 3.1 Choose the Recovery Path

| Condition | Recovery path |
|---|---|
| Primary unavailable; replica healthy and caught up | Promote the verified replica |
| Accidental delete/corruption with known time | Restore base backup and replay WAL to a point immediately before the event |
| Logical corruption with uncertain start | Restore candidates to isolation; compare and select the last verified clean point |
| Suspected database/container compromise | Rebuild trusted infrastructure; restore only a backup verified clean and rotate all reachable credentials/keys |
| Schema migration failure | Stop rollout; preserve state; use approved safe down migration only when proven, otherwise forward-fix |

### 3.2 PITR Procedure

1. Freeze or redirect writes and record the final known-good timestamp, current
   WAL location, replication lag, migration version, application image digest,
   and incident timeline.
2. Verify base-backup checksum/signature, encryption metadata, object version,
   retention lock, and WAL continuity before restore.
3. Restore into an isolated network with no application traffic and no outbound
   access beyond required control services.
4. Replay WAL to the approved target timestamp. Never guess a target in the
   production instance.
5. Validate PostgreSQL startup logs, roles/extensions, `schema_migrations`,
   referential integrity, constraints, critical table counts, and sequence
   floors. Reconcile DB references with media storage.
6. Start one application instance against the restored database. Run health,
   readiness, authentication, conversation, message send, replay/idempotency,
   history, reaction, edit/delete, and read-receipt flows.
7. Measure data loss and restore duration against RPO/RTO. IC and database lead
   approve promotion.
8. Cut over through the staged deployment path. Monitor errors, slow queries,
   replication, sequence conflicts, outbox lag, and receipt lag.
9. Retain the old environment read-only until the incident retention decision;
   then securely destroy it.

### 3.3 Database Credential Rotation

Use distinct credentials for superuser/bootstrap, migrator, application, read
replica, backup, and observability roles.

1. Create a new version in the managed secret store; never overwrite the only
   working value before consumers can roll.
2. Create/alter the database credential over a protected administrative path.
3. Roll consumers one role at a time. Readiness must prove new connections can
   be established; observe pool errors and authentication failures.
4. Drain old connections and revoke the old credential.
5. Verify old authentication fails, migrations still run only as migrator, the
   app cannot perform migration/admin actions, and backups remain read-only.
6. Record the secret version, rotation time, operators, consumers, and evidence
   without recording the secret.

For the local compose environment, rotate values in the gitignored
`infra/docker/.env` and recreate the disposable stack. Local `.env` files are
not an accepted production secret mechanism.

---

## 4. Backup Verification

Backups are encrypted off-machine, access-controlled, audited, and preferably
immutable. Backup keys are separate from data and runtime keys. A successful
upload is not a verified backup.

### 4.1 Continuous Checks

- Page on failed/missing base backups, broken WAL archiving, checksum errors,
  retention gaps, object mutation/deletion, backup-role privilege drift, or
  storage capacity risk.
- Monitor last successful base backup, last archived/retrievable WAL segment,
  oldest recoverable timestamp, restore-point age, backup size anomalies, and
  encryption/key metadata.
- Validate media backup completion and DB-to-media reconciliation results.

### 4.2 Monthly Restore Drill

1. Select a random retained base backup and a target timestamp after it.
2. Restore into isolated disposable infrastructure with production egress
   disabled and least-privilege drill credentials.
3. Verify checksums/signatures, decrypt using the documented recovery path,
   and replay WAL to the selected timestamp.
4. Validate schema version, constraints, row-count ranges, newest/oldest sample
   records, and required roles/extensions.
5. Run a business-flow test: register/authenticate a test identity, create or
   access a conversation, send a message, retry the same client message id,
   list history, react, and advance read/delivery receipts.
6. Reconcile media references and verify one representative object per storage
   class when media exists.
7. Record measured RPO/RTO, backup identifiers, checks performed, failures,
   follow-ups, and the destruction of the drill environment.

A drill that cannot complete is a SEV-2 until a verified recovery path exists.

---

## 5. Credential and Key Rotation

### 5.1 Rotation Policy

- Rotate immediately after suspected disclosure, unauthorized access, staff or
  vendor offboarding, control-plane compromise, or cryptographic deprecation.
- Rehearse quarterly and rotate on the approved schedule even without an
  incident.
- Use dual-version overlap where the protocol supports it. Never distribute
  private material through chat, tickets, source control, image layers, or CI
  logs.
- Rotation is complete only when old material is revoked and rejection is
  verified.

### 5.2 JWT Signing-Key Rotation (Add Before Retire)

1. Generate a new Ed25519 key in the approved KMS/vault; assign a unique `kid`.
2. Publish the new public key/JWKS entry while the old key remains valid.
3. Verify every verifier has fetched/accepted the new key and unknown algorithms
   or missing/duplicate `kid` values remain rejected.
4. Switch signing to the new key and observe signature failures.
5. Keep the old public verification key for at least the maximum access-token
   lifetime plus clock-skew/cache allowance. The old private key is no longer
   used.
6. Remove/revoke the old key after overlap; verify old tokens expire/reject as
   designed and new tokens validate across all instances.
7. For compromise, revoke sessions/token families as required; normal overlap
   may be shortened only by the security lead with user-impact awareness.

### 5.3 Encryption and Backup Keys

Use envelope encryption. Rotate the key-encryption key in KMS/vault, rewrap
data keys without decrypting bulk data where supported, sample-decrypt old and
new objects, and retain old key versions only for the documented backup
retention window. Test disaster access to KMS independently of the primary
region. Key deletion requires evidence that no retained data depends on it.

### 5.4 Other Credentials

- **Redis:** introduce a new ACL user/password, roll clients, observe auth and
  reconnect metrics, then revoke the old ACL identity. Redis is never public.
- **Cloud/CI/registry:** issue a new scoped identity, update automation, run a
  dry-run/build/deploy, revoke old tokens, and audit use during exposure.
- **Push providers:** upload the replacement, send staging canaries, roll
  production, monitor provider errors, then revoke the old key.
- **TLS/pinning:** provision and validate the new certificate/pin before
  retiring the old one; preserve overlap for shipped clients.

---

## 6. Disaster Recovery

Quarterly exercises validate the targets in `ARCHITECTURE.md` section 35 and
`DEVOPS.md` sections 13-14.

### 6.1 Region Loss

1. Declare SEV-0/SEV-1 and freeze normal deployments.
2. Confirm standby replication health, lag, media replication, secret/KMS
   availability, image digest, migrations, Terraform state, and capacity.
3. Fence the failed primary region to prevent split brain.
4. Promote the verified standby and deploy the same immutable image artifact.
5. Apply health/readiness and business-flow checks before edge failover.
6. Shift traffic gradually through Cloudflare/LB; monitor SLOs, auth, PG,
   Redis, sequence/outbox/receipt lag, and media error rates.
7. Re-establish backups and a new standby before declaring recovery complete.

### 6.2 Redis Loss

- Cache Redis fails open to PostgreSQL; rate/latency and database saturation
  are watched closely.
- Realtime/queue Redis degrades to sync behavior. Redis never becomes the only
  source of business data.
- For sequence recovery, clear/recreate hot keys only after confirming the
  durable `conversation_sequences` floor; the allocator bootstraps above that
  floor and the message composite primary key remains the final guard.
- Restore AOF/replica as appropriate, verify queue/stream ownership, and watch
  duplicate processing, sequence conflicts, and memory/eviction policy.

### 6.3 Media Loss

Restore encrypted backups, mark unavailable assets as restoring, reconcile
database references/checksums, regenerate derivatives where possible, and
return access in stages. Signed URL and membership checks remain enforced
during degraded operation.

### 6.4 Exercise Evidence

Every drill records scenario, participants, timestamps, actual RPO/RTO,
failover/failback steps, user-visible behavior, alerts fired, gaps, owners, and
due dates. A target missed in a drill remains an open reliability/security risk.

---

## 7. Production Deployment Security Checklist

This checklist supplements `DEVOPS.md` section 24 and `SECURITY.md` Appendix B.

### 7.1 Before Build

- [ ] Change approved; threat model/authz/privacy impact reviewed where relevant
- [ ] Migration additive-first, tested on representative data, and recovery path documented
- [ ] `CHANGELOG.md`, API/database/operations documentation, dashboards, and runbooks updated
- [ ] No credentials, private keys, PII, message content, or debug endpoints added
- [ ] Dependencies reviewed; lock files committed; licenses and provenance acceptable

### 7.2 Quality and Supply-Chain Gates

- [ ] Formatting, unit, integration, race, lint/vet, and build gates pass
- [ ] `govulncheck`, dependency, gitleaks, IaC, and filesystem/image scans pass or have an approved exception
- [ ] Image built once from reviewed source; base and produced image identified by digest
- [ ] SBOM and provenance/attestation attached to the artifact; artifact signed where supported
- [ ] Migration and application artifacts are immutable and use separate least-privilege identities

### 7.3 Environment and Data

- [ ] Production secrets come from the managed secret store; no `.env` or image-layer secrets
- [ ] DB/Redis/storage are private, encrypted, least-privilege, and reachable only from required networks
- [ ] Backup health and a recent successful restore drill are verified before a high-risk/stateful release
- [ ] Terraform plan reviewed; remote state locked/encrypted; origin remains locked to Cloudflare/Tunnel
- [ ] TLS, WAF, bot, rate limits, body limits, headers, and admin-plane controls verified

### 7.4 Rollout

- [ ] Migrations run first with the migrator role and verified schema version
- [ ] Same image promoted staging to production; health/readiness and smoke pass in staging
- [ ] On-call, rollback owner, dashboard links, alert expectations, and observation window named
- [ ] Canary rollout uses automatic metric gates for errors, latency, readiness, PG/Redis saturation, and queue/outbox lag
- [ ] Old instances drain gracefully; rollback/forward-fix decision is pre-authorized

### 7.5 After Rollout

- [ ] Health/readiness and representative authenticated business flows pass
- [ ] No migration, auth, sequence-conflict, deadlock, pool-saturation, outbox, receipt, or backup alerts
- [ ] Audit and operational logs contain identifiers/correlation only, not secrets/content
- [ ] Artifact digest, schema version, tests, approvals, and observation results recorded
- [ ] Temporary access, flags, exceptions, and emergency rules removed or assigned expiry/owner

---

## 8. Container Hardening

The repository's `server/Dockerfile` already uses a multi-stage build,
distroless runtime, static binary, and non-root user. Production orchestration
must additionally enforce:

- Pin base images by reviewed digest; rebuild for security fixes and verify the
  resulting image digest. Mutable tags alone are insufficient provenance.
- Run as non-root with `no-new-privileges`; drop all Linux capabilities and add
  back only a documented requirement.
- Read-only root filesystem; writable `tmpfs` only where required; no Docker
  socket, host PID/network namespace, privileged mode, or broad host mounts.
- Default seccomp/AppArmor/SELinux policy, resource limits, PID limits, and
  bounded logs.
- Secrets via runtime secret mounts/injection, never build args, Dockerfile
  `ENV`, copied files, or image labels.
- Private service networks; expose only the proxy port. PostgreSQL and Redis
  are not internet-published in production.
- Signed/scanned images, SBOM/provenance, admission policy, and deploy by digest.
- Health/readiness probes with bounded timeouts; graceful termination and
  restart policy tested.

`infra/docker/docker-compose.yml` is a local development stack: it publishes
database/Redis ports, uses local secret interpolation, and does not represent
production hardening. Never deploy it unchanged to production.

---

## 9. Supply-Chain Security

- Pin and review dependencies and actions; commit `go.sum` and all lock files.
  New dependencies require owner, maintenance, vulnerability, license, and
  transitive-risk review.
- CI runs `govulncheck`, dependency scanning, gitleaks, Trivy, and IaC scans.
  Critical/high findings block release unless the security lead documents a
  time-bounded compensating-control exception.
- Build in an ephemeral, least-privilege runner with protected release
  environments. Pull-request code does not receive production secrets.
- Generate an SBOM, artifact provenance, source revision, builder identity,
  base-image digest, and produced image digest. Sign and verify release
  artifacts where supported.
- Protect branches/tags; require review and green checks; version tags are
  immutable. Audit CI, registry, and release access.
- Patch actively exploited vulnerabilities immediately; target critical within
  7 days, high within 30 days, and rebuild images even when application source
  is unchanged.
- On dependency/build compromise, freeze releases, revoke CI/registry tokens,
  identify affected artifacts, block their digests, rebuild from a trusted
  runner/source, and notify consumers through the incident process.

---

## 10. Monitoring and Alerting

Every page links to an owner and this or a more specific runbook. Alert rules
are code-reviewed and tested. Required signals include:

| Area | Page conditions |
|---|---|
| Authentication | Refresh reuse burst, session-revoke storm, login/OTP anomaly, new-region/device spike |
| Authorization/data | Authz-denial spike, unusual sensitive-table reads, export/download surge, admin-action anomaly |
| API/messaging | SLO burn, 5xx spike, send latency, sequence conflicts, transaction deadlocks/serialization failures, PG pool saturation, outbox/receipt lag |
| PostgreSQL | Down/unready, replication/WAL lag, failed migration, storage near-full, slow-query jump, backup failure/gap |
| Redis | Down, auth failures, memory/eviction/noeviction risk, AOF/replication failure, reconnect storm |
| Edge/network | Origin bypass, WAF/bot anomaly, TLS expiry, DDoS, unusual geography/egress |
| Containers/supply chain | Runtime drift, unexpected process/egress, image/signature policy failure, critical vulnerability/secret finding |
| Recovery | Restore drill failure, RPO/RTO miss, standby unhealthy, immutable backup mutation |

Dashboards correlate `request_id`, trace, user/session/conversation identifiers,
deployment revision, image digest, and schema version. Message content, OTPs,
tokens, keys, and raw credentials are never telemetry labels or log fields.

Alert verification occurs during staging rollout and quarterly drills. An alert
that cannot be acted on is fixed or removed; a missing alert for an operational
failure is a release gap.

---

## 11. Release Checklist and Evidence

A release manager completes and archives this concise gate after the detailed
checklists above:

- [ ] Approved source revision and atomic commits identified
- [ ] Unit, integration, full race, lint/vet, and build results attached
- [ ] Security/supply-chain scans green; SBOM, provenance, image digest attached
- [ ] Documentation and changelog current; migration and recovery reviewed
- [ ] Backup/standby posture verified for stateful changes
- [ ] Staging health, readiness, migration, and authenticated smoke flows pass
- [ ] Production canary owner, dashboards, alerts, rollback, and watch window named
- [ ] Canary/full rollout results and post-release business-flow evidence recorded
- [ ] Temporary access/exceptions removed; open risks have owner and expiry

Release evidence must be sufficient for another operator to answer: exactly
what ran, from which source, with which schema, what tests and controls passed,
who approved it, how it was observed, and how it can be recovered.

---

## 12. Operating Cadence

- **Weekly:** vulnerability/dependency/image updates, backup and alert review,
  privileged-access review, and base-image rebuild where needed.
- **Monthly:** full backup restore drill, credential inventory, certificate and
  retention review, on-call/runbook review, and measured RPO/RTO report.
- **Quarterly:** region/Redis/database failover exercise, key-rotation rehearsal,
  security tabletop, threat-model update, and access recertification.
- **After every incident or failed drill:** update this runbook and its source
  standards, assign corrective actions, and verify closure.

The security lead and SRE owner review this document quarterly. The repository
history is the change record; operational drill and incident evidence stays in
the access-controlled evidence system, not in git.
